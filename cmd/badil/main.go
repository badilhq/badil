package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/spf13/cobra"

	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/config"
	forgegithub "github.com/badilhq/badil/internal/forge/github"
	"github.com/badilhq/badil/internal/github"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/namespace"
	"github.com/badilhq/badil/internal/server"
	"github.com/badilhq/badil/internal/status"
	statuskube "github.com/badilhq/badil/internal/status/kube"
	"github.com/badilhq/badil/internal/store"
)

// shutdownTimeout bounds graceful shutdown: draining in-flight requests and
// then background teardowns share it, inside the default 30s pod grace period.
const shutdownTimeout = 25 * time.Second

// readHeaderTimeout bounds reading a request's headers on every listener.
const readHeaderTimeout = 10 * time.Second

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(exitCode(err))
	}
}

// rootCmd builds the badil command and its subcommands.
func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "badil",
		Short:        "Preview environment manager",
		Version:      buildVersion(),
		SilenceUsage: true,
	}

	root.AddCommand(serveCmd())
	root.AddCommand(cleanupCmd())
	root.AddCommand(listCmd())
	root.AddCommand(waitCmd())
	root.AddCommand(verifyAppCmd())
	root.AddCommand(bootstrapAppCmd())
	root.AddCommand(versionCmd())
	return root
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			interval, ttl, err := config.LoadSweepSchedule()
			if err != nil {
				return err
			}

			var m *metrics.Metrics
			if cfg.MetricsAddr != "" {
				m = metrics.New()
			}

			ghApp, err := newApp(cfg)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			previewStatus := statuskube.InCluster(ctx)
			namespaces := namespace.InCluster(ctx)
			if err = loadStatus(ctx, previewStatus); err != nil {
				return err
			}
			wake := app.NewWake()
			st, f, err := newStoreAndForge(ctx, ghApp, cfg,
				append(statusHooks(previewStatus, wake), store.WithMetrics(m))...)
			if err != nil {
				return err
			}
			defer closeStore(ctx, st)
			previewStatus.Start(ctx, st)

			a := app.New(app.Config{
				Store: st, Forge: f, Metrics: m, Namespaces: namespaces, Wake: wake, Status: previewStatus,
			})
			srv := server.NewServer(a, f, f, cfg.DeployToken,
				server.WithMetrics(m),
				server.WithStatusToken(cfg.StatusToken),
				server.WithReadToken(cfg.ReadToken), server.WithPreviewTTL(ttl))
			catchUp := a.CatchUp()
			httpSrv := newHTTPServer(cfg.ListenAddr, srv)

			if cfg.DeployToken == "" {
				slog.InfoContext(ctx, "deploy API disabled", slog.String("reason", "BADIL_DEPLOY_TOKEN is unset"))
			}
			if m != nil {
				app.CountPreviews(ctx, st, m)
			}
			serveInBackground(ctx, "server", httpSrv)
			metricsSrv := startMetricsServer(ctx, cfg.MetricsAddr, m)
			a.Go(func() { app.RunCatchUp(ctx, catchUp) })
			a.StartSweepLoop(ctx, app.SweepAPI(st, f, m), catchUp, m, interval, ttl)

			<-ctx.Done()
			slog.InfoContext(ctx, "shutting down server")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			shutdownErr := shutdownServers(shutdownCtx, httpSrv, metricsSrv)
			if shutdownErr == nil {
				shutdownErr = a.WaitBackground(shutdownCtx)
			}
			previewStatus.Close(shutdownCtx)
			return shutdownErr
		},
	}
}

// loadStatus reads the previews' status back into s (status.Store.Load),
// before the server listens. A failed read is returned.
func loadStatus(ctx context.Context, s *status.Store) error {
	if err := s.Load(ctx); err != nil {
		return fmt.Errorf("loading preview status: %w", err)
	}
	return nil
}

// statusHooks returns the store options that keep s in step with the gitops
// repo: the status of the previews a landed push deleted is deleted, and
// every fetch that moves the head collects the status of the previews no
// longer there. Both wake the waits through wake.
func statusHooks(s *status.Store, wake *app.Wake) []store.Option {
	return []store.Option{
		store.WithApplied(app.AppliedHook(s, wake)),
		store.WithHeadMoved(app.HeadMovedHook(s, wake)),
	}
}

// newHTTPServer returns the HTTP server of srv on addr. When its shutdown
// starts, srv's waits end at once (server.Server.EndWaits), so that shutdown
// does not wait for their long polls.
func newHTTPServer(addr string, srv *server.Server) *http.Server {
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	httpSrv.RegisterOnShutdown(srv.EndWaits)
	return httpSrv
}

// startMetricsServer serves GET /metrics from m on addr in the background and
// returns its server. It starts nothing and returns nil when m is nil.
func startMetricsServer(ctx context.Context, addr string, m *metrics.Metrics) *http.Server {
	if m == nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	metricsSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	serveInBackground(ctx, "metrics server", metricsSrv)
	return metricsSrv
}

// serveInBackground runs srv.ListenAndServe in the background, logging it as
// name, and exits the process when it fails for any reason but shutdown.
func serveInBackground(ctx context.Context, name string, srv *http.Server) {
	logger := slog.Default()
	go func() {
		logger.InfoContext(ctx, "starting "+name, "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.ErrorContext(ctx, name+" error", "err", err)
			os.Exit(1)
		}
	}()
}

