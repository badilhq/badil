package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/store"
)

// Config sync triggers, the trigger attribute of the config sync logs.
const (
	TriggerPush = "push"
	TriggerAPI  = "api"
)

// Errors of Syncer.Sync. Callers test them with errors.Is.
var (
	// ErrConfigUnavailable wraps a badil.yaml that can't be read or parsed.
	ErrConfigUnavailable = errors.New("badil.yaml unavailable")
	// ErrPreviewNotFound is a slug whose preview file does not exist.
	ErrPreviewNotFound = errors.New("preview not found")
)

// PreviewSyncStore is the subset of store.GitHubStore used by syncPreview.
type PreviewSyncStore interface {
	SyncPreviewWithRetry(ctx context.Context, slug string, catalog *store.ServiceCatalog) (*store.SyncResult, error)
}

// PreviewSyncAPI is what syncPreview uses: the store sync, the forge and the
// comment fan-out, and the Recorder of the rewrites.
type PreviewSyncAPI struct {
	Store    PreviewSyncStore
	Forge    forge.Forge
	Recorder Recorder
	FanOut   FanOutFunc
}

// SyncStore is the subset of store.GitHubStore used by Syncer.
type SyncStore interface {
	PreviewSyncStore
	ListPreviews(ctx context.Context) ([]string, error)
	FetchServiceCatalogSHA(ctx context.Context) (*store.ServiceCatalog, string, error)
}

// SyncAPI is what Syncer uses: the store, the forge, the Recorder and the
// comment fan-out.
type SyncAPI struct {
	Store    SyncStore
	Forge    forge.Forge
	Recorder Recorder
	FanOut   FanOutFunc
}

// previewSyncAPI narrows a to what syncPreview uses.
func (a SyncAPI) previewSyncAPI() PreviewSyncAPI {
	return PreviewSyncAPI{Store: a.Store, Forge: a.Forge, Recorder: a.Recorder, FanOut: a.FanOut}
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
// push run (Push) or an API run (Sync), each waiting for the guard in turn.
// Waiters acquire the guard in no particular order. Each run is capped at
// timeout from when it holds the guard.
type Syncer struct {
	api     SyncAPI
	timeout time.Duration
	// guard is held, by a send, for the length of a guarded run.
	guard chan struct{}
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

// Push runs the sync of a push to the gitops default branch. Holding the
// guard, it reads badil.yaml's blob SHA and stops when it equals the SHA the
// last push run synced: a push queued behind the run that synced its content
// does nothing more. Otherwise every preview is synced with the catalog of
// that content and the SHA recorded, also when a preview failed (the sweep
// retries it) or the content can't be parsed (the same content would fail
// again). A badil.yaml that can't be read, or previews that can't be listed,
// record nothing, so the next push retries.
func (s *Syncer) Push(ctx context.Context) {
	if err := s.acquire(ctx); err != nil {
		return
	}
	defer s.release()

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	catalog, sha, err := s.api.Store.FetchServiceCatalogSHA(ctx)
	if sha != "" && sha == s.lastSHA {
		slog.InfoContext(ctx, "config sync skipped",
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
		configFailed(ctx, TriggerPush, err)
		return
	}
	report, err := s.syncAll(ctx, catalog)
	if err == nil {
		s.lastSHA = sha
	}
	finish(ctx, TriggerPush, report, err)
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
	catalog, _, err := s.api.Store.FetchServiceCatalogSHA(ctx)
	if err != nil {
		configFailed(ctx, TriggerAPI, err)
		return SyncReport{}, fmt.Errorf("%w: %w", ErrConfigUnavailable, err)
	}
	if slug == "" {
		report, listErr := s.syncAll(ctx, catalog)
		finish(ctx, TriggerAPI, report, listErr)
		return report, listErr
	}
	report, found := s.syncOne(ctx, catalog, slug)
	if !found {
		return report, fmt.Errorf("%w: %s", ErrPreviewNotFound, slug)
	}
	finish(ctx, TriggerAPI, report, nil)
	return report, nil
}

// configFailed logs a run stopped by an unreadable badil.yaml.
func configFailed(ctx context.Context, trigger string, err error) {
	slog.ErrorContext(ctx, "config sync failed",
		slog.String("trigger", trigger),
		slog.String("op", "read badil.yaml"),
		slog.String("err", err.Error()),
	)
}

// finish logs a run that read badil.yaml: an error when err (a ListPreviews
// error) is set, its counts otherwise.
func finish(ctx context.Context, trigger string, report SyncReport, err error) {
	if err != nil {
		slog.ErrorContext(ctx, "config sync failed",
			slog.String("trigger", trigger),
			slog.String("op", "list previews"),
			slog.String("err", err.Error()),
		)
		return
	}
	slog.InfoContext(ctx, "config sync finished",
		slog.String("trigger", trigger),
		slog.Int("synced", len(report.Synced)),
		slog.Int("unchanged", report.Unchanged),
		slog.Int("failed", len(report.Failed)),
	)
}

// syncAll lists the previews and syncs each with catalog. A ListPreviews
// error is returned at once.
func (s *Syncer) syncAll(ctx context.Context, catalog *store.ServiceCatalog) (SyncReport, error) {
	slugs, err := s.api.Store.ListPreviews(ctx)
	if err != nil {
		return SyncReport{}, err
	}
	var report SyncReport
	for _, slug := range slugs {
		res, syncErr := syncPreview(ctx, s.api.previewSyncAPI(), slug, catalog)
		report.add(ctx, slug, res, syncErr)
	}
	return report, nil
}

// syncOne syncs the preview slug with catalog. It reports false, syncing
// nothing, when the file of slug does not exist.
func (s *Syncer) syncOne(ctx context.Context, catalog *store.ServiceCatalog, slug string) (SyncReport, bool) {
	var report SyncReport
	res, err := syncPreview(ctx, s.api.previewSyncAPI(), slug, catalog)
	if err == nil && res.NotFound {
		return report, false
	}
	report.add(ctx, slug, res, err)
	return report, true
}

// add adds to r the outcome of syncPreview for slug: res, or err, which is
// logged.
func (r *SyncReport) add(ctx context.Context, slug string, res *store.SyncResult, err error) {
	switch {
	case err != nil:
		previewSyncFailed(ctx, slug, err)
		r.Failed = append(r.Failed, slug)
	case res.Changed:
		r.Synced = append(r.Synced, slug)
	default:
		r.Unchanged++
	}
}

// previewSyncFailed logs a preview a run failed to sync.
func previewSyncFailed(ctx context.Context, slug string, err error) {
	slog.ErrorContext(ctx, "config sync preview failed",
		slog.String("slug", slug),
		slog.String("err", err.Error()),
	)
}
