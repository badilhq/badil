package lifecycle

import "context"

// TeardownPRs exports teardownPRs to the external tests.
func TeardownPRs(ctx context.Context, api TeardownAPI, tt Target, slugs []string, reason string) {
	teardownPRs(ctx, api, tt, slugs, reason)
}