// shutdownServers shuts every non-nil server down concurrently, within ctx,
// and returns their joined errors.
func shutdownServers(ctx context.Context, servers ...*http.Server) error {
	errs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		if srv == nil {
			continue
		}
		wg.Go(func() {
			errs[i] = srv.Shutdown(ctx)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// newStoreAndForge builds the gitops repo store and the GitHub forge from
// cfg (newForge). One installation transport of the GitHub App backs both the
// API client and the store's git credentials, so its cached installation
// token is shared. The store commits as the App's bot
// (github.CommitIdentity), matches pr-urls with the forge's rule, clones the
// gitops repo cfg.GitOpsRepoURL into cfg.GitOpsCloneDir with the credentials
// of github.GitopsCredentials and is configured by opts; an identity that
// can't be resolved or a failed clone is an error.
func newStoreAndForge(
	ctx context.Context, app *github.App, cfg *config.Config, opts ...store.Option,
) (*store.GitHubStore, *forgegithub.Forge, error) {
	itr, f, identity, err := newForge(ctx, app, cfg)
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(ctx, store.CloneConfig{
		URL:         cfg.GitOpsRepoURL,
		Dir:         cfg.GitOpsCloneDir,
		Credentials: github.GitopsCredentials(cfg.GitOpsRepoUsername, cfg.GitOpsRepoToken, itr),
	}, identity, opts...)
	if err != nil {
		return nil, nil, err
	}
	return st, f, nil
}

// newForge returns the transport of the installation cfg.InstallationID of
// app, the GitHub forge of cfg.GitHubURL over a client on that transport, and
// the App's bot as the gitops commit identity (github.CommitIdentity). Every
// client is on app's API base (github.App.Client).
func newForge(
	ctx context.Context, app *github.App, cfg *config.Config,
) (*ghinstallation.Transport, *forgegithub.Forge, store.CommitIdentity, error) {
	itr := app.InstallationTransport(cfg.InstallationID)
	client := app.Client(itr)
	id, err := github.CommitIdentity(ctx, app.Client(app.AppsTransport()), client, cfg.GitHubURL)
	if err != nil {
		return nil, nil, store.CommitIdentity{}, fmt.Errorf("gitops commit identity: %w", err)
	}
	f, err := forgegithub.New(client, app, cfg.InstallationID, cfg.GitHubURL)
	if err != nil {
		return nil, nil, store.CommitIdentity{}, err
	}
	return itr, f, store.CommitIdentity(id), nil
}

// closeStore removes the store's clone directory when it made one, logging a
// failure.
func closeStore(ctx context.Context, st *store.GitHubStore) {
	if err := st.Close(); err != nil {
		slog.Default().WarnContext(ctx, "removing the gitops clone failed", slog.String("err", err.Error()))
	}
}

// newApp builds the GitHub App from the loaded config.
func newApp(cfg *config.Config) (*github.App, error) {
	app, err := github.NewApp(github.AppConfig{
		AppID:         cfg.AppID,
		PrivateKey:    cfg.PrivateKey,
		WebhookSecret: cfg.WebhookSecret,
		APIURL:        cfg.GitHubAPIURL,
	})
	if err != nil {
		return nil, fmt.Errorf("creating github app: %w", err)
	}
	return app, nil
}

// cleanupCmd runs one sweep over the gitops repo's previews: previews with no
// pin are deleted, previews idle for longer than CLEANUP_TTL (when set) are
// deleted, and pins of closed PRs are torn down. A catch-up follows it
// (app.RunCleanup).
func cleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cleanup",
		Short: "Delete stale and expired preview environments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			ttl, err := config.LoadCleanupTTL()
			if err != nil {
				return err
			}
			ghApp, err := newApp(cfg)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), app.CleanupTimeout)
			defer cancel()
			st, f, err := newStoreAndForge(ctx, ghApp, cfg)
			if err != nil {
				return err
			}
			defer closeStore(ctx, st)
			catchUp := app.CleanupCatchUp(st, f, namespace.InCluster(ctx))
			return app.RunCleanup(cmd.Context(), ctx, app.SweepAPI(st, f, nil), catchUp, time.Now().UTC(), ttl)
		},
	}
}

// verifyAppCmd hits GET /app with App-JWT auth using GITHUB_APP_ID and the
// PEM at GITHUB_PRIVATE_KEY_PATH (or inline GITHUB_PRIVATE_KEY), on the API
// base of GITHUB_API_URL (config.LoadGitHubURLs). On success
// it prints the App's identity and lists every installation reachable with
// these credentials — the installation IDs needed for GITHUB_INSTALLATION_ID.
//
// Intended for operator use after creating or rotating a GitHub App: confirms
// that the (App ID, private key) pair is valid before standing up the server.
func verifyAppCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify-app",
		Short: "Verify GitHub App credentials and list installations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			appID, err := config.LoadAppID()
			if err != nil {
				return err
			}
			pem, err := config.LoadPrivateKey()
			if err != nil {
				return err
			}
			_, apiURL, err := config.LoadGitHubURLs()
			if err != nil {
				return err
			}

			ghApp, err := github.NewApp(github.AppConfig{AppID: appID, PrivateKey: pem, APIURL: apiURL})
			if err != nil {
				return fmt.Errorf("loading PEM: %w", err)
			}
			return github.VerifyApp(cmd.Context(), ghApp, cmd.OutOrStdout())
		},
	}
}
