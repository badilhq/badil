package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// readHeaderTimeout bounds reading a request's headers on every listener.
const readHeaderTimeout = 10 * time.Second

// NewHTTPServer returns the HTTP server of s on addr. When its shutdown
// starts, s's waits end at once (Server.EndWaits), so that shutdown does not
// wait for their long polls.
func NewHTTPServer(addr string, s *Server) *http.Server {
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	httpSrv.RegisterOnShutdown(s.EndWaits)
	return httpSrv
}

// StartMetricsServer serves GET /metrics from handler on addr in the
// background (ServeInBackground, with exit) and returns its server.
func StartMetricsServer(
	ctx context.Context, addr string, handler http.Handler, exit func(code int),
) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", handler)
	metricsSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	ServeInBackground(ctx, "metrics server", metricsSrv, exit)
	return metricsSrv
}

// ServeInBackground runs srv.ListenAndServe in the background, logging it as
// name, and calls exit(1) from that goroutine when it fails for any reason but
// shutdown. The process passes os.Exit.
func ServeInBackground(ctx context.Context, name string, srv *http.Server, exit func(code int)) {
	logger := slog.Default()
	go func() {
		logger.InfoContext(ctx, "starting "+name, "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.ErrorContext(ctx, name+" error", "err", err)
			exit(1)
		}
	}()
}

// ShutdownServers shuts every non-nil server down concurrently, within ctx,
// and returns their joined errors.
func ShutdownServers(ctx context.Context, servers ...*http.Server) error {
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
