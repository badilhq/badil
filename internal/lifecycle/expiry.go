package lifecycle

import (
	"context"
	"log/slog"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
)

// ExpiryStore remembers the open PRs of the previews the sweep deleted for
// the TTL, each at the head SHA it had then, so that catch-up does not deploy
// them again until they get a new head or are deployed by a label event or
// /v1/deploy. Keys are PRKey values. A nil ExpiryStore in an API struct
// remembers nothing.
type ExpiryStore interface {
	// Mark records key (a PRKey) as expired at headSHA, replacing any record of key.
	Mark(ctx context.Context, key, prURL, headSHA string, at time.Time) error
	// Recorded returns the head SHA key is recorded at; ok is false when key has no record.
	Recorded(ctx context.Context, key string) (headSHA string, ok bool, err error)
	// Clear removes the record of key; a missing record is no error.
	Clear(ctx context.Context, key string) error
	// Prune removes every record whose key keep returns false for.
	Prune(ctx context.Context, keep func(key string) bool) error
}

// clearExpiry removes the record of the PR of t from e, a nil e doing
// nothing. A failure is logged as a warning.
func clearExpiry(ctx context.Context, e ExpiryStore, t Target) {
	if e == nil {
		return
	}
	if err := e.Clear(ctx, PRKey(t)); err != nil {
		slog.WarnContext(ctx, "expiry clear failed",
			slog.String("repo", t.FullName),
			slog.Int("pr_number", t.Number),
			slog.String("err", err.Error()),
		)
	}
}

// markExpired records in e, a nil e doing nothing, each PR pinned in the
// values vals of the preview slug that states (keyed by PRKey) holds as
// open, at its head SHA and at. A PR whose state was not read, or that is
// closed, is not marked. A failure is logged as a warning.
func markExpired(
	ctx context.Context, e ExpiryStore, parse preview.PRURLParser,
	slug string, vals preview.Values, states map[string]*forge.PullRequest, at time.Time,
) {
	if e == nil {
		return
	}
	for _, t := range pinnedPRs(parse, slug, vals) {
		pr, ok := states[PRKey(t)]
		if !ok || !pr.Open {
			continue
		}
		if err := e.Mark(ctx, PRKey(t), pr.URL, pr.HeadSHA, at); err != nil {
			slog.WarnContext(ctx, "expiry mark failed",
				slog.String("repo", t.FullName),
				slog.Int("pr_number", t.Number),
				slog.String("err", err.Error()),
			)
		}
	}
}
