package lifecycle

import (
	"context"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/preview"
)

// RefusedByHolder exports refusedByHolder to the external tests.
func RefusedByHolder(
	ctx context.Context,
	api ReconcileAPI,
	ev Event,
	pr *gitops.PullRequest,
	t preview.Resolution,
	services []string,
	label string,
) (string, string, bool) {
	return refusedByHolder(ctx, api, ev, pr, t, services, label)
}
