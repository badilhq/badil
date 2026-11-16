package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// quiet is how long a test waits to conclude that nothing happens.
const quiet = 100 * time.Millisecond

// syncFake is a concurrency-safe lifecycle.SyncAPI. FetchServiceCatalogSHA
// returns sha with catalogErr; previews is what ListPreviews returns and the
// set of slugs GetPreview finds. SyncPreviewWithRetry rewrites the slugs in
// changed and fails those in syncErr. When gate is set, SyncPreviewWithRetry
// sends its slug on entered, then waits until gate is closed.
type syncFake struct {
	*metrics.Metrics

	mu         sync.Mutex
	sha        string
	catalogErr error
	previews   []string
	listErr    error
	getErr     error
	changed    map[string]bool
	syncErr    map[string]error
	gate       chan struct{}
	entered    chan string

	catalogReads int
	synced       []string
	gets         []string
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

func (f *syncFake) FetchServiceCatalogSHA(context.Context) (*gitops.ServiceCatalog, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catalogReads++
	if f.catalogErr != nil {
		return nil, f.sha, f.catalogErr
	}
	return &gitops.ServiceCatalog{}, f.sha, nil
}

func (f *syncFake) ListPreviews(context.Context) ([]string, error) {
	return f.previews, f.listErr
}

func (f *syncFake) GetPreview(_ context.Context, slug string) (*preview.PreviewValues, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, slug)
	if f.getErr != nil {
		return nil, "", f.getErr
	}
	if !slices.Contains(f.previews, slug) {
		return nil, "", gitops.ErrNotFound
	}
	return &preview.PreviewValues{}, "blob-" + slug, nil
}

func (f *syncFake) SyncPreviewWithRetry(
	_ context.Context,
	slug string,
	_ *gitops.ServiceCatalog,
) (*gitops.SyncResult, error) {
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
	if f.changed[slug] {
		return &gitops.SyncResult{Changed: true, CommitURL: syncCommitURL}, nil
	}
	return &gitops.SyncResult{}, nil
}

func (f *syncFake) FanOutPRComments(context.Context, preview.PreviewValues, string) error {
	return nil
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

// configSyncs fails t unless the badil_config_syncs_total samples of m are
// exactly want.
func configSyncs(t *testing.T, m *metrics.Metrics, want map[string]float64) {
	t.Helper()
	assertMetrics(t, m, want, "badil_config_syncs_total")
}

func TestSyncer_SyncAll(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a", "b", "c")
	f.changed = map[string]bool{"a": true}
	f.syncErr = map[string]error{"c": errors.New("sync boom")}
	report, err := lifecycle.NewSyncer(f, time.Minute).Sync(context.Background(), "")
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
	configSyncs(t, f.Metrics, map[string]float64{`badil_config_syncs_total{result="error",trigger="api"}`: 1})
	assertMetrics(t, f.Metrics, map[string]float64{"badil_sweep_previews_synced_total": 1},
		"badil_sweep_previews_synced_total")
}

func TestSyncer_SyncOne(t *testing.T) {
	cases := []struct {
		name       string
		slug       string
		getErr     error
		wantErr    error
		wantReport lifecycle.SyncReport
		wantSyncs  []string
		wantResult string
	}{
		{
			name: "rewritten", slug: "b", wantReport: lifecycle.SyncReport{Synced: []string{"b"}},
			wantSyncs: []string{"b"}, wantResult: "ok",
		},
		{name: "unknown", slug: "zz", wantErr: lifecycle.ErrPreviewNotFound, wantResult: "ok"},
		{
			name: "unreadable", slug: "b", getErr: errors.New("read boom"),
			wantReport: lifecycle.SyncReport{Failed: []string{"b"}}, wantResult: "error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := newSyncFake("a", "b")
			f.changed = map[string]bool{"b": true}
			f.getErr = tc.getErr
			report, err := lifecycle.NewSyncer(f, time.Minute).Sync(context.Background(), tc.slug)
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
			configSyncs(t, f.Metrics, map[string]float64{
				`badil_config_syncs_total{result="` + tc.wantResult + `",trigger="api"}`: 1,
			})
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
			_, err := lifecycle.NewSyncer(f, time.Minute).Sync(context.Background(), "")
			if err == nil || errors.Is(err, lifecycle.ErrConfigUnavailable) != tc.wantConfig {
				t.Fatalf("err = %v, want ErrConfigUnavailable %v", err, tc.wantConfig)
			}
			if got := f.syncs(); len(got) != 0 {
				t.Errorf("syncs = %v, want none", got)
			}
			configSyncs(t, f.Metrics, map[string]float64{`badil_config_syncs_total{result="error",trigger="api"}`: 1})
		})
	}
}

func TestSyncer_PushSyncsOnlyANewSHA(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a", "b")
	f.syncErr = map[string]error{"b": errors.New("sync boom")}
	s := lifecycle.NewSyncer(f, time.Minute)
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
	configSyncs(t, f.Metrics, map[string]float64{`badil_config_syncs_total{result="error",trigger="push"}`: 2})
}

