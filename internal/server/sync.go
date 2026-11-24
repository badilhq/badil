package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/badilhq/badil/internal/api"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// handleSync syncs every preview, or the one the body names, with badil.yaml,
// waiting for the config sync guard, and answers with what it rewrote. A slug
// that is empty or not its own Slugify form is refused before any gitops read.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req api.SyncRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(ctx, w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	slug := ""
	if req.Slug != nil {
		slug = *req.Slug
		if slug == "" || slug != preview.Slugify(slug) {
			writeJSONError(ctx, w, http.StatusBadRequest, "invalid slug")
			return
		}
	}

	report, err := s.app.Sync(ctx, slug)
	if err != nil {
		status := http.StatusInternalServerError
		msg := err.Error()
		switch {
		case errors.Is(err, lifecycle.ErrPreviewNotFound):
			status, msg = http.StatusNotFound, fmt.Sprintf("preview %s not found", slug)
		case errors.Is(err, lifecycle.ErrConfigUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, store.ErrUnauthorized):
			status = http.StatusBadGateway
		}
		slog.WarnContext(ctx, "sync failed",
			slog.String("slug", slug),
			slog.Int("status", status),
			slog.String("err", err.Error()),
		)
		writeJSONError(ctx, w, status, msg)
		return
	}
	resp := api.SyncResponse{Synced: report.Synced, Unchanged: report.Unchanged, Failed: report.Failed}
	if resp.Synced == nil {
		resp.Synced = []string{}
	}
	if resp.Failed == nil {
		resp.Failed = []string{}
	}
	writeJSON(ctx, w, http.StatusOK, resp)
}
