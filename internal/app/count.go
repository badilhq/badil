package app

import (
	"context"
	"log/slog"
	"time"
)

// previewsCountTimeout bounds the startup listing of the previews.
const previewsCountTimeout = 10 * time.Second

// PreviewLister lists the previews of the gitops repo.
type PreviewLister interface {
	ListPreviews(ctx context.Context) ([]string, error)
}

// PreviewsRecorder records the number of previews listed: *metrics.Metrics.
type PreviewsRecorder interface {
	PreviewsListed(n int)
}

// CountPreviews records with rec the number of previews of one listing of the
// gitops repo by l, bounded by previewsCountTimeout. serve calls it before the
// server accepts requests, so no create or delete races the listing. It lists
// nothing when rec is nil (metrics disabled). A failed or timed-out listing is
// logged and records nothing.
func CountPreviews(ctx context.Context, l PreviewLister, rec PreviewsRecorder) {
	if rec == nil {
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, previewsCountTimeout)
	defer cancel()
	slugs, err := l.ListPreviews(listCtx)
	if err != nil {
		slog.Default().WarnContext(ctx, "previews count failed", slog.String("err", err.Error()))
		return
	}
	rec.PreviewsListed(len(slugs))
}
