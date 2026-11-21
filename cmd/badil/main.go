package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v71/github"
	"github.com/spf13/cobra"

	"github.com/badilhq/badil/internal/config"
	"github.com/badilhq/badil/internal/forge"
	forgegithub "github.com/badilhq/badil/internal/forge/github"
	"github.com/badilhq/badil/internal/github"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/server"
	"github.com/badilhq/badil/internal/status"
	"github.com/badilhq/badil/internal/store"
)

// shutdownTimeout bounds graceful shutdown: draining in-flight requests and
// then background teardowns share it, inside the default 30s pod grace period.
const shutdownTimeout = 25 * time.Second

// cleanupTimeout bounds one cleanup sweep over every preview.
const cleanupTimeout = 10 * time.Minute

// readHeaderTimeout bounds reading a request's headers on every listener.
const readHeaderTimeout = 10 * time.Second

// minCleanupInterval is the shortest CLEANUP_INTERVAL accepted.
const minCleanupInterval = time.Minute

// previewsCountTimeout bounds the startup listing of the previews.
const previewsCountTimeout = 10 * time.Second

// sweepAPI is st, f and the fan-out with the metrics recorder lifecycle.Sweep
// expects. A nil *metrics.Metrics records nothing.
func sweepAPI(st *store.GitHubStore, f forge.Forge, m *metrics.Metrics) lifecycle.SweepAPI {
	return lifecycle.SweepAPI{Store: st, Forge: f, Recorder: m, FanOut: lifecycle.FanOutPRComments}
}

// reconcileAPI is st, f and the fan-out with the metrics recorder
// lifecycle.ReconcilePR expects. A nil *metrics.Metrics records nothing.
func reconcileAPI(st *store.GitHubStore, f forge.Forge, m *metrics.Metrics) lifecycle.ReconcileAPI {
	return lifecycle.ReconcileAPI{Store: st, Forge: f, Recorder: m, FanOut: lifecycle.FanOutPRComments}
}

// catchUpAPI is st, f and the metrics recorder lifecycle.CatchUp expects. A
// nil *metrics.Metrics records nothing.
func catchUpAPI(st *store.GitHubStore, f forge.Forge, m *metrics.Metrics) lifecycle.CatchUpAPI {
	return lifecycle.CatchUpAPI{Store: st, Forge: f, Recorder: m}
}

// catchUpRunner runs one catch-up: *lifecycle.CatchUp.
type catchUpRunner interface {
	Run(ctx context.Context)
}

