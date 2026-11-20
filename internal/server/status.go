package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/status"
	"github.com/badilhq/badil/internal/store"
)

const (
	// statusPath is the route of the renderer's status callback.
	statusPath = "/webhooks/status"
	// maxStatusEventBytes caps the body of POST /webhooks/status.
	maxStatusEventBytes = 64 << 10
	// maxSpinUp is the longest spin-up recorded.
	maxSpinUp = 24 * time.Hour
)

// Results of a status event, the result label of badil_status_events_total.
const (
	statusRecorded = "recorded"
	statusIgnored  = "ignored"
	statusError    = "error"
)

// Reasons a valid status event is ignored.
const (
	reasonUnknownPreview = "unknown_preview"
	reasonUnknownService = "unknown_service"
	reasonStale          = "stale"
)

// statusEvent is the body of POST /webhooks/status: the status a renderer
// reports for one service of a preview, at a revision.
type statusEvent struct {
	APIVersion string `json:"apiVersion"`
	Preview    string `json:"preview"`
	Service    string `json:"service"`
	Revision   string `json:"revision"`
	Status     string `json:"status"`
	At         string `json:"at"`
}

// parse validates e and returns its time.
func (e statusEvent) parse() (time.Time, error) {
	switch {
	case e.APIVersion != preview.APIVersion:
		return time.Time{}, errors.New("apiVersion must be " + preview.APIVersion)
	case e.Preview == "" || e.Service == "" || e.Revision == "":
		return time.Time{}, errors.New("preview, service and revision are required")
	case !status.Valid(e.Status):
		return time.Time{}, errors.New("status must be ready, progressing, degraded or sync-failed")
	}
	at, err := time.Parse(time.RFC3339, e.At)
	if err != nil {
		return time.Time{}, errors.New("at must be an RFC 3339 time")
	}
	return at, nil
}

// handleStatusEvent records the status a renderer reports for a preview
// service. It answers 204 to every valid event, recorded or ignored, 400 to
// an invalid or oversize body, and 503, recording nothing, when the preview
// can't be read.
func (s *Server) handleStatusEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStatusEventBytes))
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	var e statusEvent
	if err = json.Unmarshal(body, &e); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	at, err := e.parse()
	if err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	attrs := []any{
		slog.String("preview", e.Preview),
		slog.String("service", e.Service),
		slog.String("revision", e.Revision),
		slog.String("status", e.Status),
	}
	vals, _, err := s.store.GetPreview(ctx, e.Preview)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.metrics.StatusEvent(e.Status, statusError)
		slog.Default().ErrorContext(ctx, "status event",
			append(attrs, slog.String("result", statusError), slog.String("err", err.Error()))...)
		http.Error(w, "gitops repo unavailable", http.StatusServiceUnavailable)
		return
	}

	reason := ""
	var pinned *preview.ServiceEntry
	switch {
	case err != nil:
		reason = reasonUnknownPreview
	default:
		var known bool
		pinned, known = findService(vals, e.Service)
		if !known {
			reason = reasonUnknownService
		}
	}
	if reason == "" {
		recorded, firstReady := s.status.Record(e.Preview, e.Service,
			status.Entry{Revision: e.Revision, Status: e.Status, At: at})
		if !recorded {
			reason = reasonStale
		} else if firstReady {
			if d, ok := spinUp(pinned, e.Revision, at); ok {
				s.metrics.SpinUp(e.Service, d)
				attrs = append(attrs, slog.Float64("spinup_seconds", d.Seconds()))
			}
		}
	}

	if reason != "" {
		s.metrics.StatusEvent(e.Status, statusIgnored)
		attrs = append(attrs, slog.String("result", statusIgnored), slog.String("reason", reason))
	} else {
		s.metrics.StatusEvent(e.Status, statusRecorded)
		attrs = append(attrs, slog.String("result", statusRecorded))
	}
	slog.Default().InfoContext(ctx, "status event", attrs...)
	w.WriteHeader(http.StatusNoContent)
}

// findService reports whether vals has a service or a dependency named name,
// and returns its service entry when it is a service.
func findService(vals *preview.PreviewValues, name string) (*preview.ServiceEntry, bool) {
	for i := range vals.Services {
		if vals.Services[i].Name == name {
			return &vals.Services[i], true
		}
	}
	for _, dep := range vals.Dependencies {
		if dep.Name == name {
			return nil, true
		}
	}
	return nil, false
}

// spinUp returns the time from the gitops write of the pinned service entry
// to at, when the entry is pinned, revision starts with its commitSha, and
// the time is within [0, maxSpinUp].
func spinUp(entry *preview.ServiceEntry, revision string, at time.Time) (time.Duration, bool) {
	if entry == nil || entry.CommitSha == "" || entry.Metadata == nil ||
		!strings.HasPrefix(revision, entry.CommitSha) {
		return 0, false
	}
	updatedAt, err := time.Parse(time.RFC3339, entry.Metadata.UpdatedAt)
	if err != nil {
		return 0, false
	}
	d := at.Sub(updatedAt)
	if d < 0 || d > maxSpinUp {
		return 0, false
	}
	return d, true
}
