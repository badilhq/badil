package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

// quiet is how long a test waits to conclude that nothing happens.
const quiet = 100 * time.Millisecond

// syncFake is a concurrency-safe lifecycle.SyncAPI over a gitops repo of a
// real store holding sweepBadil and the previews of files: a preview in
// changed differs from its projection on the catalog, one in broken can't
// be parsed, the others match it. FetchServiceCatalogSHA returns the
// catalog with sha, or catalogErr. Apply fails with applyErr when set and
// counts its calls; when gate is set, it first sends on entered, then waits
// until gate is closed.
type syncFake struct {
	*metrics.Metrics

	mu         sync.Mutex
	sha        string
	catalogErr error
	applyErr   error
	gate       chan struct{}
	entered    chan struct{}

	g            *gitops
	catalogReads int
	applies      int
}

// newSyncFake returns a syncFake whose previews are unchanged, changed and
// broken.
func newSyncFake(t *testing.T, unchanged, changed, broken []string) *syncFake {
	t.Helper()
	previews := map[string]*preview.Values{}
	for _, slug := range unchanged {
		previews[slug] = values(pin("backend-1", pr42, recent))
	}
	raw := map[string]string{}
	for _, slug := range changed {
		raw["previews/"+slug+"/values.yaml"] = marshal(t, *values(pin("backend-1", pr42, recent)))
	}
	for _, slug := range broken {
		raw["previews/"+slug+"/values.yaml"] = "services: [\n"
	}
	return &syncFake{Metrics: metrics.New(), sha: "sha-1", g: newGitops(t, sweepBadil, previews, raw)}
}

func (f *syncFake) setSHA(sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sha = sha
}

func (f *syncFake) setApplyErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyErr = err
}

func (f *syncFake) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.catalogReads
}

func (f *syncFake) applied() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applies
}

func (f *syncFake) FetchServiceCatalogSHA(ctx context.Context) (*store.ServiceCatalog, string, error) {
	f.mu.Lock()
	f.catalogReads++
	sha, catalogErr := f.sha, f.catalogErr
	f.mu.Unlock()
	if catalogErr != nil {
		return nil, sha, catalogErr
	}
	catalog, err := f.g.st.FetchServiceCatalog(ctx)
	return catalog, sha, err
}

func (f *syncFake) Apply(ctx context.Context, op store.Op) (store.Result, error) {
	if f.gate != nil {
		f.entered <- struct{}{}
		<-f.gate
	}
	f.mu.Lock()
	f.applies++
	applyErr := f.applyErr
	f.mu.Unlock()
	if applyErr != nil {
		return store.Result{}, applyErr
	}
	return f.g.st.Apply(ctx, op)
}

// api is the lifecycle.SyncAPI over f: its store and recorder, with a
// fan-out that posts nothing. Its forge is only the default GitHub URL rules
// (storetest.GitHubForge): it has no client.
func (f *syncFake) api() lifecycle.SyncAPI {
	return lifecycle.SyncAPI{
		Store:    f,
		Forge:    storetest.GitHubForge,
		Recorder: f,
		FanOut: func(context.Context, forge.Forge, preview.Values, string) error {
			return nil
		},
	}
}

// gated makes f's Apply wait for the returned release.
func (f *syncFake) gated() func() {
	f.gate = make(chan struct{})
	f.entered = make(chan struct{}, 64)
	var once sync.Once
	return func() { once.Do(func() { close(f.gate) }) }
}

// waitEntered waits for an Apply to enter f's gate.
func waitEntered(t *testing.T, f *syncFake) {
	t.Helper()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("no sync entered the gate")
	}
}

// assertReport fails t unless report is want.
func assertReport(t *testing.T, report, want lifecycle.SyncReport) {
	t.Helper()
	if !slices.Equal(report.Synced, want.Synced) || report.Unchanged != want.Unchanged ||
		!slices.Equal(report.Failed, want.Failed) {
		t.Errorf("report = %+v, want %+v", report, want)
	}
}

