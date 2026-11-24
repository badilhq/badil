package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/badilhq/badil/internal/api"
	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/preview"
)

const (
	// statusPath is the route of the renderer's status callback.
	statusPath = "/webhooks/status"
	// maxStatusEventBytes caps the body of POST /webhooks/status.
	maxStatusEventBytes = 64 << 10
)

// parseStatusEvent validates e and returns its time.
func parseStatusEvent(e api.StatusEvent) (time.Time, error) {
	switch {
	case e.APIVersion != preview.APIVersion:
		return time.Time{}, errors.New("apiVersion must be " + preview.APIVersion)
	case e.Preview == "" || e.Service == "" || e.Revision == "":
		return time.Time{}, errors.New("preview, service and revision are required")
	case !preview.ValidStatus(e.Status):
		return time.Time{}, errors.New("status must be ready, progressing, degraded or sync-failed")
	}
	at, err := time.Parse(time.RFC3339, e.At)
	if err != nil {
		return time.Time{}, errors.New("at must be an RFC 3339 time")
	}
	return at, nil
}

// handleStatusEvent records the status a renderer reports for a preview
// service (app.App.RecordStatus). It answers 204 to every valid event,
// recorded or ignored, 400 to an invalid or oversize body, and 503, recording
// nothing, when the preview can't be read.
func (s *Server) handleStatusEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStatusEventBytes))
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	var e api.StatusEvent
	if err = json.Unmarshal(body, &e); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	at, err := parseStatusEvent(e)
	if err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err = s.app.RecordStatus(ctx, app.StatusEvent{
		Preview: e.Preview, Service: e.Service, Revision: e.Revision, Status: e.Status, At: at,
	}); err != nil {
		http.Error(w, "gitops repo unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