func TestSyncer_PushConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		sha  string
		// wantRuns is the runs of two pushes: the second push of a recorded
		// SHA stops before any run.
		wantRuns float64
	}{
		{name: "unparsable content is recorded", sha: "sha-bad", wantRuns: 1},
		{name: "unreadable file is retried", sha: "", wantRuns: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := newSyncFake("a")
			f.sha, f.catalogErr = tc.sha, errors.New("catalog boom")
			s := lifecycle.NewSyncer(f, time.Minute)
			s.Push(context.Background())
			s.Push(context.Background())
			if f.reads() != 2 {
				t.Errorf("catalog reads = %d, want 2", f.reads())
			}
			if got := f.syncs(); len(got) != 0 {
				t.Errorf("syncs = %v, want none", got)
			}
			configSyncs(t, f.Metrics, map[string]float64{
				`badil_config_syncs_total{result="error",trigger="push"}`: tc.wantRuns,
			})
		})
	}
}

func TestSyncer_PushListErrorRecordsNoSHA(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
	f.listErr = errors.New("list boom")
	s := lifecycle.NewSyncer(f, time.Minute)
	ctx := context.Background()

	s.Push(ctx) // sha-1, previews can't be listed: nothing synced, no SHA kept.
	f.listErr = nil
	s.Push(ctx) // sha-1 again: retried, syncs a.
	s.Push(ctx) // sha-1 now recorded: nothing.
	if got := f.syncs(); !slices.Equal(got, []string{"a"}) {
		t.Errorf("syncs = %v, want one run over a after the list failure", got)
	}
	configSyncs(t, f.Metrics, map[string]float64{
		`badil_config_syncs_total{result="error",trigger="push"}`: 1,
		`badil_config_syncs_total{result="ok",trigger="push"}`:    1,
	})
}

func TestSyncer_PushDoesNotTouchAPISHA(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
	s := lifecycle.NewSyncer(f, time.Minute)
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
	release := f.gated()
	defer release()
	s := lifecycle.NewSyncer(f, time.Minute)

	var wg sync.WaitGroup
	wg.Go(func() { _, _ = s.Sync(context.Background(), "") })
	waitEntered(t, f)
	wg.Go(func() { s.Push(context.Background()) })
	wg.Go(func() { _, _ = s.Sync(context.Background(), "a") })
	time.Sleep(quiet)
	if got := f.reads(); got != 1 {
		t.Fatalf("catalog reads during the first run = %d, want 1 (the others wait for the guard)", got)
	}
	f.mu.Lock()
	gets := len(f.gets)
	f.mu.Unlock()
	if gets != 0 {
		t.Errorf("slug run read its preview while another run held the guard")
	}
	release()
	wg.Wait()
	if got := f.reads(); got != 3 {
		t.Errorf("catalog reads = %d, want 3", got)
	}
}

func TestSyncer_PushArrivalsCollapse(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
	release := f.gated()
	defer release()
	s := lifecycle.NewSyncer(f, time.Minute)

	var wg sync.WaitGroup
	wg.Go(func() { _, _ = s.Sync(context.Background(), "") })
	waitEntered(t, f)
	returned := make(chan struct{}, 3)
	for range 3 {
		wg.Go(func() {
			s.Push(context.Background())
			returned <- struct{}{}
		})
	}
	for range 2 {
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("collapsed pushes did not return while the run held the guard")
		}
	}
	release()
	wg.Wait()
	if got := f.reads(); got != 2 {
		t.Errorf("catalog reads = %d, want 2 (the API run and one push follow-up)", got)
	}
	configSyncs(t, f.Metrics, map[string]float64{
		`badil_config_syncs_total{result="ok",trigger="api"}`:  1,
		`badil_config_syncs_total{result="ok",trigger="push"}`: 1,
	})
}

func TestSyncer_APICallsDoNotCollapse(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
	f.changed = map[string]bool{"a": true}
	release := f.gated()
	defer release()
	s := lifecycle.NewSyncer(f, time.Minute)

	var wg sync.WaitGroup
	wg.Go(func() { s.Push(context.Background()) })
	waitEntered(t, f)
	reports := make(chan lifecycle.SyncReport, 3)
	for range 3 {
		wg.Go(func() {
			report, err := s.Sync(context.Background(), "")
			if err != nil {
				t.Errorf("Sync: %v", err)
			}
			reports <- report
		})
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
	if n != 3 {
		t.Errorf("answered API calls = %d, want 3", n)
	}
	configSyncs(t, f.Metrics, map[string]float64{
		`badil_config_syncs_total{result="ok",trigger="api"}`:  3,
		`badil_config_syncs_total{result="ok",trigger="push"}`: 1,
	})
}

func TestSyncer_SyncWaitStopsWithContext(t *testing.T) {
	captureLogs(t)
	f := newSyncFake("a")
	release := f.gated()
	defer release()
	s := lifecycle.NewSyncer(f, time.Minute)

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

func TestSyncer_RunBoundedFromGuard(t *testing.T) {
	captureLogs(t)
	f := &deadlineFake{syncFake: newSyncFake("a")}
	s := lifecycle.NewSyncer(f, time.Hour)
	if _, err := s.Sync(context.Background(), ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if left := time.Until(f.deadline); left < 59*time.Minute || left > time.Hour {
		t.Errorf("run deadline in %v, want about the 1h bound", left)
	}
}

// deadlineFake is a syncFake recording the deadline of the catalog read.
type deadlineFake struct {
	*syncFake

	deadline time.Time
}

func (f *deadlineFake) FetchServiceCatalogSHA(ctx context.Context) (*gitops.ServiceCatalog, string, error) {
	f.deadline, _ = ctx.Deadline()
	return f.syncFake.FetchServiceCatalogSHA(ctx)
}