// runCatchUp runs one catch-up of c, bounded by cleanupTimeout and cancelled
// with ctx.
func runCatchUp(ctx context.Context, c catchUpRunner) {
	cctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	c.Run(cctx)
}

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

			interval, ttl, err := sweepSchedule()
			if err != nil {
				return err
			}

			var m *metrics.Metrics
			if cfg.MetricsAddr != "" {
				m = metrics.New()
			}

			app, err := newApp(cfg)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			previewStatus := status.InCluster(ctx)
			if err = loadStatus(ctx, previewStatus); err != nil {
				return err
			}
			wake := server.NewWake()
			st, f, err := newStoreAndForge(ctx, app, cfg,
				append(statusHooks(previewStatus, wake), store.WithMetrics(m))...)
			if err != nil {
				return err
			}
			defer closeStore(ctx, st)
			previewStatus.Start(ctx, st)

			srv := server.NewServer(f, st, cfg.DeployToken,
				server.WithMetrics(m), server.WithStatus(cfg.StatusToken, previewStatus),
				server.WithReadToken(cfg.ReadToken), server.WithPreviewTTL(ttl), server.WithWake(wake))
			catchUpCfg := catchUpAPI(st, f, m)
			catchUpCfg.Skipped = srv.RecordCatchUpSkip
			catchUp := lifecycle.NewCatchUp(catchUpCfg, srv.QueueReconcile)
			httpSrv := newHTTPServer(cfg.ListenAddr, srv)

			if cfg.DeployToken == "" {
				slog.InfoContext(ctx, "deploy API disabled", slog.String("reason", "BADIL_DEPLOY_TOKEN is unset"))
			}
			countPreviews(ctx, st, m)
			serveInBackground(ctx, "server", httpSrv)
			metricsSrv := startMetricsServer(ctx, cfg.MetricsAddr, m)
			srv.Go(func() { runCatchUp(ctx, catchUp) })
			startSweepLoop(ctx, srv, sweepAPI(st, f, m), catchUp, m, interval, ttl)

			<-ctx.Done()
			slog.InfoContext(ctx, "shutting down server")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			shutdownErr := shutdownServers(shutdownCtx, httpSrv, metricsSrv)
			if shutdownErr == nil {
				shutdownErr = srv.WaitBackground(shutdownCtx)
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
func statusHooks(s *status.Store, wake *server.Wake) []store.Option {
	return []store.Option{
		store.WithApplied(func(res store.Result) {
			s.Delete(res.Deleted...)
			wake.Notify()
		}),
		store.WithHeadMoved(func() {
			s.CollectGarbage()
			wake.Notify()
		}),
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

// previewLister lists the previews of the gitops repo.
type previewLister interface {
	ListPreviews(ctx context.Context) ([]string, error)
}

// countPreviews sets m's previews gauge from one listing of the gitops repo,
// bounded by previewsCountTimeout. serve calls it before the server accepts
// requests, so no create or delete races the listing. It lists nothing when m
// is nil (metrics disabled). A failed or timed-out listing is logged and
// leaves the gauge as it is.
func countPreviews(ctx context.Context, api previewLister, m *metrics.Metrics) {
	if m == nil {
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, previewsCountTimeout)
	defer cancel()
	slugs, err := api.ListPreviews(listCtx)
	if err != nil {
		slog.Default().WarnContext(ctx, "previews count failed", slog.String("err", err.Error()))
		return
	}
	m.PreviewsListed(len(slugs))
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

// sweepSchedule reads CLEANUP_INTERVAL and, when it is set, CLEANUP_TTL: the
// in-server sweep's interval (0 when off) and TTL.
func sweepSchedule() (time.Duration, time.Duration, error) {
	interval, err := parseCleanupInterval(os.Getenv("CLEANUP_INTERVAL"))
	if err != nil || interval == 0 {
		return 0, 0, err
	}
	ttl, err := parseCleanupTTL(os.Getenv("CLEANUP_TTL"))
	if err != nil {
		return 0, 0, err
	}
	return interval, ttl, nil
}

// startSweepLoop runs runSweepLoop in the background of srv, so shutdown
// waits for it. It starts nothing when interval is 0.
func startSweepLoop(
	ctx context.Context,
	srv *server.Server,
	api lifecycle.SweepAPI,
	catchUp catchUpRunner,
	m *metrics.Metrics,
	interval, ttl time.Duration,
) {
	if interval == 0 {
		return
	}
	slog.Default().InfoContext(ctx, "starting cleanup loop",
		slog.String("interval", interval.String()),
		slog.String("ttl", ttl.String()),
	)
	srv.Go(func() {
		runSweepLoop(ctx, api, catchUp, m, interval, ttl, time.Now)
	})
}

// runSweepLoop sweeps every interval until ctx is done, starting one interval
// from now, and runs a catch-up after each sweep, whether it succeeded or
// failed, unless ctx is done. Runs never overlap: a tick that comes during a
// run waits for it. Each sweep and each catch-up is bounded by cleanupTimeout
// and cancelled with ctx.
func runSweepLoop(
	ctx context.Context,
	api lifecycle.SweepAPI,
	catchUp catchUpRunner,
	m *metrics.Metrics,
	interval, ttl time.Duration,
	now func() time.Time,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		runSweep(ctx, api, m, ttl, now)
		if ctx.Err() != nil {
			return
		}
		runCatchUp(ctx, catchUp)
	}
}

// runSweep runs one sweep and records it, unless ctx was done: a sweep
// cancelled by shutdown is not recorded, one that hit cleanupTimeout is
// recorded as failed.
func runSweep(
	ctx context.Context,
	api lifecycle.SweepAPI,
	m *metrics.Metrics,
	ttl time.Duration,
	now func() time.Time,
) {
	sctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	err := lifecycle.Sweep(sctx, api, now().UTC(), ttl)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "cleanup failed", slog.String("err", err.Error()))
	}
	m.SweepRun(err == nil, now())
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
// token is shared. The store commits as the App's bot (commitIdentity),
// matches pr-urls with the forge's rule, clones the gitops repo
// cfg.GitOpsRepoURL into cfg.GitOpsCloneDir with the credentials of
// gitopsCredentials and is configured by opts; an identity that can't be
// resolved or a failed clone is an error.
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
		Credentials: gitopsCredentials(cfg, itr),
	}, identity, f.ParsePRURL, opts...)
	if err != nil {
		return nil, nil, err
	}
	return st, f, nil
}

