package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/api"
	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

func validBody() string {
	return `{
		"service": "backend-1",
		"head_ref": "feature/add-pricing",
		"commit_sha": "abc1234567890",
		"pr_author": "octocat",
		"pr_url": "https://github.com/acme/backend-1/pull/42",
		"pr_number": "42",
		"workflow_run_url": "https://github.com/acme/backend-1/actions/runs/1"
	}`
}

func newTestServer(t *testing.T, fg *fakeGitops) *Server {
	t.Helper()
	fg.open(t)
	a := app.New(app.Config{Store: fg, Forge: fg, FanOut: fakeFanOut(fg)})
	return NewServer(a, fg, nil, "test-token")
}

func postDeploy(t *testing.T, srv *Server, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDeploy_HappyPath(t *testing.T) {
	fg := &fakeGitops{catalog: []string{"backend-1", "backend-2", "front"}}
	rec := postDeploy(t, newTestServer(t, fg), validBody(), "test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp api.DeployResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PreviewSlug != "feature-add-pricing" {
		t.Errorf("PreviewSlug = %q, want feature-add-pricing", resp.PreviewSlug)
	}
	if resp.GitOpsCommitURL != fg.headURL() {
		t.Errorf("GitOpsCommitURL = %q, want the pushed commit %q", resp.GitOpsCommitURL, fg.headURL())
	}
	if c := fg.remote.Commits()[0]; c.Subject() != "chore(preview): deploy backend-1@abc1234 to feature-add-pricing" {
		t.Errorf("commit subject = %q", c.Subject())
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("Warnings = %v, want empty", resp.Warnings)
	}

	// Verify the handler stamped a server-side timestamp.
	if fg.capturedInputs.Timestamp == "" {
		t.Error("captured Timestamp is empty; handler must stamp it server-side")
	}
	if fg.capturedSlug != "feature-add-pricing" {
		t.Errorf("captured slug = %q", fg.capturedSlug)
	}
	if !fg.fanOutBodyCalled {
		t.Error("FanOutPRComments was not called")
	}
}

// TestDeploy_RedeployAnswersTheHeadCommit deploys the same commit twice: a
// manual deploy never skips an unchanged service, so each one answers the
// commit the preview is at and comments, whether the second one changed the
// file or, within the same second, changed nothing.
func TestDeploy_RedeployAnswersTheHeadCommit(t *testing.T) {
	fg := &fakeGitops{catalog: []string{"backend-1"}}
	srv := newTestServer(t, fg)
	for i := range 2 {
		fg.fanOutBodyCalled = false
		rec := postDeploy(t, srv, validBody(), "test-token")
		if rec.Code != http.StatusOK {
			t.Fatalf("deploy %d: status = %d, body = %s", i, rec.Code, rec.Body.String())
		}
		var resp api.DeployResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.GitOpsCommitURL != fg.headURL() {
			t.Errorf("deploy %d: GitOpsCommitURL = %q, want the head %q", i, resp.GitOpsCommitURL, fg.headURL())
		}
		if !fg.fanOutBodyCalled {
			t.Errorf("deploy %d: FanOutPRComments was not called", i)
		}
	}
}

func TestDeploy_MissingField(t *testing.T) {
	fg := &fakeGitops{catalog: []string{"backend-1"}}
	body := `{
		"service": "backend-1",
		"head_ref": "feature/x",
		"commit_sha": "",
		"pr_author": "octocat",
		"pr_url": "https://github.com/acme/backend-1/pull/1",
		"pr_number": "1",
		"workflow_run_url": "https://github.com/acme/backend-1/actions/runs/1"
	}`
	rec := postDeploy(t, newTestServer(t, fg), body, "test-token")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "commit_sha") {
		t.Errorf("body must name missing field, got: %s", rec.Body.String())
	}
}

func TestDeploy_ServiceNotInCatalog(t *testing.T) {
	fg := &fakeGitops{catalog: []string{"backend-2", "front"}}
	rec := postDeploy(t, newTestServer(t, fg), validBody(), "test-token")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "backend-1") {
		t.Errorf("body must name the missing service, got: %s", rec.Body.String())
	}
}

func TestDeploy_FanOutPartialFailureSurfacesAsWarnings(t *testing.T) {
	joined := errors.Join(
		errors.New("service backend-2 (acme/backend-2#7): rate limited"),
		errors.New("service front (acme/front#11): not found"),
	)
	fg := &fakeGitops{catalog: []string{"backend-1", "backend-2", "front"}, fanOutErr: joined}
	rec := postDeploy(t, newTestServer(t, fg), validBody(), "test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (write succeeded)", rec.Code)
	}
	var resp api.DeployResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Warnings) != 2 {
		t.Errorf("Warnings = %v, want 2 entries", resp.Warnings)
	}
}

