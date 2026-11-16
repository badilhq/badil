package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

// SyncRequest is the optional body of POST /v1/sync. Without a slug, every
// preview is synced.
type SyncRequest struct {
	Slug *string `json:"slug"`
}

// SyncResponse is the success body of POST /v1/sync: the slugs rewritten, the
// number of previews left unchanged and the slugs that failed.
type SyncResponse struct {
	Synced    []string `json:"synced"`
	Unchanged int      `json:"unchanged"`
	Failed    []string `json:"failed"`
}

// handleSync syncs every preview, or the one the body names, with badil.yaml,
// waiting for the config sync guard, and answers with what it rewrote. A slug
// that is empty or not its own Slugify form is refused before any gitops read.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req SyncRequest
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

	report, err := s.syncer.Sync(ctx, slug)
	if err != nil {
		status := http.StatusInternalServerError
		msg := err.Error()
		switch {
		case errors.Is(err, lifecycle.ErrPreviewNotFound):
			status, msg = http.StatusNotFound, fmt.Sprintf("preview %s not found", slug)
		case errors.Is(err, lifecycle.ErrConfigUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, gitops.ErrUnauthorized):
			status = http.StatusBadGateway
		}
		slog.WarnContext(ctx, "sync failed", //nolint:sloglint // server has no scoped logger yet
			slog.String("slug", slug),
			slog.Int("status", status),
			slog.String("err", err.Error()),
		)
		writeJSONError(ctx, w, status, msg)
		return
	}
	resp := SyncResponse{Synced: report.Synced, Unchanged: report.Unchanged, Failed: report.Failed}
	if resp.Synced == nil {
		resp.Synced = []string{}
	}
	if resp.Failed == nil {
		resp.Failed = []string{}
	}
	writeJSON(ctx, w, http.StatusOK, resp)
}