// newForge returns the transport of the installation cfg.InstallationID of
// app, the GitHub forge of cfg.GitHubURL over a client on that transport, and
// the App's bot as the gitops commit identity (commitIdentity). Every client
// is on app's API base (github.App.Client).
func newForge(
	ctx context.Context, app *github.App, cfg *config.Config,
) (*ghinstallation.Transport, *forgegithub.Forge, store.CommitIdentity, error) {
	itr := app.InstallationTransport(cfg.InstallationID)
	client := app.Client(itr)
	identity, err := commitIdentity(ctx, app.Client(app.AppsTransport()), client, cfg.GitHubURL)
	if err != nil {
		return nil, nil, store.CommitIdentity{}, fmt.Errorf("gitops commit identity: %w", err)
	}
	f, err := forgegithub.New(client, app, cfg.InstallationID, cfg.GitHubURL)
	if err != nil {
		return nil, nil, store.CommitIdentity{}, err
	}
	return itr, f, identity, nil
}

// commitIdentity returns the GitHub App's bot as the author and committer of
// the gitops commits: name <slug>[bot] and email
// <id>+<slug>[bot]@users.noreply.<host>, where host is the host name of
// githubURL (GITHUB_URL) without its port, the slug comes from GET /app with
// appClient (the App JWT) and the bot user id from GET /users/<slug>[bot]
// with installClient (the installation).
func commitIdentity(
	ctx context.Context, appClient, installClient *gh.Client, githubURL string,
) (store.CommitIdentity, error) {
	web, err := url.Parse(githubURL)
	if err != nil {
		return store.CommitIdentity{}, fmt.Errorf("GITHUB_URL: %w", err)
	}
	app, _, err := appClient.Apps.Get(ctx, "")
	if err != nil {
		return store.CommitIdentity{}, fmt.Errorf("GET /app: %w", err)
	}
	if app.GetSlug() == "" {
		return store.CommitIdentity{}, errors.New("GET /app: no slug")
	}
	login := app.GetSlug() + "[bot]"
	user, _, err := installClient.Users.Get(ctx, login)
	if err != nil {
		return store.CommitIdentity{}, fmt.Errorf("GET /users/%s: %w", login, err)
	}
	if user.GetID() == 0 {
		return store.CommitIdentity{}, fmt.Errorf("GET /users/%s: no id", login)
	}
	return store.CommitIdentity{
		Name:  login,
		Email: fmt.Sprintf("%d+%s@users.noreply.%s", user.GetID(), login, web.Hostname()),
	}, nil
}

// defaultGitUsername is the basic-auth username of an installation token, and
// of GITOPS_REPO_TOKEN when GITOPS_REPO_USERNAME is unset.
const defaultGitUsername = "x-access-token"

// gitopsCredentials authenticates the store's git calls with
// cfg.GitOpsRepoToken, as cfg.GitOpsRepoUsername or defaultGitUsername, when
// the token is set, and as the installation of itr (gitCredentials)
// otherwise.
func gitopsCredentials(cfg *config.Config, itr *ghinstallation.Transport) store.CredentialProvider {
	if cfg.GitOpsRepoToken == "" {
		return gitCredentials(itr)
	}
	user := cmp.Or(cfg.GitOpsRepoUsername, defaultGitUsername)
	return func(context.Context) (string, string, error) {
		return user, cfg.GitOpsRepoToken, nil
	}
}

