package lifecycle

import (
	"context"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
)

// RefusedByHolder exports refusedByHolder to the external tests.
func RefusedByHolder(
	ctx context.Context,
	api ReconcileAPI,
	ev Event,
	pr *forge.PullRequest,
	t preview.Resolution,
	services []string,
	label string,
) (string, string, *preview.PreviewValues) {
	return refusedByHolder(ctx, api, ev, pr, t, services, label)
}
