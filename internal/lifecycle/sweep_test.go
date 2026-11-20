package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

const ttl = 168 * time.Hour

// now is the sweep time: 2026-06-10, so 2026-05-26 is past a 168h TTL and
// 2026-06-09 is not.
var now = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // test fixture

// sweepBadil is the badil.yaml of the sweep tests.
const sweepBadil = `services:
  backend-1:
    repo: https://github.com/acme/backend-1.git
  backend-2:
    repo: https://github.com/acme/backend-2.git
  worker:
    repo: https://github.com/acme/backend-1.git
  front:
    repo: https://github.com/acme/front.git
`

// fanOut is one comment posted on the open PRs of a preview.
type fanOut struct {
	vals preview.PreviewValues
	body string
}

// fakeAPI is a SweepAPI over a gitops repo of a real store, made on the
// first api call. The repo holds sweepBadil (with preview.argocdURL set to
// argocdURL when it is), or no badil.yaml when noCatalog is set, the files of
// previews (projected on the catalog) and raw. ListPreviews fails with
// listErr when set. PR states, labels and head refs are keyed by
// "owner/repo#n". Each push first runs beforePush when set, whose error fails
// the push. Metrics records the outcomes; nil records nothing.
type fakeAPI struct {
	*metrics.Metrics

	noCatalog  bool
	argocdURL  string
	previews   map[string]*preview.PreviewValues
	raw        map[string]string
	listErr    error
	prOpen     map[string]bool
	prErr      map[string]error
	prLabels   map[string][]string
	prHeadRef  map[string]string
	fanErr     error
	beforePush func(*storetest.Remote) error

	g       *gitops
	prReads []string
	upserts []string
	bodies  []string
	fanOuts []fanOut
}

func (f *fakeAPI) ListPreviews(ctx context.Context) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.g.st.ListPreviews(ctx)
}

func (f *fakeAPI) GetPreview(ctx context.Context, slug string) (*preview.PreviewValues, string, error) {
	return f.g.st.GetPreview(ctx, slug)
}

func (f *fakeAPI) Apply(ctx context.Context, op store.Op) (store.Result, error) {
	return f.g.st.Apply(ctx, op)
}

func (f *fakeAPI) GetPullRequest(_ context.Context, owner, repo string, number int) (*forge.PullRequest, error) {
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	f.prReads = append(f.prReads, key)
	if err := f.prErr[key]; err != nil {
		return nil, err
	}
	return &forge.PullRequest{Open: f.prOpen[key], Labels: f.prLabels[key], HeadRef: f.prHeadRef[key]}, nil
}

func (f *fakeAPI) UpsertComment(_ context.Context, owner, repo string, prNumber int, body string) error {
	f.upserts = append(f.upserts, fmt.Sprintf("%s/%s#%d", owner, repo, prNumber))
	f.bodies = append(f.bodies, body)
	return nil
}

func (f *fakeAPI) ListChangedFiles(context.Context, string, string, int) ([]forge.ChangedFile, error) {
	return nil, errors.New("unexpected ListChangedFiles")
}

func (f *fakeAPI) ListOpenPullRequests(context.Context, string, string) ([]forge.PullRequest, error) {
	return nil, errors.New("unexpected ListOpenPullRequests")
}

func (f *fakeAPI) ParseWebhook(*http.Request) (forge.Event, error) {
	return forge.Event{}, errors.New("unexpected ParseWebhook")
}

