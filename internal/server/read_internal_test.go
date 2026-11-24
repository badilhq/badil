package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/status"
)

const (
	testReadToken   = "test-read-token"
	testDeployToken = "test-deploy-token"
)

// readBadil is a catalog without needs: backend-1, front on develop exposed
// at /app, and the dependency postgres, with https preview URLs.
const readBadil = `services:
  backend-1:
    repo: https://github.com/acme/backend-1.git
  front:
    repo: https://github.com/acme/front.git
    branch: develop
dependencies:
  postgres:
    chart: oci://registry.example.test/charts/postgresql
    version: 1.0.0
preview:
  domain: example.test
  expose:
    front:
      path: /app
`

// needsBadil is a catalog with needs: front needs backend-1, which needs
// postgres; search needs nothing.
const needsBadil = `services:
  backend-1:
    repo: https://github.com/acme/backend-1.git
    needs: [postgres]
  front:
    repo: https://github.com/acme/front.git
    needs: [backend-1]
  search:
    repo: https://github.com/acme/search.git
dependencies:
  postgres:
    chart: oci://registry.example.test/charts/postgresql
    version: 1.0.0
`

const (
	backend42 = "https://github.com/acme/backend-1/pull/42"
	front7    = "https://github.com/acme/front/pull/7"
)

// pinnedEntry is service pinned by prURL at statusSHA's 7-character prefix.
func pinnedEntry(service, prURL string) preview.ServiceEntry {
	return preview.ServiceEntry{
		Name: service, CommitSha: statusSHA[:7],
		Metadata: &preview.Metadata{
			PRAuthor: "octocat", PRURL: prURL, PRNumber: "42",
			CreatedAt: "2026-11-19T20:00:00Z", UpdatedAt: statusUpdatedAt,
		},
	}
}

// readFake is a fakeGitops with badil as its catalog and the previews
// feature-x, whose only pinned entry is pinned, and bare, which pins nothing.
func readFake(badil string, pinned preview.ServiceEntry) *fakeGitops {
	return &fakeGitops{
		badilYAML: badil,
		previews: map[string]*preview.Values{
			"feature-x": {Services: []preview.ServiceEntry{pinned}},
			"bare":      {},
		},
	}
}

// newReadServer returns a server over fg with the deploy token, the read
// token and a memory status store, configured by opts, whose waits answer
// at once unless the test sets waitTimeout.
func newReadServer(
	t *testing.T, fg *fakeGitops, cfg app.Config, opts ...Option,
) (*Server, *status.Store, *LogCapture) {
	t.Helper()
	st := status.NewMemory()
	s, capture := newTeardownServer(t, fg, cfg, append([]Option{
		WithStatus(testStatusToken, st), WithReadToken(testReadToken),
	}, opts...)...)
	s.waitTimeout = time.Millisecond
	return s, st, capture
}

// get sends GET path to s with the read token.
func get(s *Server, path string) *httptest.ResponseRecorder {
	return send(s, http.MethodGet, path, "{}", "Bearer "+testReadToken)
}

