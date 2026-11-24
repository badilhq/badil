package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

// maxSpinUp is the longest spin-up recorded.
const maxSpinUp = 24 * time.Hour

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

// StatusEvent is the status a renderer reports for one service of a
// preview, at a revision, and when.
type StatusEvent struct {
	Preview, Service, Revision, Status string
	At                                 time.Time
}

// HasStatus reports whether the app has a status repository (Config.Status).
func (a *App) HasStatus() bool {
	return a.status != nil
}

// RecordStatus records the status a renderer reports for a preview service,
// and wakes the waits when it is recorded. The app must have a status
// repository (HasStatus). An event of an unknown preview or service, or one
// older than the recorded status, is ignored. Each event is counted and
// logged with its result. When the preview can't be read, nothing is
// recorded and the read error is returned.
func (a *App) RecordStatus(ctx context.Context, e StatusEvent) error {
	attrs := []any{
		slog.String("preview", e.Preview),
		slog.String("service", e.Service),
		slog.String("revision", e.Revision),
		slog.String("status", e.Status),
	}
	vals, _, err := a.store.GetPreview(ctx, e.Preview)
	if err != nil && !errors.Is(err, lifecycle.ErrNotFound) {
		a.metrics.StatusEvent(e.Status, statusError)
		slog.Default().ErrorContext(ctx, "status event",
			append(attrs, slog.String("result", statusError), slog.String("err", err.Error()))...)
		return err
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
		recorded, firstReady := a.status.Record(e.Preview, e.Service,
			preview.ServiceStatus{Revision: e.Revision, Status: e.Status, At: e.At})
		switch {
		case !recorded:
			reason = reasonStale
		case a.previewGone(ctx, e.Preview):
			reason = reasonUnknownPreview
		case firstReady:
			if d, ok := spinUp(pinned, e.Revision, e.At); ok {
				a.metrics.SpinUp(e.Service, d)
				attrs = append(attrs, slog.Float64("spinup_seconds", d.Seconds()))
			}
		}
		if recorded {
			a.Wake().Notify()
		}
	}

	if reason != "" {
		a.metrics.StatusEvent(e.Status, statusIgnored)
		attrs = append(attrs, slog.String("result", statusIgnored), slog.String("reason", reason))
	} else {
		a.metrics.StatusEvent(e.Status, statusRecorded)
		attrs = append(attrs, slog.String("result", statusRecorded))
	}
	slog.Default().InfoContext(ctx, "status event", attrs...)
	return nil
}

// previewGone reads the preview slug again, after an event of it was
// recorded, and reports whether its file is gone. A deletion that landed
// between the first read and the record would otherwise bring the status
// back: the store's WithApplied Delete may already have run, and the garbage
// collection keeps a preview recorded after its listing started. When the
// preview is gone, its status is deleted. When the read fails, the status is
// kept, the failure logged, and a garbage collection asked for: it removes
// the status if the preview is gone.
func (a *App) previewGone(ctx context.Context, slug string) bool {
	_, _, err := a.store.GetPreview(ctx, slug)
	switch {
	case err == nil:
		return false
	case errors.Is(err, lifecycle.ErrNotFound):
		a.status.Delete(slug)
		return true
	default:
		slog.Default().WarnContext(ctx, "status event preview re-read failed",
			slog.String("preview", slug), slog.String("err", err.Error()))
		a.status.CollectGarbage()
		return false
	}
}

// findService reports whether vals has a service or a dependency named name,
// and returns its service entry when it is a service.
func findService(vals *preview.Values, name string) (*preview.ServiceEntry, bool) {
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
	if entry == nil || entry.Metadata == nil || !preview.AtPin(revision, entry.CommitSha) {
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