// api is the lifecycle.SweepAPI over f: its store, forge and recorder, with
// the fan-out recorded in fanOuts.
func (f *fakeAPI) api(t *testing.T) lifecycle.SweepAPI {
	t.Helper()
	if f.g == nil {
		badil := ""
		if !f.noCatalog {
			badil = sweepBadil
			if f.argocdURL != "" {
				badil += "preview:\n  argocdURL: " + f.argocdURL + "\n"
			}
		}
		f.g = newGitops(t, badil, f.previews, f.raw)
		if f.beforePush != nil {
			f.g.remote.OnPush(func() error { return f.beforePush(f.g.remote) })
		}
	}
	return lifecycle.SweepAPI{
		Store:    f,
		Forge:    f,
		Recorder: f,
		FanOut: func(_ context.Context, _ forge.Forge, vals preview.PreviewValues, body string) error {
			f.fanOuts = append(f.fanOuts, fanOut{vals: vals, body: body})
			return f.fanErr
		},
	}
}

// sweep runs one sweep over f at now with ttl.
func (f *fakeAPI) sweep(t *testing.T, ttl time.Duration) error {
	t.Helper()
	return lifecycle.Sweep(context.Background(), f.api(t), now, ttl)
}

// assertOneCommit fails t unless f pushed exactly one commit, with subject
// and body, and returns it.
func (f *fakeAPI) assertOneCommit(t *testing.T, subject string, body ...string) storetest.Commit {
	t.Helper()
	return f.g.assertOneCommit(t, subject, body...)
}

// pin is an entry pinned by prURL and last deployed at updatedAt.
func pin(name, prURL, updatedAt string) preview.ServiceEntry {
	return preview.ServiceEntry{
		Name:      name,
		CommitSha: "abc123",
		Metadata:  &preview.Metadata{PRURL: prURL, UpdatedAt: updatedAt},
	}
}

func values(entries ...preview.ServiceEntry) *preview.PreviewValues {
	return &preview.PreviewValues{Services: entries}
}

const (
	pr42   = "https://github.com/acme/backend-1/pull/42"
	pr7    = "https://github.com/acme/backend-2/pull/7"
	recent = "2026-06-09T10:00:00Z"
	stale  = "2026-05-26T10:00:00Z"
)

func TestSweep_NoPinsDeletes(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{"feature-x": values(preview.ServiceEntry{Name: "backend-1"})},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := f.assertOneCommit(t, "chore(preview): sweep: delete 1, unpin 0, sync 0", "feature-x: delete")
	if len(f.fanOuts) != 0 || len(f.upserts) != 0 || len(f.prReads) != 0 {
		t.Errorf("rule 1 must not comment or read PRs: fanOuts=%d upserts=%v prReads=%v",
			len(f.fanOuts), f.upserts, f.prReads)
	}
	logs := capture.find(slog.LevelInfo, "preview deleted")
	if len(logs) != 1 || logs[0]["slug"].String() != "feature-x" || logs[0]["reason"].String() != "no pins" ||
		logs[0]["gitops_commit_url"].String() != commitURL(c) {
		t.Errorf("preview deleted logs = %v", logs)
	}
	if _, ok := logs[0]["last_deploy"]; ok {
		t.Error("a no-pins delete must not carry last_deploy")
	}
}

func TestSweep_ExpiredDeletesAndComments(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{"feature-x": values(
			pin("backend-1", pr42, stale),
			preview.ServiceEntry{Name: "front"},
		)},
		// The PR is closed: rule 2 still wins over rule 3.
		prOpen: map[string]bool{},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := f.assertOneCommit(t, "chore(preview): sweep: delete 1, unpin 0, sync 0", "feature-x: delete")
	if logs := capture.find(slog.LevelInfo, "preview teardown"); len(logs) != 0 || len(f.upserts) != 0 {
		t.Errorf("an expired preview must not go through rule 3: teardowns=%v upserts=%v", logs, f.upserts)
	}
	wantBody := "## 🧹 Preview: `feature-x`\n\n" +
		"Preview deleted: no deploy for 168h0m0s.\n\n" +
		"---\n" + link(c)
	if len(f.fanOuts) != 1 || f.fanOuts[0].body != wantBody || len(f.fanOuts[0].vals.Services) == 0 {
		t.Fatalf("fanOuts = %+v, want one with body:\n%s", f.fanOuts, wantBody)
	}
	logs := capture.find(slog.LevelInfo, "preview deleted")
	if len(logs) != 1 || logs[0]["reason"].String() != "expired" ||
		logs[0]["last_deploy"].String() != stale || logs[0]["gitops_commit_url"].String() != commitURL(c) {
		t.Errorf("preview deleted logs = %v", logs)
	}
}

