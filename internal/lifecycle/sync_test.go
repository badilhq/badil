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
)

// quiet is how long a test waits to conclude that nothing happens.
const quiet = 100 * time.Millisecond

// syncFake is a concurrency-safe lifecycle.SyncAPI. FetchServiceCatalogSHA
// returns sha with catalogErr; previews is what ListPreviews returns and the
// set of slugs SyncPreviewWithRetry finds. SyncPreviewWithRetry rewrites the
// slugs in changed and fails those in syncErr. When gate is set,
// SyncPreviewWithRetry sends its slug on entered, then waits until gate is
// closed.
type syncFake struct {
	*metrics.Metrics

	mu         sync.Mutex
	sha        string
	catalogErr error
	previews   []string
	listErr    error
	changed    map[string]bool
	syncErr    map[string]error
	gate       chan struct{}
	entered    chan string

	catalogReads int
	synced       []string
}

func newSyncFake(previews ...string) *syncFake {
	return &syncFake{Metrics: metrics.New(), sha: "sha-1", previews: previews}
}

func (f *syncFake) setSHA(sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sha = sha
}

func (f *syncFake) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.catalogReads
}

func (f *syncFake) syncs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.synced)
}

func (f *syncFake) FetchServiceCatalogSHA(context.Context) (*store.ServiceCatalog, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catalogReads++
	if f.catalogErr != nil {
		return nil, f.sha, f.catalogErr
	}
	return &store.ServiceCatalog{}, f.sha, nil
}

func (f *syncFake) ListPreviews(context.Context) ([]string, error) {
	return f.previews, f.listErr
}

func (f *syncFake) SyncPreviewWithRetry(
	_ context.Context,
	slug string,
	_ *store.ServiceCatalog,
) (*store.SyncResult, error) {
	if f.gate != nil {
		f.entered <- slug
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced = append(f.synced, slug)
	if err := f.syncErr[slug]; err != nil {
		return nil, err
	}
	if !slices.Contains(f.previews, slug) {
		return &store.SyncResult{NotFound: true}, nil
	}
	if f.changed[slug] {
		return &store.SyncResult{Changed: true, CommitURL: syncCommitURL}, nil
	}
	return &store.SyncResult{}, nil
}

func (f *syncFake) FanOutPRComments(context.Context, preview.PreviewValues, string) error {
	return nil
}

// api is the lifecycle.SyncAPI over f: its store and recorder, with its
// FanOutPRComments as the fan-out. It has no forge.
func (f *syncFake) api() lifecycle.SyncAPI {
	return lifecycle.SyncAPI{
		Store:    f,
		Recorder: f,
		FanOut: func(ctx context.Context, _ forge.Forge, vals preview.PreviewValues, body string) error {
			return f.FanOutPRComments(ctx, vals, body)
		},
	}
}

// gated makes f's syncs wait for the returned release.
func (f *syncFake) gated() func() {
	f.gate = make(chan struct{})
	f.entered = make(chan string, 64)
	var once sync.Once
	return func() { once.Do(func() { close(f.gate) }) }
}

// waitEntered waits for a sync to enter f's gate.
func waitEntered(t *testing.T, f *syncFake) {
	t.Helper()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("no sync entered the gate")
	}
}

func TestSyncer_SyncAll(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a", "b", "c")
	f.changed = map[string]bool{"a": true}
	f.syncErr = map[string]error{"c": errors.New("sync boom")}
	report, err := lifecycle.NewSyncer(f.api(), time.Minute).Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	want := lifecycle.SyncReport{Synced: []string{"a"}, Unchanged: 1, Failed: []string{"c"}}
	if !slices.Equal(report.Synced, want.Synced) || report.Unchanged != want.Unchanged ||
		!slices.Equal(report.Failed, want.Failed) {
		t.Errorf("report = %+v, want %+v", report, want)
	}
	if got := f.syncs(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("syncs = %v, want every preview", got)
	}
	assertMetrics(t, f.Metrics, map[string]float64{"badil_previews_synced_total": 1}, "badil_previews_synced_total")
}