// gitCredentials authenticates the store's git calls as the installation of
// itr: user x-access-token, with the installation token itr caches and
// refreshes before it expires.
func gitCredentials(itr *ghinstallation.Transport) store.CredentialProvider {
	return func(ctx context.Context) (string, string, error) {
		token, err := itr.Token(ctx)
		return defaultGitUsername, token, err
	}
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
// (runCleanup).
func cleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cleanup",
		Short: "Delete stale and expired preview environments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			ttl, err := parseCleanupTTL(os.Getenv("CLEANUP_TTL"))
			if err != nil {
				return err
			}
			app, err := newApp(cfg)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cleanupTimeout)
			defer cancel()
			st, f, err := newStoreAndForge(ctx, app, cfg)
			if err != nil {
				return err
			}
			defer closeStore(ctx, st)
			catchUp := lifecycle.NewCatchUp(catchUpAPI(st, f, nil), reconcileNow(reconcileAPI(st, f, nil)))
			return runCleanup(cmd.Context(), ctx, sweepAPI(st, f, nil), catchUp, time.Now().UTC(), ttl)
		},
	}
}

// runCleanup runs one sweep under sweepCtx, then one catch-up of c under its
// own cleanupTimeout from ctx (runCatchUp), whether the sweep succeeded or
// failed. It returns the sweep's error: catch-up's failures are only logged
// and counted.
func runCleanup(
	ctx, sweepCtx context.Context, api lifecycle.SweepAPI, c catchUpRunner, now time.Time, ttl time.Duration,
) error {
	err := lifecycle.Sweep(sweepCtx, api, now, ttl)
	runCatchUp(ctx, c)
	return err
}

// reconcileNow reconciles through lifecycle.ReconcilePR over api at once,
// each reconcile capped at server.ReconcileTimeout, and calls done with its
// result.
func reconcileNow(api lifecycle.ReconcileAPI) lifecycle.ReconcileFunc {
	return func(ctx context.Context, ev lifecycle.Event, done func(lifecycle.ReconcileResult)) {
		rctx, cancel := context.WithTimeout(ctx, server.ReconcileTimeout)
		defer cancel()
		done(lifecycle.ReconcilePR(rctx, api, ev))
	}
}

// parseCleanupInterval parses CLEANUP_INTERVAL. Empty means no in-server
// sweep (0); otherwise it must be a Go duration of at least
// minCleanupInterval.
func parseCleanupInterval(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval < minCleanupInterval {
		return 0, fmt.Errorf("CLEANUP_INTERVAL must be a duration of at least 1m, got %q", raw)
	}
	return interval, nil
}

// parseCleanupTTL parses CLEANUP_TTL. Empty means no expiry (0); otherwise it
// must be a positive Go duration.
func parseCleanupTTL(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl <= 0 {
		return 0, fmt.Errorf("CLEANUP_TTL must be a positive duration, got %q", raw)
	}
	return ttl, nil
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
			appIDStr := os.Getenv("GITHUB_APP_ID")
			if appIDStr == "" {
				return errors.New("GITHUB_APP_ID is required")
			}
			appID, err := strconv.ParseInt(appIDStr, 10, 64)
			if err != nil {
				return fmt.Errorf("GITHUB_APP_ID must be numeric: %w", err)
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
			client := ghApp.Client(ghApp.AppsTransport())

			app, _, err := client.Apps.Get(cmd.Context(), "")
			if err != nil {
				return fmt.Errorf("GET /app: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "App credentials OK")
			fmt.Fprintf(cmd.OutOrStdout(), "  id:    %d\n", app.GetID())
			fmt.Fprintf(cmd.OutOrStdout(), "  slug:  %s\n", app.GetSlug())
			fmt.Fprintf(cmd.OutOrStdout(), "  name:  %s\n", app.GetName())
			fmt.Fprintf(cmd.OutOrStdout(), "  owner: %s\n", app.GetOwner().GetLogin())
			fmt.Fprintf(cmd.OutOrStdout(), "  url:   %s\n", app.GetHTMLURL())

			insts, _, err := client.Apps.ListInstallations(cmd.Context(), nil)
			if err != nil {
				return fmt.Errorf("list installations: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nInstallations (%d):\n", len(insts))
			if len(insts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "  (none — install the App on an account before running `badil serve`)")
			}
			for _, ins := range insts {
				fmt.Fprintf(cmd.OutOrStdout(), "  - id=%d  account=%s (%s)\n",
					ins.GetID(), ins.GetAccount().GetLogin(), ins.GetAccount().GetType())
			}
			return nil
		},
	}
}
