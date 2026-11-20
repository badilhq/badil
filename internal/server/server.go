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
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/status"
)

// syncTimeout caps each config sync run (a push to the gitops default branch,
// or POST /v1/sync), counted from when the run holds the syncer's guard.
const syncTimeout = 10 * time.Minute

// Option configures a Server built by NewServer.
type Option func(*Server)

// WithMetrics records webhook deliveries and lifecycle outcomes in m.
func WithMetrics(m *metrics.Metrics) Option {
	return func(s *Server) {
		s.metrics = m
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

type Server struct {
	forge forge.Forge
	store storeAPI
	// fanOut posts a preview's summary on its open PRs:
	// lifecycle.FanOutPRComments.
	fanOut      lifecycle.FanOutFunc
	metrics     *metrics.Metrics
	deployToken string
	// statusToken authenticates POST /webhooks/status, "" when it is
	// disabled.
	statusToken string
	// status holds the status the renderer reports.
	status *status.Store
	router chi.Router
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

// NewServer returns the server of the webhooks of f, over the gitops repo
// st, configured by opts. The deploy API is served only when deployToken is
// set.
func NewServer(
	f forge.Forge,
	st storeAPI,
	deployToken string,
	opts ...Option,
) *Server {
	s := &Server{
		forge:       f,
		store:       st,
		fanOut:      lifecycle.FanOutPRComments,
		deployToken: deployToken,
		jobTimeout:  ReconcileTimeout,
	}
	for _, opt := range opts {
		opt(s)
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
	if deployToken != "" {
		r.Route("/v1", func(r chi.Router) {
			r.Use(requireBearer(deployToken))
			r.Post("/deploy", s.handleDeploy)
			r.Post("/sync", s.handleSync)
		})
	}
	s.router = r
	return s
}

// reconcileAPI is the store, the forge, the metrics and the fan-out as
// lifecycle.ReconcilePR expects them. A nil *metrics.Metrics records nothing.
func (s *Server) reconcileAPI() lifecycle.ReconcileAPI {
	return lifecycle.ReconcileAPI{Store: s.store, Forge: s.forge, Recorder: s.metrics, FanOut: s.fanOut}
}

// deployAPI is the store, the forge, the metrics and the fan-out as
// lifecycle.DeployPR expects them.
func (s *Server) deployAPI() lifecycle.DeployAPI {
	return lifecycle.DeployAPI{Store: s.store, Forge: s.forge, Recorder: s.metrics, FanOut: s.fanOut}
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
