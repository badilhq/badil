// Command fakegithub serves a mock of the GitHub API badil calls, with a
// control API that changes its state and sends signed webhooks to badil. It
// is a dev tool for local and end-to-end testing, not part of the release.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/badilhq/badil/internal/fakegithub"
	"github.com/badilhq/badil/internal/github"
)

// secretEnv is the environment variable holding the webhook secret.
const secretEnv = "FAKEGITHUB_WEBHOOK_SECRET" //nolint:gosec // the variable's name, not a credential

// exitUsage is the exit code of invalid settings.
const exitUsage = 2

// shutdownGrace bounds the graceful shutdown.
const shutdownGrace = 10 * time.Second

// readHeaderTimeout bounds reading a request's headers.
const readHeaderTimeout = 10 * time.Second

// settings are the binary's flags and environment.
type settings struct {
	listen string
	server fakegithub.Config
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	s, err := parseSettings(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("invalid settings", slog.String("err", err.Error()))
		os.Exit(exitUsage)
	}
	if err = run(s); err != nil {
		slog.Error("fakegithub failed", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

// parseSettings reads the flags in args and the webhook secret from getenv.
// --web-url must pass github.ParseWebURL (GITHUB_URL's rules) and is kept as
// the canonical base it returns, without a trailing "/". Usage and flag
// errors are written to out.
func parseSettings(args []string, getenv func(string) string, out io.Writer) (settings, error) {
	var s settings
	fs := flag.NewFlagSet("fakegithub", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&s.listen, "listen", ":8090", "address to listen on")
	webURL := fs.String("web-url", "", "web base of every html_url and repo URL, badil's GITHUB_URL (required)")
	fs.StringVar(&s.server.WebhookURL, "webhook-url", "", "badil's webhook URL, .../webhooks/github (required)")
	fs.Int64Var(&s.server.InstallationID, "installation-id", 1, "installation ID, badil's GITHUB_INSTALLATION_ID")
	fs.StringVar(&s.server.AppSlug, "app-slug", "badil-mock", "slug of the App")
	fs.Int64Var(&s.server.BotUserID, "bot-user-id", 1, "user ID of the App's bot")
	if err := fs.Parse(args); err != nil {
		return settings{}, err
	}
	if fs.NArg() > 0 {
		return settings{}, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if *webURL == "" {
		return settings{}, errors.New("--web-url is required")
	}
	web, err := github.ParseWebURL(*webURL)
	if err != nil {
		return settings{}, fmt.Errorf("--web-url %w", err)
	}
	s.server.WebURL = web
	if s.server.WebhookURL == "" {
		return settings{}, errors.New("--webhook-url is required")
	}
	if _, err = github.ParseBaseURL(s.server.WebhookURL); err != nil {
		return settings{}, fmt.Errorf("--webhook-url %w", err)
	}
	if s.server.InstallationID < 1 || s.server.BotUserID < 1 {
		return settings{}, errors.New("--installation-id and --bot-user-id must be positive")
	}
	if s.server.AppSlug == "" {
		return settings{}, errors.New("--app-slug must not be empty")
	}
	s.server.WebhookSecret = getenv(secretEnv)
	if s.server.WebhookSecret == "" {
		return settings{}, errors.New(secretEnv + " is required")
	}
	return s, nil
}

// run serves the mock on s.listen until SIGINT or SIGTERM, then shuts down
// within shutdownGrace.
func run(s settings) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{
		Addr:              s.listen,
		Handler:           fakegithub.New(s.server).Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.InfoContext(ctx, "fakegithub listening",
		slog.String("addr", s.listen), slog.String("web_url", s.server.WebURL),
		slog.String("webhook_url", s.server.WebhookURL), slog.Int64("installation_id", s.server.InstallationID))
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.InfoContext(ctx, "shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