func TestSweep_ExpiryCommentFailure(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{"feature-x": values(pin("backend-1", pr42, stale))},
		fanErr:   errors.New("comment boom"),
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v (a comment failure is not a preview failure)", err)
	}
	logs := capture.find(slog.LevelWarn, "preview expiry comment failed")
	if len(logs) != 1 || logs[0]["slug"].String() != "feature-x" || logs[0]["err"].String() != "comment boom" {
		t.Errorf("expiry comment failed logs = %v", logs)
	}
}

func TestSweep_NeverExpires(t *testing.T) {
	cases := []struct {
		name string
		ttl  time.Duration
		vals *preview.PreviewValues
	}{
		{name: "ttl off", ttl: 0, vals: values(pin("backend-1", pr42, stale))},
		{name: "unparseable updated-at", ttl: ttl, vals: values(pin("backend-1", pr42, "2026-05-26"))},
		{name: "recent deploy", ttl: ttl, vals: values(pin("backend-1", pr42, stale), pin("backend-2", pr7, recent))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &fakeAPI{
				previews: map[string]*preview.PreviewValues{"feature-x": tc.vals},
				prOpen:   map[string]bool{"acme/backend-1#42": true, "acme/backend-2#7": true},
				// Both are open at their branch preview: rule 3 leaves them.
				prHeadRef: map[string]string{"acme/backend-1#42": "feature/x", "acme/backend-2#7": "feature/x"},
			}
			if err := f.sweep(t, tc.ttl); err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if got := f.g.subjects(); len(got) != 0 || len(f.fanOuts) != 0 || len(teardownsLogged(capture)) != 0 {
				t.Errorf("pushed=%q fanOuts=%d teardowns=%v, want nothing",
					got, len(f.fanOuts), teardownsLogged(capture))
			}
		})
	}
}

func TestSweep_ClosedPRTearsDown(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{"feature-x": values(
			pin("backend-1", pr42, recent),
			pin("backend-2", pr7, recent),
		)},
		prOpen:    map[string]bool{"acme/backend-2#7": true},
		prHeadRef: map[string]string{"acme/backend-2#7": "feature/x"},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if want := []string{"acme/backend-1#42", "acme/backend-2#7"}; !slices.Equal(f.prReads, want) {
		t.Errorf("prReads = %v, want %v", f.prReads, want)
	}
	want := []teardownCall{{slug: "feature-x", owner: "acme", repo: "backend-1", number: 42}}
	if got := teardownsLogged(capture); !slices.Equal(got, want) {
		t.Errorf("teardowns = %+v, want %+v", got, want)
	}
	c := f.assertOneCommit(t, "chore(preview): sweep: delete 0, unpin 1, sync 0", "feature-x: unpin backend-1")
	// The teardown path comments on the closed PR, exactly like a webhook close.
	if want := []string{"acme/backend-1#42"}; !slices.Equal(f.upserts, want) {
		t.Errorf("upserts = %v, want %v", f.upserts, want)
	}
	if len(f.bodies) != 1 || !strings.HasSuffix(f.bodies[0], link(c)) {
		t.Errorf("bodies = %q, want one linking %s", f.bodies, link(c))
	}
	if len(f.fanOuts) != 1 {
		t.Errorf("fanOuts = %d, want one summary of the kept preview", len(f.fanOuts))
	}
}

