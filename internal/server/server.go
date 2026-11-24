package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/status"
)

// syncTimeout caps each config sync run (a push to the gitops default branch,
// or POST /v1/sync), counted from when the run holds the syncer's guard.
const syncTimeout = 10 * time.Minute

// Recorder records the webhook deliveries, the renderer status events and the
// spin-up durations the server observes, and, as a lifecycle.Recorder, the
// outcomes of the lifecycle operations it runs. *metrics.Metrics implements
// it.
type Recorder interface {
	lifecycle.Recorder
	WebhookDelivery(event, status string)
	StatusEvent(status, result string)
	SpinUp(service string, d time.Duration)
}

// nopRecorder is the Recorder of a Server built without WithMetrics: it
// records nothing.
type nopRecorder struct{}

func (nopRecorder) Reconcile(string, string) {}

func (nopRecorder) Teardown(string, string) {}

func (nopRecorder) SweepListed(int) {}

func (nopRecorder) SweepDeleted(string) {}

func (nopRecorder) PreviewSynced() {}

func (nopRecorder) PreviewCreated() {}

func (nopRecorder) WebhookDelivery(string, string) {}

func (nopRecorder) StatusEvent(string, string) {}

func (nopRecorder) SpinUp(string, time.Duration) {}

// Option configures a Server built by NewServer.
type Option func(*Server)

// WithMetrics records webhook deliveries and lifecycle outcomes in m. A nil m
// leaves the default, which records nothing.
func WithMetrics(m Recorder) Option {
	return func(s *Server) {
		if m != nil {
			s.metrics = m
		}
	}
}

// WithNamespaces checks the namespace of each new preview through c before
// it is written (lifecycle.NamespaceChecker). Without it, no namespace is
// checked.
func WithNamespaces(c lifecycle.NamespaceChecker) Option {
	return func(s *Server) {
		s.namespaces = c
	}
}

// WithStatus enables POST /webhooks/status, authenticated by the Bearer
// token, recording into st. An empty token leaves the route unregistered.
func WithStatus(token string, st *status.Store) Option {
	return func(s *Server) {
		s.statusToken = token
		s.status = st
	}
}

// WithReadToken enables the GET /v1 routes with the Bearer token, besides the
// deploy token. An empty token leaves them to the deploy token alone.
func WithReadToken(token string) Option {
	return func(s *Server) {
		s.readToken = token
	}
}

// WithPreviewTTL sets the TTL of the in-server sweep, which a preview's
// expiresAt adds to its updatedAt; 0, the default, omits expiresAt.
func WithPreviewTTL(ttl time.Duration) Option {
	return func(s *Server) {
		s.previewTTL = ttl
	}
}

// WithWake has the wait routes block on w, which the caller also notifies
// on the changes the server does not see itself: a landed Apply
// (store.WithApplied) and a fetch that moved the gitops head
// (store.WithHeadMoved). Without it, the server makes its own.
func WithWake(w *Wake) Option {
	return func(s *Server) {
		s.wake = w
	}
}

type Server struct {
	forge forge.Forge
	// webhooks verifies and parses the deliveries of /webhooks/github.
	webhooks forge.WebhookParser
	store    storeAPI
	// fanOut posts a preview's summary on its open PRs:
	// lifecycle.FanOutPRComments.
	fanOut  lifecycle.FanOutFunc
	metrics Recorder
	// namespaces checks the namespace of a new preview, nil when none is
	// checked.
	namespaces  lifecycle.NamespaceChecker
	deployToken string
	// statusToken authenticates POST /webhooks/status, "" when it is
	// disabled.
	statusToken string
	// status holds the status the renderer reports.
	status *status.Store
	// readToken authenticates the GET /v1 routes, besides deployToken.
	readToken string
	// previewTTL is the in-server sweep's TTL, 0 when it does not expire
	// previews.
	previewTTL time.Duration
	// outcomes holds the latest reconcile outcome of each PR.
	outcomes *outcomes
	// wake is notified on every change a wait may be waiting for.
	wake *Wake
	// waitTimeout is the longest a wait blocks.
	waitTimeout time.Duration
	// shutdown is closed when the HTTP server's shutdown starts (EndWaits).
	shutdown     chan struct{}
	shutdownOnce sync.Once
	router       chi.Router
	// bg tracks background jobs (reconciles), queued or running,
	// for WaitBackground.
	bg sync.WaitGroup
	// jobs serializes background jobs: at most one runs at a time.
	jobs sync.Mutex
	// jobTimeout caps each background job, counted from when it holds jobs.
	jobTimeout time.Duration
	// syncer runs the config syncs, one at a time.
	syncer *lifecycle.Syncer
}