func TestSyncer_SyncAll(t *testing.T) {
	captureLogs(t)
	f := newSyncFake(t, []string{"b"}, []string{"a"}, []string{"c"})
	report, err := lifecycle.NewSyncer(f.api(), time.Minute).Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	assertReport(t, report, lifecycle.SyncReport{Synced: []string{"a"}, Unchanged: 1, Failed: []string{"c"}})
	pushed := f.g.pushed()
	if len(pushed) != 1 || pushed[0].Subject() != "chore(preview): sync a with badil.yaml" ||
		!slices.Equal(pushed[0].Body(), []string{"a: sync"}) {
		t.Errorf("pushed = %q, want one sync of a", f.g.subjects())
	}
	assertMetrics(t, f.Metrics, map[string]float64{"badil_previews_synced_total": 1}, "badil_previews_synced_total")
}

func TestSyncer_SyncAllSeveralPreviews(t *testing.T) {
	captureLogs(t)
	f := newSyncFake(t, nil, []string{"a", "b"}, nil)
	report, err := lifecycle.NewSyncer(f.api(), time.Minute).Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	assertReport(t, report, lifecycle.SyncReport{Synced: []string{"a", "b"}})
	pushed := f.g.pushed()
	if len(pushed) != 1 || pushed[0].Subject() != "chore(preview): sync 2 previews with badil.yaml" ||
		!slices.Equal(pushed[0].Body(), []string{"a: sync", "b: sync"}) {
		t.Errorf("pushed = %q, want one sync of both", f.g.subjects())
	}
}

func TestSyncer_SyncOne(t *testing.T) {
	cases := []struct {
		name       string
		slug       string
		wantErr    error
		wantReport lifecycle.SyncReport
		pushed     int
	}{
		{name: "rewritten", slug: "b", wantReport: lifecycle.SyncReport{Synced: []string{"b"}}, pushed: 1},
		{name: "unknown", slug: "zz", wantErr: lifecycle.ErrPreviewNotFound},
		{name: "unreadable", slug: "c", wantReport: lifecycle.SyncReport{Failed: []string{"c"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := newSyncFake(t, []string{"a"}, []string{"b"}, []string{"c"})
			report, err := lifecycle.NewSyncer(f.api(), time.Minute).Sync(context.Background(), tc.slug)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			assertReport(t, report, tc.wantReport)
			if got := len(f.g.pushed()); got != tc.pushed {
				t.Errorf("pushed = %q, want %d commits", f.g.subjects(), tc.pushed)
			}
		})
	}
}

func TestSyncer_SyncErrors(t *testing.T) {
	cases := []struct {
		name       string
		catalogErr error
		applyErr   error
		wantConfig bool
		wantApply  int
	}{
		{name: "badil.yaml unavailable", catalogErr: errors.New("parse boom"), wantConfig: true},
		{name: "operation failure", applyErr: store.ErrUnauthorized, wantApply: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := newSyncFake(t, nil, []string{"a"}, nil)
			f.catalogErr, f.applyErr = tc.catalogErr, tc.applyErr
			_, err := lifecycle.NewSyncer(f.api(), time.Minute).Sync(context.Background(), "")
			if err == nil || errors.Is(err, lifecycle.ErrConfigUnavailable) != tc.wantConfig {
				t.Fatalf("err = %v, want ErrConfigUnavailable %v", err, tc.wantConfig)
			}
			if tc.applyErr != nil && !errors.Is(err, tc.applyErr) {
				t.Errorf("err = %v, want %v", err, tc.applyErr)
			}
			if got := f.applied(); got != tc.wantApply {
				t.Errorf("applies = %d, want %d", got, tc.wantApply)
			}
		})
	}
}

func TestSyncer_PushSyncsOnlyANewSHA(t *testing.T) {
	captureLogs(t)
	f := newSyncFake(t, nil, []string{"a"}, []string{"b"})
	s := lifecycle.NewSyncer(f.api(), time.Minute)
	ctx := context.Background()

	s.Push(ctx) // sha-1, first push since start: syncs, b fails.
	s.Push(ctx) // sha-1 again (e.g. badil's own commit): nothing.
	if got := f.applied(); got != 1 {
		t.Fatalf("runs = %d, want one", got)
	}
	f.setSHA("sha-2")
	s.Push(ctx)
	if got := f.applied(); got != 2 {
		t.Errorf("runs = %d, want a second run for sha-2", got)
	}
}