// send sends method path with body to s, with the Authorization header when
// authorization is set.
func send(s *Server, method, path, body, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// decode decodes rec's body into a T, failing t unless rec has code.
func decode[T any](t *testing.T, rec *httptest.ResponseRecorder, code int) T {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d %q, want %d", rec.Code, rec.Body.String(), code)
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

// waitFor returns the 200 wait response of GET path.
func waitFor(t *testing.T, s *Server, path string) WaitResponse {
	t.Helper()
	return decode[WaitResponse](t, get(s, path), http.StatusOK)
}

// assertState fails t unless got is in state with reason.
func assertState(t *testing.T, got WaitResponse, state, reason string) {
	t.Helper()
	if got.State != state || got.Reason != reason {
		t.Errorf("wait = %s %q, want %s %q", got.State, got.Reason, state, reason)
	}
}

// record records the status of service in feature-x.
func record(st *status.Store, service, revision, s string) {
	st.Record("feature-x", service, status.Entry{Revision: revision, Status: s, At: time.Now()})
}

func TestReadAPI_Auth(t *testing.T) {
	type call struct {
		method, path, token string
		want                int
	}
	getRoutes := []string{
		"/v1/previews",
		"/v1/previews/feature-x",
		"/v1/previews/feature-x/wait",
		"/v1/wait?repo=acme/front&pr=7",
	}
	cases := []struct {
		name          string
		deploy, read  string
		calls         []call
		allGetsStatus int
	}{
		{
			name: "both tokens", deploy: testDeployToken, read: testReadToken, allGetsStatus: http.StatusOK,
			calls: []call{
				{http.MethodGet, "/v1/previews", testDeployToken, http.StatusOK},
				{http.MethodGet, "/v1/previews", "wrong", http.StatusUnauthorized},
				{http.MethodGet, "/v1/previews", "", http.StatusUnauthorized},
				{http.MethodPost, "/v1/deploy", testReadToken, http.StatusUnauthorized},
				{http.MethodPost, "/v1/sync", testReadToken, http.StatusUnauthorized},
				{http.MethodPost, "/v1/deploy", testDeployToken, http.StatusBadRequest},
			},
		},
		{
			name: "deploy token only", deploy: testDeployToken, allGetsStatus: http.StatusUnauthorized,
			calls: []call{
				{http.MethodGet, "/v1/previews", testDeployToken, http.StatusOK},
				{http.MethodGet, "/v1/previews/feature-x/wait", testDeployToken, http.StatusOK},
			},
		},
		{
			name: "read token only", read: testReadToken, allGetsStatus: http.StatusOK,
			calls: []call{
				{http.MethodGet, "/v1/previews", testDeployToken, http.StatusUnauthorized},
				{http.MethodPost, "/v1/deploy", testReadToken, http.StatusNotFound},
				{http.MethodPost, "/v1/sync", testReadToken, http.StatusNotFound},
			},
		},
		{
			name: "no token", allGetsStatus: http.StatusNotFound,
			calls: []call{
				{http.MethodPost, "/v1/deploy", "", http.StatusNotFound},
				{http.MethodPost, "/v1/sync", "", http.StatusNotFound},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := readFake(readBadil, pinnedEntry("backend-1", backend42))
			fg.open(t)
			a := app.New(app.Config{Store: fg, Forge: fg})
			s := NewServer(a, fg, nil, fg, tc.deploy, WithReadToken(tc.read), WithStatus("", status.NewMemory()))
			s.waitTimeout = time.Millisecond
			for _, path := range getRoutes {
				if rec := send(s, http.MethodGet, path, "{}", "Bearer "+testReadToken); rec.Code != tc.allGetsStatus {
					t.Errorf("GET %s with the read token = %d, want %d", path, rec.Code, tc.allGetsStatus)
				}
			}
			for _, c := range tc.calls {
				auth := ""
				if c.token != "" {
					auth = "Bearer " + c.token
				}
				if rec := send(s, c.method, c.path, "{}", auth); rec.Code != c.want {
					t.Errorf("%s %s with %q = %d, want %d", c.method, c.path, c.token, rec.Code, c.want)
				}
			}
		})
	}
}

func TestReadAPI_ListAndGet(t *testing.T) {
	fg := readFake(readBadil, pinnedEntry("backend-1", backend42))
	s, st, _ := newReadServer(t, fg, app.Config{}, WithPreviewTTL(168*time.Hour))
	at := time.Date(2026, 11, 19, 21, 5, 0, 0, time.UTC)
	st.Record("feature-x", "backend-1", status.Entry{Revision: statusSHA, Status: status.Ready, At: at})

	unpinned := func(slug string) []PreviewEntryJSON {
		return []PreviewEntryJSON{
			{Name: "backend-1", Kind: "service", Branch: "main", Status: "unknown"},
			{
				Name:   "front",
				Kind:   "service",
				Branch: "develop",
				URL:    "https://front-" + slug + ".example.test/app",
				Status: "unknown",
			},
			{Name: "postgres", Kind: "dependency", Status: "unknown"},
		}
	}
	featureX := PreviewJSON{
		Slug:      "feature-x",
		CreatedAt: "2026-11-19T20:00:00Z",
		UpdatedAt: statusUpdatedAt,
		ExpiresAt: "2026-11-26T21:00:00Z",
		Services:  unpinned("feature-x"),
	}
	featureX.Services[0] = PreviewEntryJSON{
		Name:   "backend-1",
		Kind:   "service",
		Branch: "main",
		Pinned: &PinnedJSON{
			SHA:    statusSHA[:7],
			PR:     PRJSON{Repo: "acme/backend-1", Number: 42, URL: backend42},
			Author: "octocat",
		},
		Status:   "ready",
		Revision: statusSHA,
		StatusAt: "2026-11-19T21:05:00Z",
	}
	bare := PreviewJSON{Slug: "bare", Services: unpinned("bare")}

	list := decode[PreviewList](t, get(s, "/v1/previews"), http.StatusOK)
	if want := (PreviewList{Previews: []PreviewJSON{bare, featureX}}); !reflect.DeepEqual(list, want) {
		t.Errorf("GET /v1/previews = %+v, want %+v", list, want)
	}
	if got := decode[PreviewJSON](
		t,
		get(s, "/v1/previews/feature-x"),
		http.StatusOK,
	); !reflect.DeepEqual(
		got,
		featureX,
	) {
		t.Errorf("GET /v1/previews/feature-x = %+v, want %+v", got, featureX)
	}
	rec := get(s, "/v1/previews/bare")
	if body := rec.Body.String(); strings.Contains(body, "createdAt") || strings.Contains(body, "updatedAt") ||
		strings.Contains(body, "expiresAt") {
		t.Errorf("GET /v1/previews/bare = %s, want no createdAt, updatedAt nor expiresAt", body)
	}
	for _, slug := range []string{"missing", "..%2Fx"} {
		got := decode[ErrorResponse](t, get(s, "/v1/previews/"+slug), http.StatusNotFound)
		if got.Error != "preview not found" {
			t.Errorf("GET /v1/previews/%s error = %q, want preview not found", slug, got.Error)
		}
	}
}

func TestReadAPI_NoExpiryWithoutTTL(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)), app.Config{})
	got := decode[PreviewJSON](t, get(s, "/v1/previews/feature-x"), http.StatusOK)
	if got.UpdatedAt != statusUpdatedAt || got.ExpiresAt != "" {
		t.Errorf("updatedAt %q expiresAt %q, want %q and none", got.UpdatedAt, got.ExpiresAt, statusUpdatedAt)
	}
}