// NewServer returns the server of the webhooks webhooks parses, over the
// forge f and the gitops repo st, configured by opts. The deploy API is
// served only when deployToken is set, and the read API when deployToken or
// the read token is.
func NewServer(
	f forge.Forge,
	webhooks forge.WebhookParser,
	st storeAPI,
	deployToken string,
	opts ...Option,
) *Server {
	s := &Server{
		forge:       f,
		webhooks:    webhooks,
		store:       st,
		fanOut:      lifecycle.FanOutPRComments,
		metrics:     nopRecorder{},
		deployToken: deployToken,
		jobTimeout:  ReconcileTimeout,
		outcomes:    newOutcomes(),
		waitTimeout: waitTimeout,
		shutdown:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.wake == nil {
		s.wake = NewWake()
	}
	s.syncer = lifecycle.NewSyncer(s.syncAPI(), syncTimeout)
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(accessLog)
	r.Get(healthzPath, s.handleHealthz)
	r.Post("/webhooks/github", s.handleWebhook)
	if s.statusToken != "" && s.status != nil {
		r.With(requireBearer(s.statusToken)).Post(statusPath, s.handleStatusEvent)
	}
	if deployToken != "" || s.readToken != "" {
		r.Route("/v1", func(r chi.Router) {
			if deployToken != "" {
				r.With(requireBearer(deployToken)).Post("/deploy", s.handleDeploy)
				r.With(requireBearer(deployToken)).Post("/sync", s.handleSync)
			}
			r.Group(func(r chi.Router) {
				r.Use(requireBearer(s.readToken, deployToken))
				r.Get("/previews", s.handleListPreviews)
				r.Get("/previews/{slug}", s.handleGetPreview)
				r.Get("/previews/{slug}/wait", s.handleSlugWait)
				r.Get("/wait", s.handleWait)
			})
		})
	}
	s.router = r
	return s
}

// reconcileAPI is the store, the forge, the metrics, the fan-out and the
// namespace check as lifecycle.ReconcilePR expects them.
func (s *Server) reconcileAPI() lifecycle.ReconcileAPI {
	return lifecycle.ReconcileAPI{
		Store: s.store, Forge: s.forge, Recorder: s.metrics, FanOut: s.fanOut, Namespaces: s.namespaces,
	}
}

// deployAPI is the store, the forge, the metrics, the fan-out and the
// namespace check as lifecycle.DeployPR expects them.
func (s *Server) deployAPI() lifecycle.DeployAPI {
	return lifecycle.DeployAPI{
		Store: s.store, Forge: s.forge, Recorder: s.metrics, FanOut: s.fanOut, Namespaces: s.namespaces,
	}
}

// syncAPI is the store, the forge, the metrics and the fan-out as
// lifecycle.Syncer expects them.
func (s *Server) syncAPI() lifecycle.SyncAPI {
	return lifecycle.SyncAPI{Store: s.store, Forge: s.forge, Recorder: s.metrics, FanOut: s.fanOut}
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Go runs f in the background, tracked by WaitBackground. Unlike reconciles,
// f does not wait for other background jobs.
func (s *Server) Go(f func()) {
	s.bg.Go(f)
}

// WaitBackground blocks until every background job has finished. It
// returns nil once they are done, or ctx.Err() when ctx expires first.
func (s *Server) WaitBackground(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