func TestDeploy_MissingBearer(t *testing.T) {
	fg := &fakeGitops{catalog: []string{"backend-1"}}
	rec := postDeploy(t, newTestServer(t, fg), validBody(), "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if fg.capturedSlug != "" {
		t.Error("gitops handler should not be reached when bearer is missing")
	}
}

func TestDeploy_InvalidJSON(t *testing.T) {
	fg := &fakeGitops{}
	rec := postDeploy(t, newTestServer(t, fg), "{not json", "test-token")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDeploy_UnknownField(t *testing.T) {
	// DisallowUnknownFields guards against drift between client and server.
	fg := &fakeGitops{catalog: []string{"backend-1"}}
	body := `{
		"service": "backend-1",
		"head_ref": "feature/x",
		"commit_sha": "abc",
		"pr_author": "octocat",
		"pr_url": "https://github.com/acme/backend-1/pull/1",
		"pr_number": "1",
		"workflow_run_url": "https://github.com/acme/backend-1/actions/runs/1",
		"extra_field": "should be rejected"
	}`
	rec := postDeploy(t, newTestServer(t, fg), body, "test-token")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown field", rec.Code)
	}
}

const servicesWithPreview = `services:
  backend-1:
    repo: https://github.com/acme/backend-1.git
  backend-2:
    repo: https://github.com/acme/backend-2.git
  front:
    repo: https://github.com/acme/front.git
preview:
  domain: 127.0.0.1.nip.io
  tls: false
  expose:
    front: {}
    backend-1:
      path: /docs
`

func previewBody() string {
	return strings.Replace(validBody(), "feature/add-pricing", "feature/pricing", 1)
}

func TestDeploy_PreviewURLsInSummary(t *testing.T) {
	fg := &fakeGitops{badilYAML: servicesWithPreview}
	rec := postDeploy(t, newTestServer(t, fg), previewBody(), "test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		"- **front** · 🔄 `main` · [front-feature-pricing.127.0.0.1.nip.io/](http://front-feature-pricing.127.0.0.1.nip.io/)\n",
		" · [backend-1-feature-pricing.127.0.0.1.nip.io/docs](http://backend-1-feature-pricing.127.0.0.1.nip.io/docs)\n",
		"- **backend-2** · 🔄 `main`\n",
	} {
		if !strings.Contains(fg.capturedSummary, want) {
			t.Errorf("summary missing %q, got:\n%s", want, fg.capturedSummary)
		}
	}
	if strings.Contains(fg.capturedSummary, "backend-2-feature-pricing") {
		t.Errorf("summary links non-exposed backend-2, got:\n%s", fg.capturedSummary)
	}
}

func TestDeploy_ArgoCDLinkInSummary(t *testing.T) {
	yml := servicesWithPreview + "  argocdURL: https://argocd.example.com/\n"
	fg := &fakeGitops{badilYAML: yml}
	rec := postDeploy(t, newTestServer(t, fg), previewBody(), "test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	want := preview.ArgoCDAppsURL("https://argocd.example.com", "feature-pricing")
	if !strings.Contains(fg.capturedSummary, want) {
		t.Errorf("summary missing %q, got:\n%s", want, fg.capturedSummary)
	}
}

func TestDeploy_PreviewURLsDefaultToHTTPS(t *testing.T) {
	yml := strings.Replace(servicesWithPreview, "  tls: false\n", "", 1)
	fg := &fakeGitops{badilYAML: yml}
	rec := postDeploy(t, newTestServer(t, fg), previewBody(), "test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(fg.capturedSummary, "(https://front-feature-pricing.127.0.0.1.nip.io/)") {
		t.Errorf("summary missing https front link, got:\n%s", fg.capturedSummary)
	}
}

func TestDeploy_MalformedPreviewFails(t *testing.T) {
	yml := strings.Replace(servicesWithPreview, "path: /docs", "path: docs", 1)
	fg := &fakeGitops{badilYAML: yml}
	rec := postDeploy(t, newTestServer(t, fg), previewBody(), "test-token")

	if rec.Code < http.StatusBadRequest {
		t.Fatalf("status = %d, want an error (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "preview.expose.backend-1.path") {
		t.Errorf("body must name the offending key, got: %s", rec.Body.String())
	}
	if fg.capturedSlug != "" || fg.fanOutBodyCalled {
		t.Error("a malformed preview block must fail before any write or comment")
	}
}

// servicesWithCap is a catalog capped at two previews.
const servicesWithCap = `services:
  backend-1:
    repo: https://github.com/acme/backend-1.git
preview:
  maxPreviews: 2
`

func TestDeploy_PreviewLimitRefusesNewSlug(t *testing.T) {
	fg := &fakeGitops{badilYAML: servicesWithCap, previews: capPreviews("checkout", "search")}
	rec := postDeploy(t, newTestServer(t, fg), previewBody(), "test-token")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), `{"error":"preview limit (2) reached"}`+"\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if fg.capturedSlug != "" || fg.fanOutBodyCalled {
		t.Error("a refused deploy must not write nor comment")
	}
}

// capPreviews returns a preview, pinned by acme/backend-1#42, for each of
// slugs.
func capPreviews(slugs ...string) map[string]*preview.Values {
	out := map[string]*preview.Values{}
	for _, slug := range slugs {
		out[slug] = &preview.Values{Services: []preview.ServiceEntry{pinnedBy42()}}
	}
	return out
}

func TestDeploy_PreviewLimitDeploys(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		previews map[string]*preview.Values
	}{
		{
			name:     "existing slug at the cap",
			yaml:     servicesWithCap,
			previews: capPreviews("checkout", "feature-pricing"),
		},
		{
			name:     "new slug below the cap",
			yaml:     strings.Replace(servicesWithCap, "maxPreviews: 2", "maxPreviews: 3", 1),
			previews: capPreviews("checkout", "search"),
		},
		{
			name:     "cap unset",
			yaml:     strings.Replace(servicesWithCap, "preview:\n  maxPreviews: 2\n", "", 1),
			previews: capPreviews("checkout", "search"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := &fakeGitops{badilYAML: tc.yaml, previews: tc.previews}
			rec := postDeploy(t, newTestServer(t, fg), previewBody(), "test-token")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if fg.capturedSlug != "feature-pricing" {
				t.Errorf("capturedSlug = %q, want feature-pricing", fg.capturedSlug)
			}
		})
	}
}

// TestDeploy_PushUnauthorized pushes to a remote that refuses the push with
// 403: the deploy answers 502.
func TestDeploy_PushUnauthorized(t *testing.T) {
	remote := storetest.NewRemote(t, map[string]string{"badil.yaml": servicesWithCap})
	srv := httptest.NewServer(remote.HTTPHandler(http.StatusForbidden))
	t.Cleanup(srv.Close)
	st, err := store.Open(
		context.Background(),
		store.CloneConfig{URL: srv.URL + "/acme/gitops.git"},
		storetest.Identity,
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fg := &fakeGitops{badilYAML: servicesWithCap}
	fg.open(t)
	fg.st = st
	a := app.New(app.Config{Store: fg, Forge: fg, FanOut: fakeFanOut(fg)})
	rec := postDeploy(t, NewServer(a, fg, nil, "test-token"), previewBody(), "test-token")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), `{"error":"write preview: push gitops repo: gitops: unauthorized`) {
		t.Errorf("body = %s, want the unauthorized write", rec.Body.String())
	}
	if fg.fanOutBodyCalled {
		t.Error("a refused push must not comment")
	}
}