func TestWait_OutcomeStates(t *testing.T) {
	result := func(outcome, reason string) lifecycle.ReconcileResult {
		return lifecycle.ReconcileResult{Outcome: outcome, Reason: reason, HeadSHA: statusSHA}
	}
	skipped := func(reason string) lifecycle.ReconcileResult { return result(lifecycle.OutcomeSkipped, reason) }
	cases := []struct {
		res           lifecycle.ReconcileResult
		sha           string
		state, reason string
	}{
		{res: skipped("service held by another pr"), state: "refused", reason: "service held by another pr"},
		{res: skipped("preview limit reached"), state: "refused", reason: "preview limit reached"},
		{res: skipped("namespace in use"), state: "refused", reason: "namespace in use"},
		{res: skipped("conflicting preview labels"), state: "refused", reason: "conflicting preview labels"},
		{res: skipped("fork pr"), state: "refused", reason: "fork pr"},
		{res: skipped("repo not in catalog"), state: "refused", reason: "repo not in catalog"},
		{res: result(lifecycle.OutcomeTornDown, ""), state: "refused", reason: "torn down"},
		{res: skipped("no service changed"), state: "skipped", reason: "no service changed"},
		{res: skipped("not labeled"), state: "skipped", reason: "not labeled"},
		{res: result(lifecycle.OutcomeFailed, ""), state: "failed", reason: "reconcile failed"},
		{res: skipped("not labeled"), sha: statusSHA[:7], state: "skipped", reason: "not labeled"},
		{res: skipped("not labeled"), sha: strings.ToUpper(statusSHA), state: "skipped", reason: "not labeled"},
		{res: skipped("not labeled"), sha: "fff", state: "pending"},
		{res: skipped("already deployed"), state: "progressing"},
		{res: result(lifecycle.OutcomeDeployed, ""), state: "progressing"},
	}
	for _, tc := range cases {
		t.Run(tc.res.Outcome+" "+tc.res.Reason+" "+tc.sha, func(t *testing.T) {
			s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("front", front7)), app.Config{})
			s.app.RecordOutcome(lifecycle.Target{Owner: "Acme", Repo: "Front", Number: 7}, tc.res)
			path := "/v1/wait?repo=acme/front&pr=7"
			if tc.sha != "" {
				path += "&sha=" + tc.sha
			}
			assertState(t, waitFor(t, s, path), tc.state, tc.reason)
		})
	}
}

