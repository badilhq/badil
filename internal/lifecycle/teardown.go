package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
)

// PRClosedReason is the teardown reason for a closed (or merged) PR.
const PRClosedReason = "PR closed"

// Teardown triggers, the trigger label of Recorder.Teardown.
const (
	triggerWebhook = "webhook"
	triggerSweep   = "sweep"
)

// Teardown results, the result label of Recorder.Teardown.
const (
	teardownError    = "error"
	teardownNoop     = "noop"
	teardownDeleted  = "deleted"
	teardownUnpinned = "unpinned"
)

// Recorder records lifecycle outcomes. *metrics.Metrics implements it; a nil
// one records nothing.
type Recorder interface {
	// Reconcile records one ReconcilePR outcome and, when skipped, its reason.
	Reconcile(outcome, reason string)
	// Teardown records one teardown of a PR from a preview.
	Teardown(trigger, result string)
	// SweepListed records the number of previews a sweep listed.
	SweepListed(n int)
	// SweepDeleted records one preview deleted by a sweep rule.
	SweepDeleted(rule string)
	// PreviewSynced records one preview rewritten to match badil.yaml.
	PreviewSynced()
	// PreviewCreated records one preview a deploy created.
	PreviewCreated()
}

// Applier writes the gitops repo: store.GitHubStore.Apply.
type Applier interface {
	Apply(ctx context.Context, op Op) (Result, error)
}

// TeardownAPI is what a teardown uses: the store, the forge, the Recorder
// and the comment fan-out.
type TeardownAPI struct {
	Store    Applier
	Forge    forge.Forge
	Recorder Recorder
	FanOut   FanOutFunc
}

// Target identifies a preview and the PR to tear down or reconcile.
type Target struct {
	Slug     string
	Owner    string
	Repo     string
	FullName string
	Number   int
}

// teardownStep is the teardown of the PR tt from the preview tt.Slug in an
// operation: why, how it is recorded, whether the PR's comment states it,
// whether only some services are unpinned, and what the last attempt did.
type teardownStep struct {
	tt      Target
	reason  string
	trigger string
	// commentPR edits the PR's comment, which states reason.
	commentPR bool
	// services, when set, are the only services unpinned: the file is never
	// deleted (Tree.UnpinServices).
	services []string
	// ran is set once an attempt ran the step; result or err is its outcome.
	ran    bool
	result *TeardownResult
	err    error
}

// run tears the PR down from its preview in tree, recording the outcome. A
// failure is recorded, not returned: it stages nothing.
func (s *teardownStep) run(tree Tree) {
	s.ran = true
	if s.services != nil {
		s.result, s.err = tree.UnpinServices(s.tt.Slug, s.tt.Owner, s.tt.Repo, s.tt.Number, s.services)
		return
	}
	s.result, s.err = tree.Teardown(s.tt.Slug, s.tt.Owner, s.tt.Repo, s.tt.Number)
}

// removed reports whether the step removed the PR from its preview.
func (s *teardownStep) removed() bool {
	return s.ran && s.err == nil && len(s.result.Removed) > 0
}

// newSteps returns the teardown steps of the PR tt from each of slugs, with
// reason, triggered by a webhook, each editing the PR's comment.
func newSteps(tt Target, slugs []string, reason string) []*teardownStep {
	steps := make([]*teardownStep, 0, len(slugs))
	for _, slug := range slugs {
		t := tt
		t.Slug = slug
		steps = append(steps, &teardownStep{tt: t, reason: reason, trigger: triggerWebhook, commentPR: true})
	}
	return steps
}

// removedSlugs returns, sorted and deduplicated, the previews steps removed
// their PR from.
func removedSlugs(steps []*teardownStep) []string {
	var slugs []string
	for _, s := range steps {
		if s.removed() {
			slugs = append(slugs, s.tt.Slug)
		}
	}
	slices.Sort(slugs)
	return slices.Compact(slugs)
}

// unpinSubject is the commit subject of an operation that only unpins the PR
// tt from slugs or deletes them.
func unpinSubject(tt Target, slugs []string) string {
	return fmt.Sprintf("chore(preview): unpin %s#%d from %s", tt.FullName, tt.Number, strings.Join(slugs, ", "))
}