func TestSyncer_SyncOne(t *testing.T) {
	cases := []struct {
		name       string
		slug       string
		syncErr    error
		wantErr    error
		wantReport lifecycle.SyncReport
		wantSyncs  []string
	}{
		{
			name: "rewritten", slug: "b", wantReport: lifecycle.SyncReport{Synced: []string{"b"}},
			wantSyncs: []string{"b"},
		},
		{name: "unknown", slug: "zz", wantErr: lifecycle.ErrPreviewNotFound, wantSyncs: []string{"zz"}},
		{
			name: "unreadable", slug: "b", syncErr: errors.New("read boom"),
			wantReport: lifecycle.SyncReport{Failed: []string{"b"}}, wantSyncs: []string{"b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := newSyncFake("a", "b")
			f.changed = map[string]bool{"b": true}
			f.syncErr = map[string]error{"b": tc.syncErr}
			report, err := lifecycle.NewSyncer(f.api(), time.Minute).Sync(context.Background(), tc.slug)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !slices.Equal(report.Synced, tc.wantReport.Synced) ||
				!slices.Equal(report.Failed, tc.wantReport.Failed) ||
				report.Unchanged != tc.wantReport.Unchanged {
				t.Errorf("report = %+v, want %+v", report, tc.wantReport)
			}
			if got := f.syncs(); !slices.Equal(got, tc.wantSyncs) {
				t.Errorf("syncs = %v, want %v", got, tc.wantSyncs)
			}
		})
	}
}

func TestSyncer_SyncErrors(t *testing.T) {
	cases := []struct {
		name       string
		catalogErr error
		listErr    error
		wantConfig bool
	}{
		{name: "badil.yaml unavailable", catalogErr: errors.New("parse boom"), wantConfig: true},
		{name: "list failure", listErr: errors.New("list boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := newSyncFake("a")
			f.catalogErr, f.listErr = tc.catalogErr, tc.listErr
			_, err := lifecycle.NewSyncer(f.api(), time.Minute).Sync(context.Background(), "")
			if err == nil || errors.Is(err, lifecycle.ErrConfigUnavailable) != tc.wantConfig {
				t.Fatalf("err = %v, want ErrConfigUnavailable %v", err, tc.wantConfig)
			}
			if got := f.syncs(); len(got) != 0 {
				t.Errorf("syncs = %v, want none", got)
			}
		})
	}
}

func TestSyncer_PushSyncsOnlyANewSHA(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a", "b")
	f.syncErr = map[string]error{"b": errors.New("sync boom")}
	s := lifecycle.NewSyncer(f.api(), time.Minute)
	ctx := context.Background()

	s.Push(ctx) // sha-1, first push since start: syncs, b fails.
	s.Push(ctx) // sha-1 again (e.g. badil's own commit): nothing.
	if got := f.syncs(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("syncs = %v, want one run over a and b", got)
	}
	f.setSHA("sha-2")
	s.Push(ctx)
	if got := f.syncs(); !slices.Equal(got, []string{"a", "b", "a", "b"}) {
		t.Errorf("syncs = %v, want a second run for sha-2", got)
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
			f := newSyncFake("a")
			f.sha, f.catalogErr = tc.sha, errors.New("catalog boom")
			s := lifecycle.NewSyncer(f.api(), time.Minute)
			s.Push(context.Background())
			s.Push(context.Background())
			if f.reads() != 2 {
				t.Errorf("catalog reads = %d, want 2", f.reads())
			}
			if got := f.syncs(); len(got) != 0 {
				t.Errorf("syncs = %v, want none", got)
			}
			if got := len(logs.find(slog.LevelError, "config sync failed")); got != tc.wantFailures {
				t.Errorf("failed runs = %d, want %d", got, tc.wantFailures)
			}
		})
	}
}

func TestSyncer_PushListErrorRecordsNoSHA(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
	f.listErr = errors.New("list boom")
	s := lifecycle.NewSyncer(f.api(), time.Minute)
	ctx := context.Background()

	s.Push(ctx) // sha-1, previews can't be listed: nothing synced, no SHA kept.
	f.listErr = nil
	s.Push(ctx) // sha-1 again: retried, syncs a.
	s.Push(ctx) // sha-1 now recorded: nothing.
	if got := f.syncs(); !slices.Equal(got, []string{"a"}) {
		t.Errorf("syncs = %v, want one run over a after the list failure", got)
	}
}

func TestSyncer_PushDoesNotTouchAPISHA(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
	s := lifecycle.NewSyncer(f.api(), time.Minute)
	if _, err := s.Sync(context.Background(), ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	s.Push(context.Background()) // The API run recorded no SHA: the push syncs.
	if got := f.syncs(); !slices.Equal(got, []string{"a", "a"}) {
		t.Errorf("syncs = %v, want a run per trigger", got)
	}
}

func TestSyncer_OneRunAtATime(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
	f.changed = map[string]bool{"a": true}
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
		if !slices.Equal(r.Synced, []string{"a"}) {
			t.Errorf("report = %+v, want a synced", r)
		}
	}
	if n != 3 || f.reads() != 4 {
		t.Errorf("answered API calls, catalog reads = %d, %d; want 3, 4: each caller runs and gets its own report",
			n, f.reads())
	}
}

func TestSyncer_SyncWaitStopsWithContext(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
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