func TestWait_OtherLabelLeavesTheAnswer(t *testing.T) {
	fg := reconcileFake()
	s, _, _ := newReadServer(t, fg, app.Config{})
	path := "/v1/wait?repo=acme/backend-1&pr=42"
	deliver(t, s, labelEvent("opened", ""))
	assertState(t, waitFor(t, s, path), "skipped", "not labeled")

	deliver(t, s, labelEvent("labeled", "bug", "bug"))
	assertState(t, waitFor(t, s, path), "skipped", "not labeled")
	if fg.prCalls != 0 {
		t.Errorf("PR read %d times, want none", fg.prCalls)
	}
}

func TestWait_WebhookUnlabeledPRSkipped(t *testing.T) {
	fg := reconcileFake()
	s, _, _ := newReadServer(t, fg, app.Config{})
	e := prEvent("opened")
	e.PullRequest.HeadSHA = statusSHA
	deliver(t, s, e)

	got := waitFor(t, s, "/v1/wait?repo=acme/backend-1&pr=42&sha="+statusSHA)
	assertState(t, got, "skipped", "not labeled")
	if got.SHA != statusSHA {
		t.Errorf("sha = %q, want %q", got.SHA, statusSHA)
	}
}

func TestWait_FailedReconcile(t *testing.T) {
	fg := reconcileFake()
	fg.prErr = forge.ErrNotFound
	s, _, _ := newReadServer(t, fg, app.Config{})
	deliver(t, s, labelEvent("labeled", "preview", "preview"))
	assertState(t, waitFor(t, s, "/v1/wait?repo=acme/backend-1&pr=42"), "failed", "reconcile failed")
	assertState(t, waitFor(t, s, "/v1/wait?repo=acme/backend-1&pr=42&sha="+payloadSHA), "failed", "reconcile failed")
}

func TestWait_RepoNotInCatalog(t *testing.T) {
	fg := readFake(readBadil, pinnedEntry("front", front7))
	s, _, _ := newReadServer(t, fg, app.Config{})
	path := "/v1/wait?repo=acme/elsewhere&pr=3"
	assertState(t, waitFor(t, s, path), "refused", "repo not in catalog")

	s.app.RecordOutcome(lifecycle.Target{Owner: "acme", Repo: "elsewhere", Number: 3},
		lifecycle.ReconcileResult{Outcome: lifecycle.OutcomeSkipped, Reason: lifecycle.NotLabeledReason})
	assertState(t, waitFor(t, s, path), "refused", "repo not in catalog")
}

