package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/preview"
)

// Config sync triggers, the trigger label of SyncAPI.ConfigSync.
const (
	TriggerPush = "push"
	TriggerAPI  = "api"
)

// Config sync results, the result label of SyncAPI.ConfigSync.
const (
	syncOK    = "ok"
	syncError = "error"
)

// Errors of Syncer.Sync. Callers test them with errors.Is.
var (
	// ErrConfigUnavailable wraps a badil.yaml that can't be read or parsed.
	ErrConfigUnavailable = errors.New("badil.yaml unavailable")
	// ErrPreviewNotFound is a slug whose preview file does not exist.
	ErrPreviewNotFound = errors.New("preview not found")
)

// PreviewSyncAPI is what syncPreview uses: the gitops sync and comment fan-out,
// and the rewrite counter.
type PreviewSyncAPI interface {
	SyncPreviewWithRetry(ctx context.Context, slug string, catalog *gitops.ServiceCatalog) (*gitops.SyncResult, error)
	FanOutPRComments(ctx context.Context, vals preview.PreviewValues, body string) error
	// SweepSynced records one preview rewritten to match badil.yaml.
	SweepSynced()
}

// SyncAPI is the subset of gitops.Client used by Syncer, plus its recorder.
type SyncAPI interface {
	PreviewSyncAPI
	ListPreviews(ctx context.Context) ([]string, error)
	GetPreview(ctx context.Context, slug string) (*preview.PreviewValues, string, error)
	FetchServiceCatalogSHA(ctx context.Context) (*gitops.ServiceCatalog, string, error)
	// ConfigSync records one guarded run, by trigger and result.
	ConfigSync(trigger, result string)
}

// SyncReport is the outcome of a run: the slugs rewritten, the number of
// previews left unchanged and the slugs that failed, in listing order.
type SyncReport struct {
	Synced    []string
	Unchanged int
	Failed    []string
}

// Syncer runs the sweep's sync step (syncPreview) over every preview, or one,
// outside the sweep. One guarded run happens at a time in the process: a
// push run (Push) or an API run (Sync). Push arrivals while a push run waits
// for the guard collapse into it; API runs never collapse, each waits for the
// guard in turn. Waiters acquire the guard in no particular order. Each run is
// capped at timeout from when it holds the guard.
type Syncer struct {
	api     SyncAPI
	timeout time.Duration
	// guard is held, by a send, for the length of a guarded run.
	guard chan struct{}

	// mu guards pushWaiting, true while a push run waits for the guard.
	mu          sync.Mutex
	pushWaiting bool
	// lastSHA is the blob SHA of the badil.yaml the last push run synced, ""
	// before the first. Only the holder of guard reads or writes it.
	lastSHA string
}

// NewSyncer returns a Syncer over api whose runs are capped at timeout.
func NewSyncer(api SyncAPI, timeout time.Duration) *Syncer {
	return &Syncer{api: api, timeout: timeout, guard: make(chan struct{}, 1)}
}