// teardownPRs tears the PR tt down from each of slugs, with reason and the
// webhook trigger, in one teardown operation, then handles each teardown
// (afterTeardown).
func teardownPRs(ctx context.Context, api TeardownAPI, tt Target, slugs []string, reason string) {
	var steps []*teardownStep
	res, err := api.Store.Apply(ctx, Op{
		Kind: OpTeardown,
		Change: func(tree Tree) (Summary, error) {
			steps = newSteps(tt, slugs, reason)
			for _, s := range steps {
				s.run(tree)
			}
			return Summary{Subject: unpinSubject(tt, removedSlugs(steps))}, nil
		},
	})
	if steps == nil {
		steps = newSteps(tt, slugs, reason)
	}
	for _, s := range steps {
		afterTeardown(ctx, api, s, res, err, true)
	}
}

// afterTeardown handles a teardown step of an operation that ended with res
// or applyErr: it records the teardown with the step's trigger and logs it;
// when the step comments the PR, it edits the PR's comment, which states the
// reason; when the preview survives and fanOut is set, it refreshes the
// remaining PRs' summaries. A step whose operation failed is recorded as an
// error when it removed the PR from the preview, or never ran; a step that
// removed nothing is a no-op. The comments link to the commit of res. It
// reports whether the PR was removed from the preview.
func afterTeardown(
	ctx context.Context, api TeardownAPI, s *teardownStep, res Result, applyErr error, fanOut bool,
) bool {
	err := s.err
	if applyErr != nil && (!s.ran || s.removed()) {
		err = applyErr
	}
	tt := s.tt
	if err != nil {
		api.Recorder.Teardown(s.trigger, teardownError)
		slog.ErrorContext(ctx, "preview teardown failed",
			slog.String("slug", tt.Slug),
			slog.String("repo", tt.FullName),
			slog.Int("pr_number", tt.Number),
			slog.String("err", err.Error()),
		)
		return false
	}
	result := s.result
	if len(result.Removed) == 0 {
		api.Recorder.Teardown(s.trigger, teardownNoop)
		slog.InfoContext(ctx, "preview teardown skipped",
			slog.String("slug", tt.Slug),
			slog.String("repo", tt.FullName),
			slog.Int("pr_number", tt.Number),
			slog.String("reason", "no preview or no matching service"),
		)
		return false
	}
	if result.Deleted {
		api.Recorder.Teardown(s.trigger, teardownDeleted)
	} else {
		api.Recorder.Teardown(s.trigger, teardownUnpinned)
	}
	slog.InfoContext(ctx, "preview teardown",
		slog.String("slug", tt.Slug),
		slog.String("repo", tt.FullName),
		slog.Int("pr_number", tt.Number),
		slog.String("removed", strings.Join(result.Removed, ", ")),
		slog.Bool("deleted", result.Deleted),
		slog.String("gitops_commit_url", res.URL),
	)

	if s.commentPR {
		body := preview.BuildTeardownSummary(tt.Slug, result.Removed, result.Deleted, s.reason, res.Subject, res.URL)
		if upsertErr := api.Forge.UpsertComment(ctx, tt.Owner, tt.Repo, tt.Number, body); upsertErr != nil {
			teardownCommentFailed(ctx, tt, upsertErr)
		}
	}
	if result.Deleted || !fanOut {
		return true
	}
	summary := preview.BuildSummary(
		api.Forge.ParsePRURL, tt.Slug, result.Values, result.Catalog.URLs, result.Catalog.ArgoCDURL,
		result.Excluded, res.Subject, res.URL,
	)
	if fanErr := api.FanOut(ctx, api.Forge, result.Values, summary); fanErr != nil {
		teardownCommentFailed(ctx, tt, fanErr)
	}
	return true
}

// teardownCommentFailed logs a comment failure after a successful teardown
// write.
func teardownCommentFailed(ctx context.Context, tt Target, err error) {
	slog.WarnContext(ctx, "preview teardown comment failed",
		slog.String("slug", tt.Slug),
		slog.String("repo", tt.FullName),
		slog.Int("pr_number", tt.Number),
		slog.String("err", err.Error()),
	)
}