func TestWait_CatchUpRecordsTheFilteredPRs(t *testing.T) {
	unlabeled := forge.PullRequest{
		Open: true, HeadRef: "feature/a", HeadSHA: "aaaa111", URL: "https://github.com/acme/front/pull/1",
		FullName: "acme/front", HeadFullName: "acme/front", Number: 1, Labels: []string{"bug"},
	}
	fork := unlabeled
	fork.HeadSHA, fork.URL, fork.Number, fork.Labels = "bbbb222", "https://github.com/acme/front/pull/2", 2, []string{
		"preview",
	}
	fork.HeadFullName = "octocat/front"
	fg := readFake(readBadil, pinnedEntry("front", front7))
	fg.openPRs = map[string][]forge.PullRequest{"acme/front": {unlabeled, fork}}
	s, _, _ := newReadServer(t, fg, app.Config{})
	reconciled := 0
	reconcile := func(ctx context.Context, ev lifecycle.Event, done func(lifecycle.ReconcileResult)) {
		reconciled++
		s.app.QueueReconcile(ctx, ev, done)
	}
	api := lifecycle.CatchUpAPI{
		Store:    fg,
		Forge:    fg,
		Recorder: (*metrics.Metrics)(nil),
		Skipped:  s.app.RecordCatchUpSkip,
	}
	lifecycle.NewCatchUp(api, reconcile).Run(context.Background())
	if err := s.app.WaitBackground(context.Background()); err != nil {
		t.Fatal(err)
	}

	if reconciled != 0 {
		t.Errorf("reconciled %d PRs, want none", reconciled)
	}
	assertState(t, waitFor(t, s, "/v1/wait?repo=acme/front&pr=1&sha=aaaa111"), "skipped", "not labeled")
	assertState(t, waitFor(t, s, "/v1/wait?repo=acme/front&pr=2&sha=bbbb222"), "refused", "fork pr")
}

func TestWait_CatchUpSkipAndNewerOutcome(t *testing.T) {
	unlabeled := forge.PullRequest{
		Open: true, HeadRef: "feature/x", HeadSHA: statusSHA, URL: front7,
		FullName: "acme/front", HeadFullName: "acme/front", Number: 7, Labels: []string{"bug"},
	}
	target := lifecycle.Target{Owner: "acme", Repo: "front", Number: 7}
	deployed := lifecycle.ReconcileResult{Outcome: lifecycle.OutcomeDeployed, HeadSHA: statusSHA}
	path := "/v1/wait?repo=acme/front&pr=7&sha=" + statusSHA
	run := func(t *testing.T, fg *fakeGitops, s *Server) {
		t.Helper()
		api := lifecycle.CatchUpAPI{
			Store: fg, Forge: fg, Recorder: (*metrics.Metrics)(nil), Skipped: s.app.RecordCatchUpSkip,
		}
		lifecycle.NewCatchUp(api, s.app.QueueReconcile).Run(context.Background())
		if err := s.app.WaitBackground(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("an outcome recorded after the listing stays", func(t *testing.T) {
		fg := readFake(readBadil, pinnedEntry("front", front7))
		fg.openPRs = map[string][]forge.PullRequest{"acme/front": {unlabeled}}
		s, _, _ := newReadServer(t, fg, app.Config{})
		fg.afterList = func() { s.app.RecordOutcome(target, deployed) }
		run(t, fg, s)
		assertState(t, waitFor(t, s, path), "progressing", "")
	})

	t.Run("a listing started after the outcome replaces it", func(t *testing.T) {
		fg := readFake(readBadil, pinnedEntry("front", front7))
		fg.openPRs = map[string][]forge.PullRequest{"acme/front": {unlabeled}}
		s, _, _ := newReadServer(t, fg, app.Config{})
		s.app.RecordOutcome(target, deployed)
		run(t, fg, s)
		assertState(t, waitFor(t, s, path), "skipped", "not labeled")
	})
}

func TestRecordCatchUpSkip_ListingTime(t *testing.T) {
	target := lifecycle.Target{Owner: "acme", Repo: "front", Number: 7}
	skip := lifecycle.ReconcileResult{
		Outcome: lifecycle.OutcomeSkipped, Reason: lifecycle.NotLabeledReason, HeadSHA: statusSHA,
	}
	path := "/v1/wait?repo=acme/front&pr=7"
	cases := []struct {
		name          string
		listedAt      func(recorded time.Time) time.Time
		state, reason string
	}{
		{
			name: "listed before", listedAt: func(r time.Time) time.Time { return r.Add(-time.Second) },
			state: "progressing",
		},
		{name: "listed at", listedAt: func(r time.Time) time.Time { return r }, state: "progressing"},
		{
			name: "listed after", listedAt: func(r time.Time) time.Time { return r.Add(time.Second) },
			state: "skipped", reason: "not labeled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("front", front7)), app.Config{})
			s.app.RecordOutcome(
				target,
				lifecycle.ReconcileResult{Outcome: lifecycle.OutcomeDeployed, HeadSHA: statusSHA},
			)
			recorded, _ := s.app.LatestOutcome(target)
			s.app.RecordCatchUpSkip(target, skip, tc.listedAt(recorded.At))
			assertState(t, waitFor(t, s, path), tc.state, tc.reason)
		})
	}

	t.Run("no stored outcome", func(t *testing.T) {
		s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("front", front7)), app.Config{})
		s.app.RecordCatchUpSkip(target, skip, time.Now())
		assertState(t, waitFor(t, s, path), "skipped", "not labeled")
	})
}

