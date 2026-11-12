package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// healthzPath is the liveness probe path, which accessLog does not log.
const healthzPath = "/healthz"

// accessLog emits one structured log line per request: method, path, status,
// bytes, and duration. Requests to healthzPath are not logged: probes would
// flood the log. It reuses chi's WrapResponseWriter to capture the
// status code the downstream handler sets (the part that can't be observed
// from a plain http.ResponseWriter) and chi's request ID for correlation.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthzPath {
			next.ServeHTTP(w, r)
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()

		next.ServeHTTP(ww, r)

		slog.InfoContext(r.Context(), "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", ww.Status()),
			slog.Int("bytes", ww.BytesWritten()),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("remote_addr", r.RemoteAddr),
		)
	})
}