func TestSweep_PRStateUnknownKeepsPin(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "404", err: forge.ErrNotFound},
		{name: "500", err: errors.New("GET pulls/42: 500 boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &fakeAPI{
				previews: map[string]*preview.PreviewValues{"feature-x": values(pin("backend-1", pr42, recent))},
				prErr:    map[string]error{"acme/backend-1#42": tc.err},
			}
			if err := f.sweep(t, ttl); err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if got := f.g.subjects(); len(got) != 0 || len(teardownsLogged(capture)) != 0 ||
				len(f.fanOuts) != 0 || len(f.upserts) != 0 {
				t.Errorf("pushed=%q fanOuts=%d upserts=%v, want none", got, len(f.fanOuts), f.upserts)
			}
			logs := capture.find(slog.LevelWarn, "cleanup pr state unknown")
			if len(logs) != 1 || logs[0]["slug"].String() != "feature-x" ||
				logs[0]["repo"].String() != "acme/backend-1" ||
				logs[0]["pr_number"].Int64() != 42 || logs[0]["err"].String() != tc.err.Error() {
				t.Errorf("pr state unknown logs = %v", logs)
			}
		})
	}
}

func TestSweep_OnePRPinningTwoServicesCheckedOnce(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{
			"feature-x": values(
				pin("backend-1", pr42, recent),
				pin("worker", "https://github.com/ACME/Backend-1/pull/42/files", recent),
				pin("front", "not-a-pr-url", recent),
				preview.ServiceEntry{Name: "backend-2", CommitSha: "abc123"},
			),
			// The same PR in another preview is not read again.
			"other": values(pin("backend-1", pr42, recent)),
		},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if want := []string{"acme/backend-1#42"}; !slices.Equal(f.prReads, want) {
		t.Errorf("prReads = %v, want %v", f.prReads, want)
	}
	if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"feature-x", "other"}) {
		t.Errorf("teardowns = %v, want one from each preview", got)
	}
}

// TestSweep_RulesOnRetriedTree loses the first push to a deploy that lands
// in the deleted previews: the retry keeps them.
func TestSweep_RulesOnRetriedTree(t *testing.T) {
	cases := []struct {
		name  string
		file  *preview.PreviewValues
		fresh *preview.PreviewValues
	}{
		{
			name:  "rule 1: a new pin",
			file:  values(preview.ServiceEntry{Name: "backend-1"}),
			fresh: values(pin("backend-1", pr42, recent)),
		},
		{
			name:  "rule 2: a newer updated-at",
			file:  values(pin("backend-1", pr42, stale)),
			fresh: values(pin("backend-1", pr42, recent)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &fakeAPI{
				previews:  map[string]*preview.PreviewValues{"feature-x": tc.file},
				prOpen:    map[string]bool{"acme/backend-1#42": true},
				prHeadRef: map[string]string{"acme/backend-1#42": "feature/x"},
			}
			raced := false
			f.beforePush = func(r *storetest.Remote) error {
				if !raced {
					raced = true
					r.Set("previews/feature-x/values.yaml", marshal(t,
						storetest.Project("feature-x", *tc.fresh, sweepCatalog(t))))
				}
				return nil
			}
			if err := f.sweep(t, ttl); err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if got := f.g.subjects(); !slices.Equal(got, []string{"test"}) || len(f.fanOuts) != 0 {
				t.Errorf("pushed=%q fanOuts=%d, want only the commit the push lost to", got, len(f.fanOuts))
			}
			if logs := capture.find(slog.LevelInfo, "preview deleted"); len(logs) != 0 {
				t.Errorf("preview deleted logged: %v", logs)
			}
			if f.g.file("feature-x") == nil {
				t.Error("feature-x deleted, want it kept")
			}
		})
	}
}

// sweepCatalog parses sweepBadil.
func sweepCatalog(t *testing.T) *store.ServiceCatalog {
	t.Helper()
	catalog, err := store.ParseCatalog([]byte(sweepBadil))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	return catalog
}