func TestWait_PinAndWaitSet(t *testing.T) {
	s, st, _ := newReadServer(t, readFake(needsBadil, pinnedEntry("front", front7)), app.Config{})
	path := "/v1/wait?repo=acme/front&pr=7"
	assertState(t, waitFor(t, s, "/v1/wait?repo=acme/front&pr=8"), "pending", "")
	assertState(t, waitFor(t, s, path+"&sha=fff1234"), "pending", "")

	got := waitFor(t, s, path)
	assertState(t, got, "progressing", "")
	if got.Slug != "feature-x" || got.SHA != statusSHA[:7] {
		t.Errorf("slug %q sha %q, want feature-x and %q", got.Slug, got.SHA, statusSHA[:7])
	}
	var names []string
	for _, e := range got.Services {
		names = append(names, e.Kind+" "+e.Name+" "+e.Status)
	}
	if want := []string{
		"service backend-1 unknown",
		"service front unknown",
		"dependency postgres unknown",
	}; !reflect.DeepEqual(
		names,
		want,
	) {
		t.Errorf("wait set = %v, want %v", names, want)
	}

	record(st, "front", statusSHA, status.Ready)
	assertState(t, waitFor(t, s, path), "progressing", "")
	record(st, "backend-1", "0123456", status.Ready)
	assertState(t, waitFor(t, s, path), "progressing", "")
	record(st, "postgres", "1.0.0", status.Ready)
	got = waitFor(t, s, path)
	assertState(t, got, "ready", "")
	assertState(t, waitFor(t, s, "/v1/previews/feature-x/wait"), "ready", "")
	for _, e := range got.Services {
		if !e.Ready {
			t.Errorf("%s not ready in a ready wait", e.Name)
		}
	}

	record(st, "front", "fff0000", status.SyncFailed)
	assertState(t, waitFor(t, s, path), "progressing", "")
	record(st, "front", statusSHA, status.Degraded)
	assertState(t, waitFor(t, s, path), "failed", "front is degraded at "+statusSHA)
	record(st, "front", statusSHA, status.SyncFailed)
	assertState(t, waitFor(t, s, path), "failed", "front is sync-failed at "+statusSHA)
}