// acquire takes the guard, or returns ctx's error when ctx is done first.
func (s *Syncer) acquire(ctx context.Context) error {
	select {
	case s.guard <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release gives the guard back.
func (s *Syncer) release() {
	<-s.guard
}

// Push runs the sync of a push to the gitops default branch. It returns at
// once when another push run already waits for the guard: that run covers
// this push. Holding the guard, it reads badil.yaml's blob SHA and stops when
// it equals the SHA the last push run synced; such a push is not a run and is
// not recorded. Otherwise every preview is synced with the catalog of that
// content and the SHA recorded, also when a preview failed (the sweep retries
// it) or the content can't be parsed (the same content would fail again). A
// badil.yaml that can't be read, or previews that can't be listed, record
// nothing but the error, so the next push retries.
func (s *Syncer) Push(ctx context.Context) {
	s.mu.Lock()
	if s.pushWaiting {
		s.mu.Unlock()
		slog.InfoContext(ctx, "config sync collapsed") //nolint:sloglint // lifecycle has no scoped logger yet
		return
	}
	s.pushWaiting = true
	s.mu.Unlock()

	err := s.acquire(ctx)
	s.mu.Lock()
	s.pushWaiting = false
	s.mu.Unlock()
	if err != nil {
		return
	}
	defer s.release()

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	catalog, sha, err := s.api.FetchServiceCatalogSHA(ctx)
	if sha != "" && sha == s.lastSHA {
		slog.InfoContext(ctx, "config sync skipped", //nolint:sloglint // lifecycle has no scoped logger yet
			slog.String("trigger", TriggerPush),
			slog.String("badil_yaml_sha", sha),
			slog.String("reason", "badil.yaml unchanged"),
		)
		return
	}
	if err != nil {
		if sha != "" {
			s.lastSHA = sha
		}
		s.configFailed(ctx, TriggerPush, err)
		return
	}
	report, err := s.syncAll(ctx, catalog)
	if err == nil {
		s.lastSHA = sha
	}
	s.finish(ctx, TriggerPush, report, err)
}

// Sync runs an API sync: every preview when slug is "", else the preview
// slug, which the caller has validated. It waits for the guard until ctx is
// done. It returns an error wrapping ErrConfigUnavailable when badil.yaml can't
// be read or parsed, ErrPreviewNotFound when slug has no preview file, the
// listing error when the previews can't be listed, or ctx's error when it was
// done before the guard was free. A failed preview is in the report, not an
// error. It never reads nor changes the SHA of the push runs.
func (s *Syncer) Sync(ctx context.Context, slug string) (SyncReport, error) {
	if err := s.acquire(ctx); err != nil {
		return SyncReport{}, err
	}
	defer s.release()

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	catalog, _, err := s.api.FetchServiceCatalogSHA(ctx)
	if err != nil {
		s.configFailed(ctx, TriggerAPI, err)
		return SyncReport{}, fmt.Errorf("%w: %w", ErrConfigUnavailable, err)
	}
	if slug == "" {
		report, listErr := s.syncAll(ctx, catalog)
		s.finish(ctx, TriggerAPI, report, listErr)
		return report, listErr
	}
	report, found := s.syncOne(ctx, catalog, slug)
	if !found {
		// Nothing failed: the run is recorded ok and answered as not found.
		s.api.ConfigSync(TriggerAPI, syncOK)
		return report, fmt.Errorf("%w: %s", ErrPreviewNotFound, slug)
	}
	s.finish(ctx, TriggerAPI, report, nil)
	return report, nil
}

// configFailed logs and records a run stopped by an unreadable badil.yaml.
func (s *Syncer) configFailed(ctx context.Context, trigger string, err error) {
	s.api.ConfigSync(trigger, syncError)
	slog.ErrorContext(ctx, "config sync failed", //nolint:sloglint // lifecycle has no scoped logger yet
		slog.String("trigger", trigger),
		slog.String("op", "read badil.yaml"),
		slog.String("err", err.Error()),
	)
}

// finish logs and records a run that read badil.yaml: an error when err (a
// ListPreviews error) is set or a preview failed, ok otherwise.
func (s *Syncer) finish(ctx context.Context, trigger string, report SyncReport, err error) {
	if err != nil {
		s.api.ConfigSync(trigger, syncError)
		slog.ErrorContext(ctx, "config sync failed", //nolint:sloglint // lifecycle has no scoped logger yet
			slog.String("trigger", trigger),
			slog.String("op", "list previews"),
			slog.String("err", err.Error()),
		)
		return
	}
	result := syncOK
	if len(report.Failed) > 0 {
		result = syncError
	}
	s.api.ConfigSync(trigger, result)
	slog.InfoContext(ctx, "config sync finished", //nolint:sloglint // lifecycle has no scoped logger yet
		slog.String("trigger", trigger),
		slog.Int("synced", len(report.Synced)),
		slog.Int("unchanged", report.Unchanged),
		slog.Int("failed", len(report.Failed)),
	)
}

// syncAll lists the previews and syncs each with catalog. A ListPreviews
// error is returned at once.
func (s *Syncer) syncAll(ctx context.Context, catalog *gitops.ServiceCatalog) (SyncReport, error) {
	slugs, err := s.api.ListPreviews(ctx)
	if err != nil {
		return SyncReport{}, err
	}
	var report SyncReport
	for _, slug := range slugs {
		s.syncInto(ctx, &report, catalog, slug)
	}
	return report, nil
}

// syncOne syncs the preview slug with catalog. It reports false, syncing
// nothing, when the file of slug does not exist. A failed read of the file is
// a failed preview.
func (s *Syncer) syncOne(ctx context.Context, catalog *gitops.ServiceCatalog, slug string) (SyncReport, bool) {
	var report SyncReport
	_, _, err := s.api.GetPreview(ctx, slug)
	switch {
	case errors.Is(err, gitops.ErrNotFound):
		return report, false
	case err != nil:
		previewSyncFailed(ctx, slug, err)
		report.Failed = append(report.Failed, slug)
		return report, true
	}
	s.syncInto(ctx, &report, catalog, slug)
	return report, true
}

// syncInto syncs the preview slug with catalog and adds its outcome to report.
func (s *Syncer) syncInto(ctx context.Context, report *SyncReport, catalog *gitops.ServiceCatalog, slug string) {
	changed, err := syncPreview(ctx, s.api, slug, catalog)
	switch {
	case err != nil:
		previewSyncFailed(ctx, slug, err)
		report.Failed = append(report.Failed, slug)
	case changed:
		report.Synced = append(report.Synced, slug)
	default:
		report.Unchanged++
	}
}

// previewSyncFailed logs a preview a run failed to sync.
func previewSyncFailed(ctx context.Context, slug string, err error) {
	slog.ErrorContext(ctx, "config sync preview failed", //nolint:sloglint // lifecycle has no scoped logger yet
		slog.String("slug", slug),
		slog.String("err", err.Error()),
	)
}