// TestSweep_PRPinnedAfterThePreRead loses the first push to a deploy of a
// PR whose state the sweep did not read: the retry leaves its pins alone and
// reads nothing more from the forge.
func TestSweep_PRPinnedAfterThePreRead(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{
			"feature-x": values(pin("backend-1", pr42, recent)),
			"unpinned":  values(preview.ServiceEntry{Name: "backend-1"}),
		},
		prOpen: map[string]bool{},
	}
	raced := false
	f.beforePush = func(r *storetest.Remote) error {
		if !raced {
			raced = true
			r.Set("previews/feature-x/values.yaml", marshal(t, storetest.Project("feature-x",
				*values(pin("backend-1", pr42, recent), pin("backend-2", pr7, recent)), sweepCatalog(t))))
		}
		return nil
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if want := []string{"acme/backend-1#42"}; !slices.Equal(f.prReads, want) {
		t.Errorf("prReads = %v, want only the pre-read %v", f.prReads, want)
	}
	pushed := f.g.pushed()
	if len(pushed) != 2 || pushed[1].Subject() != "chore(preview): sweep: delete 1, unpin 1, sync 0" ||
		!slices.Equal(pushed[1].Body(), []string{"feature-x: unpin backend-1", "unpinned: delete"}) {
		t.Fatalf("pushed = %q, want the race then the sweep", f.g.subjects())
	}
	if pins := f.g.pins("feature-x"); pins["backend-1"] != "" || pins["backend-2"] != "abc123" {
		t.Errorf("feature-x pins = %v, want backend-1 torn down, backend-2 kept", pins)
	}
	want := []teardownCall{{slug: "feature-x", owner: "acme", repo: "backend-1", number: 42}}
	if got := teardownsLogged(capture); !slices.Equal(got, want) {
		t.Errorf("teardowns = %+v, want %+v", got, want)
	}
}

func TestSweep_FailuresCountedAndSweepContinues(t *testing.T) {
	capture := captureLogs(t)
	unpinned := values(preview.ServiceEntry{Name: "backend-1"})
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{"a": unpinned, "c": unpinned, "d": unpinned},
		raw: map[string]string{
			"previews/b/values.yaml":  "services: [\n",
			"previews/gone/README.md": "no values file\n",
		},
	}
	err := f.sweep(t, 0)
	if err == nil || err.Error() != "cleanup: 1 preview(s) failed" {
		t.Fatalf("err = %v, want cleanup: 1 preview(s) failed", err)
	}
	f.assertOneCommit(t, "chore(preview): sweep: delete 3, unpin 0, sync 0", "a: delete", "c: delete", "d: delete")
	failed := capture.find(slog.LevelError, "cleanup preview failed")
	if len(failed) != 1 || failed[0]["slug"].String() != "b" ||
		!strings.HasPrefix(failed[0]["err"].String(), "parse previews/b/values.yaml") {
		t.Errorf("cleanup preview failed logs = %v", failed)
	}
	started := capture.find(slog.LevelInfo, "cleanup started")
	if len(started) != 1 || started[0]["previews"].Int64() != 5 || started[0]["ttl"].String() != "0s" {
		t.Errorf("cleanup started logs = %v", started)
	}
	finished := capture.find(slog.LevelInfo, "cleanup finished")
	if len(finished) != 1 || finished[0]["previews"].Int64() != 5 || finished[0]["failed"].Int64() != 1 {
		t.Errorf("cleanup finished logs = %v", finished)
	}
}