func TestWait_WithoutNeedsTheWaitSetIsThePins(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)), app.Config{})
	got := waitFor(t, s, "/v1/previews/feature-x/wait")
	if len(got.Services) != 1 || got.Services[0].Name != "backend-1" {
		t.Errorf("wait set = %+v, want backend-1 alone", got.Services)
	}
}

func TestWait_SlugWithoutPinIsPending(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)), app.Config{})
	got := waitFor(t, s, "/v1/previews/bare/wait")
	assertState(t, got, "pending", "")
	if got.Slug != "bare" || len(got.Services) != 0 {
		t.Errorf("slug %q services %+v, want bare and none", got.Slug, got.Services)
	}
	if e := decode[ErrorResponse](
		t,
		get(s, "/v1/previews/missing/wait"),
		http.StatusNotFound,
	); e.Error != "preview not found" {
		t.Errorf("error = %q, want preview not found", e.Error)
	}
}

func TestReadAPI_CloneReadFailure(t *testing.T) {
	fg := readFake(readBadil, pinnedEntry("backend-1", backend42))
	s, _, capture := newReadServer(t, fg, app.Config{})
	if err := os.RemoveAll(fg.remote.Dir()); err != nil {
		t.Fatal(err)
	}
	fg.st.Invalidate()
	for _, path := range []string{"/v1/previews", "/v1/previews/feature-x", "/v1/previews/feature-x/wait", "/v1/wait?repo=acme/backend-1&pr=42"} {
		got := decode[ErrorResponse](t, get(s, path), http.StatusServiceUnavailable)
		if !strings.Contains(got.Error, "ls-remote gitops repo") {
			t.Errorf("GET %s error = %q, want the ls-remote error", path, got.Error)
		}
	}
	routes := map[string]bool{}
	for _, r := range capture.Records() {
		if r.Message != "read failed" {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "route" {
				routes[a.Value.String()] = true
			}
			return true
		})
	}
	want := map[string]bool{
		"/v1/previews":             true,
		"/v1/previews/{slug}":      true,
		"/v1/previews/{slug}/wait": true,
		"/v1/wait":                 true,
	}
	if !reflect.DeepEqual(routes, want) {
		t.Errorf("read failed routes = %v, want %v", routes, want)
	}
}

// TestReadAPI_ClientGoneDuringRead checks that a read failing once the
// client has gone neither logs read failed nor answers 503.
func TestReadAPI_ClientGoneDuringRead(t *testing.T) {
	fg := readFake(readBadil, pinnedEntry("backend-1", backend42))
	s, _, capture := newReadServer(t, fg, app.Config{})
	if err := os.RemoveAll(fg.remote.Dir()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/previews", "/v1/previews/feature-x", "/v1/previews/feature-x/wait", "/v1/wait?repo=acme/backend-1&pr=42"} {
		fg.st.Invalidate()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+testReadToken)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusServiceUnavailable || rec.Body.Len() != 0 {
			t.Errorf("GET %s with the client gone = %d %q, want no answer", path, rec.Code, rec.Body.String())
		}
	}
	for _, r := range capture.Records() {
		if r.Message == "read failed" {
			t.Errorf("logged read failed for a client that has gone")
		}
	}
}

func TestWait_InvalidQuery(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)), app.Config{})
	for _, q := range []string{"pr=1", "repo=acme/front", "repo=acme/front&pr=0", "repo=acme/front&pr=abc",
		"repo=acme&pr=1", "repo=acme/front/x&pr=1", "repo=/front&pr=1", "repo=acme/&pr=1", "repo=acme/front&pr=-3"} {
		if e := decode[ErrorResponse](t, get(s, "/v1/wait?"+q), http.StatusBadRequest); e.Error == "" {
			t.Errorf("GET /v1/wait?%s: no error message", q)
		}
	}
}