func TestSyncer_PushConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		sha  string
		// wantFailures is the failed runs of two pushes: the second push of a
		// recorded SHA stops before any run.
		wantFailures int
	}{
		{name: "unparsable content is recorded", sha: "sha-bad", wantFailures: 1},
		{name: "unreadable file is retried", sha: "", wantFailures: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			f := newSyncFake(t, nil, []string{"a"}, nil)
			f.sha, f.catalogErr = tc.sha, errors.New("catalog boom")
			s := lifecycle.NewSyncer(f.api(), time.Minute)
			s.Push(context.Background())
			s.Push(context.Background())
			if f.reads() != 2 {
				t.Errorf("catalog reads = %d, want 2", f.reads())
			}
			if got := f.applied(); got != 0 {
				t.Errorf("runs = %d, want none", got)
			}
			if got := len(logs.find(slog.LevelError, "config sync failed")); got != tc.wantFailures {
				t.Errorf("failed runs = %d, want %d", got, tc.wantFailures)
			}
		})
	}
}

func TestSyncer_PushOperationErrorRecordsNoSHA(t *testing.T) {
	logs := captureLogs(t)
	f := newSyncFake(t, nil, []string{"a"}, nil)
	f.applyErr = errors.New("push boom")
	s := lifecycle.NewSyncer(f.api(), time.Minute)
	ctx := context.Background()

	s.Push(ctx) // sha-1, the operation fails: nothing synced, no SHA kept.
	f.setApplyErr(nil)
	s.Push(ctx) // sha-1 again: retried, syncs a.
	s.Push(ctx) // sha-1 now recorded: nothing.
	if got := f.applied(); got != 2 {
		t.Errorf("runs = %d, want the failed run and its retry", got)
	}
	if got := f.g.subjects(); !slices.Equal(got, []string{"chore(preview): sync a with badil.yaml"}) {
		t.Errorf("pushed = %q, want one sync of a", got)
	}
	failed := logs.find(slog.LevelError, "config sync failed")
	if len(failed) != 1 || failed[0]["op"].String() != "sync previews" || failed[0]["err"].String() != "push boom" {
		t.Errorf("config sync failed logs = %v", failed)
	}
}

func TestSyncer_PushDoesNotTouchAPISHA(t *testing.T) {
	captureLogs(t)
	f := newSyncFake(t, nil, []string{"a"}, nil)
	s := lifecycle.NewSyncer(f.api(), time.Minute)
	if _, err := s.Sync(context.Background(), ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	s.Push(context.Background()) // The API run recorded no SHA: the push syncs.
	if got := f.applied(); got != 2 {
		t.Errorf("runs = %d, want a run per trigger", got)
	}
}

func TestSyncer_OneRunAtATime(t *testing.T) {
	captureLogs(t)
	f := newSyncFake(t, nil, []string{"a"}, nil)
	release := f.gated()
	defer release()
	s := lifecycle.NewSyncer(f.api(), time.Minute)

	var wg sync.WaitGroup
	wg.Go(func() { s.Push(context.Background()) })
	waitEntered(t, f)
	reports := make(chan lifecycle.SyncReport, 3)
	for _, slug := range []string{"", "", "a"} {
		wg.Go(func() {
			report, err := s.Sync(context.Background(), slug)
			if err != nil {
				t.Errorf("Sync %q: %v", slug, err)
			}
			reports <- report
		})
	}
	time.Sleep(quiet)
	if got := f.reads(); got != 1 {
		t.Fatalf("catalog reads during the first run = %d, want 1 (the others wait for the guard)", got)
	}
	release()
	wg.Wait()
	close(reports)
	n := 0
	for r := range reports {
		n++
		// The push run synced a: each later run finds it unchanged.
		assertReport(t, r, lifecycle.SyncReport{Unchanged: 1})
	}
	if n != 3 || f.reads() != 4 {
		t.Errorf("answered API calls, catalog reads = %d, %d; want 3, 4: each caller runs and gets its own report",
			n, f.reads())
	}
}

func TestSyncer_SyncWaitStopsWithContext(t *testing.T) {
	captureLogs(t)
	f := newSyncFake(t, nil, []string{"a"}, nil)
	release := f.gated()
	defer release()
	s := lifecycle.NewSyncer(f.api(), time.Minute)

	var wg sync.WaitGroup
	defer wg.Wait()
	defer release()
	wg.Go(func() { s.Push(context.Background()) })
	waitEntered(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), quiet)
	defer cancel()
	if _, err := s.Sync(ctx, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's error", err)
	}
}
