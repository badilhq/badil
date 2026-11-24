package app

import (
	"context"
	"sync"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

// Store is the gitops store the lifecycle APIs of an App are built over.
type Store interface {
	lifecycle.ReconcileStore
	lifecycle.SyncStore
	// ListPreviewFiles lists the slugs of the previews whose values.yaml is
	// in the gitops repo, sorted.
	ListPreviewFiles(ctx context.Context) ([]string, error)
	// GetPreview reads previews/<slug>/values.yaml, store.ErrNotFound when
	// it does not exist.
	GetPreview(ctx context.Context, slug string) (*preview.Values, string, error)
}

// Metrics records the outcomes of the lifecycle operations an App runs.
// *metrics.Metrics implements it.
type Metrics interface {
	lifecycle.Recorder
}

// nopMetrics is the Metrics of an App built without one: it records
// nothing.
type nopMetrics struct{}

func (nopMetrics) Reconcile(string, string) {}

func (nopMetrics) Teardown(string, string) {}

func (nopMetrics) SweepListed(int) {}

func (nopMetrics) SweepDeleted(string) {}

func (nopMetrics) PreviewSynced() {}

func (nopMetrics) PreviewCreated() {}

// Config configures an App built by New.
type Config struct {
	// Store is the gitops repo.
	Store Store
	// Forge is the forge of the PRs.
	Forge forge.Forge
	// Metrics records the lifecycle outcomes. Nil records nothing.
	Metrics Metrics
	// FanOut posts a preview's summary on its open PRs. Nil is
	// lifecycle.FanOutPRComments.
	FanOut lifecycle.FanOutFunc
	// Namespaces checks the namespace of each new preview before it is
	// written (lifecycle.NamespaceChecker). Nil checks none.
	Namespaces lifecycle.NamespaceChecker
	// Wake is notified on every change a wait may be waiting for; the caller
	// also notifies it on the changes the app does not see itself: a landed
	// Apply (store.WithApplied) and a fetch that moved the gitops head
	// (store.WithHeadMoved). Nil has the app make its own.
	Wake *Wake
	// JobTimeout caps each background job, counted from when it holds the
	// job queue. 0 is ReconcileTimeout.
	JobTimeout time.Duration
	// Status holds the status the renderer reports. Nil means no status is
	// known.
	Status StatusRepository
}

// App is badil's application services: the background job queue that runs
// reconciles, the latest reconcile outcome of each PR, the wake signal of the
// waits, the lifecycle APIs, the read model of the previews and the evaluation
// of the waits, built by New.
type App struct {
	store   Store
	forge   forge.Forge
	metrics Metrics
	// fanOut posts a preview's summary on its open PRs:
	// lifecycle.FanOutPRComments.
	fanOut lifecycle.FanOutFunc
	// namespaces checks the namespace of a new preview, nil when none is
	// checked.
	namespaces lifecycle.NamespaceChecker
	// outcomes holds the latest reconcile outcome of each PR.
	outcomes *outcomes
	// wake is notified on every change a wait may be waiting for.
	wake *Wake
	// bg tracks background jobs (reconciles), queued or running,
	// for WaitBackground.
	bg sync.WaitGroup
	// jobs serializes background jobs: at most one runs at a time.
	jobs sync.Mutex
	// jobTimeout caps each background job, counted from when it holds jobs.
	jobTimeout time.Duration
	// status holds the status the renderer reports, nil when none is known.
	status StatusRepository
}

// New returns the App cfg configures.
func New(cfg Config) *App {
	a := &App{
		store:      cfg.Store,
		forge:      cfg.Forge,
		metrics:    cfg.Metrics,
		fanOut:     cfg.FanOut,
		namespaces: cfg.Namespaces,
		outcomes:   newOutcomes(),
		wake:       cfg.Wake,
		jobTimeout: cfg.JobTimeout,
		status:     cfg.Status,
	}
	if a.metrics == nil {
		a.metrics = nopMetrics{}
	}
	if a.fanOut == nil {
		a.fanOut = lifecycle.FanOutPRComments
	}
	if a.wake == nil {
		a.wake = NewWake()
	}
	if a.jobTimeout == 0 {
		a.jobTimeout = ReconcileTimeout
	}
	return a
}

// Wake returns the wake signal of the waits.
func (a *App) Wake() *Wake {
	return a.wake
}

// Go runs f in the background, tracked by WaitBackground. Unlike reconciles,
// f does not wait for other background jobs.
func (a *App) Go(f func()) {
	a.bg.Go(f)
}

// WaitBackground blocks until every background job has finished. It
// returns nil once they are done, or ctx.Err() when ctx expires first.
func (a *App) WaitBackground(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		a.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
