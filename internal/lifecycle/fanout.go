package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
)

// FanOutFunc posts body on the open PRs of vals through f. FanOutPRComments
// is the one badil runs.
type FanOutFunc func(ctx context.Context, f forge.Forge, vals preview.PreviewValues, body string) error

// FanOutPRComments posts (or updates) the same markdown body as badil's
// comment (forge.Forge.UpsertComment) on every open PR referenced in
// vals.Services.Metadata.pr-url, parsed with f.ParsePRURL. Closed PRs are skipped, so a stale summary
// never overwrites their teardown comment. Failures on individual services
// (including a PR state lookup) are collected but do not abort the loop —
// best-effort delivery.
func FanOutPRComments(ctx context.Context, f forge.Forge, vals preview.PreviewValues, body string) error {
	var errs []error
	for _, svc := range vals.Services {
		if svc.Metadata == nil || svc.Metadata.PRURL == "" {
			continue
		}
		prURL := svc.Metadata.PRURL
		owner, repo, prNumber, ok := f.ParsePRURL(prURL)
		if !ok {
			errs = append(errs, fmt.Errorf("service %s: unrecognized pr-url %q", svc.Name, prURL))
			continue
		}
		pr, stateErr := f.GetPullRequest(ctx, owner, repo, prNumber)
		if stateErr != nil {
			errs = append(errs,
				fmt.Errorf("service %s (%s/%s#%d): pr state: %w", svc.Name, owner, repo, prNumber, stateErr))
			continue
		}
		if !pr.Open {
			continue
		}
		if upsertErr := f.UpsertComment(ctx, owner, repo, prNumber, body); upsertErr != nil {
			errs = append(errs, fmt.Errorf("service %s (%s/%s#%d): %w", svc.Name, owner, repo, prNumber, upsertErr))
		}
	}
	return errors.Join(errs...)
}
