package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/forge"
)

// Recorder records the webhook deliveries the server answers.
// *metrics.Metrics implements it.
type Recorder interface {
	WebhookDelivery(event, status string)
}

// nopRecorder is the Recorder of a Server built without WithMetrics: it
// records nothing.
type nopRecorder struct{}

func (nopRecorder) WebhookDelivery(string, string) {}

// Option configures a Server built by NewServer.
type Option func(*Server)

// WithMetrics records webhook deliveries in m. A nil m leaves the default,
// which records nothing.
func WithMetrics(m Recorder) Option {
	return func(s *Server) {
		if m != nil {
			s.metrics = m
		}
	}
}

// WithStatusToken enables POST /webhooks/status, authenticated by the Bearer
// token; an empty token leaves the route unregistered.
func WithStatusToken(token string) Option {
	return func(s *Server) {
		s.statusToken = token
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

type Server struct {
	// app runs the reconciles, the deploys, the config syncs and the
	// status intake, and holds the reconcile outcomes and the wake of the
	// waits.
	app   *app.App
	forge forge.Forge
	// webhooks verifies and parses the deliveries of /webhooks/github.
	webhooks    forge.WebhookParser
	metrics     Recorder
	deployToken string
	// statusToken authenticates POST /webhooks/status, "" when it is
	// disabled.
	statusToken string
	// readToken authenticates the GET /v1 routes, besides deployToken.
	readToken string
	// previewTTL is the in-server sweep's TTL, 0 when it does not expire
	// previews.
	previewTTL time.Duration
	// waitTimeout is the longest a wait blocks.
	waitTimeout time.Duration
	// shutdown is closed when the HTTP server's shutdown starts (EndWaits).
	shutdown     chan struct{}
	shutdownOnce sync.Once
	router       chi.Router
}

// NewServer returns the server of the webhooks webhooks parses, over the
// application services a (non-nil) and the forge f, configured by opts. The deploy API is served only when deployToken is set,
// and the read API when deployToken or the read token is.
func NewServer(
	a *app.App,
	f forge.Forge,
	webhooks forge.WebhookParser,
	deployToken string,
	opts ...Option,
) *Server {
	s := &Server{
		app:         a,
		forge:       f,
		webhooks:    webhooks,
		metrics:     nopRecorder{},
		deployToken: deployToken,
		waitTimeout: waitTimeout,
		shutdown:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(accessLog)
	r.Get(healthzPath, s.handleHealthz)
	r.Post("/webhooks/github", s.handleWebhook)
	if s.statusToken != "" && s.app.HasStatus() {
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

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// EndWaits ends every blocked wait, which answers its current state at once,
// and every later one after its first evaluation. It is registered with
// http.Server.RegisterOnShutdown, so shutdown does not wait for long polls.
func (s *Server) EndWaits() {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}