// TestSweep_OneCommit checks a sweep that deletes 1, unpins 2 and syncs 3
// previews makes one commit.
func TestSweep_OneCommit(t *testing.T) {
	captureLogs(t)
	// An entry whose pr-url names no PR is left alone by rule 3.
	kept := pin("backend-2", "not-a-pr-url", recent)
	unsynced := marshal(t, *values(kept))
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{
			"a-unpinned": values(preview.ServiceEntry{Name: "backend-1"}),
			"b-closed":   values(pin("backend-1", pr42, recent), kept),
			"c-closed":   values(pin("backend-1", pr42, recent), kept),
		},
		raw: map[string]string{
			"previews/d-sync/values.yaml": unsynced,
			"previews/e-sync/values.yaml": unsynced,
			"previews/f-sync/values.yaml": unsynced,
		},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	f.assertOneCommit(t, "chore(preview): sweep: delete 1, unpin 2, sync 3",
		"a-unpinned: delete", "b-closed: unpin backend-1", "c-closed: unpin backend-1",
		"d-sync: sync", "e-sync: sync", "f-sync: sync")
}

func TestSweep_PushFailure(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		Metrics: metrics.New(),
		previews: map[string]*preview.PreviewValues{
			"a":         values(preview.ServiceEntry{Name: "backend-1"}),
			"feature-x": values(pin("backend-1", pr42, recent), pin("backend-2", pr7, recent)),
		},
		prOpen:     map[string]bool{"acme/backend-2#7": true},
		prHeadRef:  map[string]string{"acme/backend-2#7": "feature/x"},
		beforePush: func(*storetest.Remote) error { return errors.New("push boom") },
	}
	err := f.sweep(t, ttl)
	if err == nil || !strings.Contains(err.Error(), "push boom") {
		t.Fatalf("err = %v, want the push error", err)
	}
	if got := f.g.subjects(); len(got) != 0 || len(f.upserts) != 0 || len(f.fanOuts) != 0 {
		t.Errorf("pushed=%q upserts=%v fanOuts=%d, want nothing", got, f.upserts, len(f.fanOuts))
	}
	if logs := capture.find(slog.LevelInfo, "preview deleted"); len(logs) != 0 {
		t.Errorf("preview deleted logged: %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_teardowns_total{result="error",trigger="sweep"}`: 1,
	}, "badil_teardowns_total", "badil_sweep_previews_deleted_total")
}

func TestSweep_ListError(t *testing.T) {
	captureLogs(t)
	listErr := errors.New("list boom")
	f := &fakeAPI{listErr: listErr}
	if err := f.sweep(t, ttl); !errors.Is(err, listErr) {
		t.Fatalf("err = %v, want %v", err, listErr)
	}
}

// TestSweep_OneSummaryPerPreview checks that a preview two rule-3 teardowns
// unpin and keep has its summary refreshed once, with the values committed:
// neither torn-down PR is still pinned in it.
func TestSweep_OneSummaryPerPreview(t *testing.T) {
	captureLogs(t)
	const front9 = "https://github.com/acme/front/pull/9"
	f := &fakeAPI{
		previews: map[string]*preview.PreviewValues{"feature-x": values(
			pin("backend-1", pr42, recent),
			pin("backend-2", pr7, recent),
			pin("front", front9, recent),
		)},
		prOpen:    map[string]bool{"acme/backend-2#7": true, "acme/front#9": true},
		prLabels:  map[string][]string{"acme/backend-2#7": {"preview:other"}},
		prHeadRef: map[string]string{"acme/backend-2#7": "feature/y", "acme/front#9": "feature/x"},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := f.assertOneCommit(t, "chore(preview): sweep: delete 0, unpin 1, sync 0",
		"feature-x: unpin backend-1", "feature-x: unpin backend-2")
	if len(f.fanOuts) != 1 || !strings.HasSuffix(f.fanOuts[0].body, link(c)) {
		t.Fatalf("fanOuts = %d, want one linking %s", len(f.fanOuts), link(c))
	}
	var pinned []string
	for _, svc := range f.fanOuts[0].vals.Services {
		if svc.CommitSha != "" {
			pinned = append(pinned, svc.Name)
		}
	}
	if !slices.Equal(pinned, []string{"front"}) {
		t.Errorf("pinned in the summary = %v, want [front]", pinned)
	}
}