// previewWriteResult is the write of backend-1 in a three-service preview.
func previewWriteResult() *store.WriteResult {
	return &store.WriteResult{
		Values: preview.Values{Services: []preview.ServiceEntry{
			{Name: "backend-1", Branch: "main", CommitSha: "abc1234567890"},
			{Name: "backend-2", Branch: "main"},
			{Name: "front", Branch: "main"},
		}},
	}
}

func TestDeploy_PreviewLimitReadErrors(t *testing.T) {
	cases := []struct {
		name   string
		fg     *fakeGitops
		status int
		op     string
	}{
		{
			name:   "read preview",
			fg:     &fakeGitops{previewErr: errors.New("read boom")},
			status: http.StatusInternalServerError,
			op:     "read preview: read boom",
		},
		{
			name:   "list previews",
			fg:     &fakeGitops{listErr: errors.Join(lifecycle.ErrUnauthorized, errors.New("list boom"))},
			status: http.StatusBadGateway,
			op:     "list previews: ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fg.badilYAML = servicesWithCap
			tc.fg.writeResult = previewWriteResult()
			rec := postDeploy(t, newTestServer(t, tc.fg), previewBody(), "test-token")

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.status, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.op) {
				t.Errorf("body = %s, want it to contain %q", rec.Body.String(), tc.op)
			}
			if tc.fg.capturedSlug != "" || tc.fg.fanOutBodyCalled {
				t.Error("a failed check must not write nor comment")
			}
		})
	}
}

// TestWithMetrics_NilRecordsNothing checks a server given an untyped nil or a
// nil *metrics.Metrics deploys a new preview, which lifecycle records through
// the server's recorder, without recording or panicking.
func TestWithMetrics_NilRecordsNothing(t *testing.T) {
	for name, m := range map[string]interface {
		Recorder
		app.Metrics
	}{
		"untyped nil":          nil,
		"nil *metrics.Metrics": (*metrics.Metrics)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			fg := &fakeGitops{catalog: []string{"backend-1"}}
			fg.open(t)
			a := app.New(app.Config{Store: fg, Forge: fg, Metrics: m, FanOut: fakeFanOut(fg)})
			srv := NewServer(a, fg, nil, "test-token", WithMetrics(m))
			if rec := postDeploy(t, srv, validBody(), "test-token"); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}
