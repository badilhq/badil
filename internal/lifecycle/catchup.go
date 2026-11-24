package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// catchUpAction is the webhook action of the events catch-up reconciles.
const catchUpAction = "synchronize"

// CatchUpStore is the subset of store.GitHubStore used by CatchUp.
type CatchUpStore interface {
	FetchServiceCatalog(ctx context.Context) (*store.ServiceCatalog, error)
	GetPreview(ctx context.Context, slug string) (*preview.Values, string, error)
}

// CatchUpRecorder records the reconciles catch-up starts. *metrics.Metrics
// implements it; a nil one records nothing.
type CatchUpRecorder interface {
	CatchUpReconcile()
}

// CatchUpAPI is what CatchUp uses: the store, the forge, the recorder and,
// when set, Skipped.
type CatchUpAPI struct {
	Store    CatchUpStore
	Forge    forge.Forge
	Recorder CatchUpRecorder
	// Skipped, when set, is called with the target of each listed PR that
	// Run leaves alone at its step 1 or 2, the skipped result it would get
	// (reason NotLabeledReason or ForkPRReason, at its listed head SHA), and
	// listedAt, taken just before Run listed the PR's repo.
	Skipped func(t Target, res ReconcileResult, listedAt time.Time)
}

// ReconcileFunc reconciles ev, as ReconcilePR does, and calls done with
// ReconcilePR's result once the reconcile completes. It may return before. A
// reconcile that panics calls done with the failed outcome, or its PR stays
// queued for the lifetime of the CatchUp.
type ReconcileFunc func(ctx context.Context, ev Event, done func(ReconcileResult))

// CatchUp reconciles the open pull requests that missed their webhooks. It
// keeps, for the lifetime of the value, the head SHA at which each PR was
// last reconciled without failing (the seen map) and the PRs whose
// reconcile is queued or running. Its methods are safe for concurrent use.
type CatchUp struct {
	api       CatchUpAPI
	reconcile ReconcileFunc

	mu sync.Mutex
	// seen maps a PR URL to the head SHA its last catch-up reconcile that
	// completed with an outcome other than failed was started at.
	seen map[string]string
	// pending holds the URLs of the PRs whose catch-up reconcile is queued
	// or running.
	pending map[string]bool
}

// NewCatchUp returns a CatchUp over api that reconciles through reconcile,
// with an empty seen map.
func NewCatchUp(api CatchUpAPI, reconcile ReconcileFunc) *CatchUp {
	return &CatchUp{api: api, reconcile: reconcile, seen: map[string]string{}, pending: map[string]bool{}}
}

// catchUpStats are the counts of the catch-up log line.
type catchUpStats struct {
	repos, openLabelled, reconciled, skippedPinned, skippedSeen, listErrors int
}

// Run lists the open pull requests of every repo of the catalog's services
// (store.ServiceCatalog.Repos, by the forge's repo URL rule) and reconciles each one that needs it,
// decided in this order:
//
//  1. a PR without a preview label (the plain label, or a named one with a
//     slug) is left alone;
//  2. so is a fork PR (its head repo differs from its base repo). A PR left
//     alone at step 1 or 2 is passed to the Skipped callback, when set,
//     with the time taken just before its repo was listed;
//  3. its target preview is resolved from its labels (preview.ResolveTarget);
//     with conflicting labels it has none, and goes on to step 5;
//  4. a PR with a target whose preview file has an entry whose pr-url names
//     the PR (preview.MatchesPR) and whose commitSha is its head SHA is
//     pinned at head, and skipped;
//  5. a PR in the seen map at its head SHA, or whose catch-up reconcile is
//     queued or running, is skipped;
//  6. any other PR is reconciled as a synchronize event with its listed
//     labels. Once its reconcile completes with an outcome other than
//     failed, it enters the seen map at the head SHA listed here.
//
// A repo whose listing fails is logged and skipped. When the catalog can't
// be read, Run logs it and lists nothing. Once ctx is done, Run lists no
// more repos and claims no more PRs. Each started reconcile is recorded,
// and the run ends with the catch-up log line.
func (c *CatchUp) Run(ctx context.Context) {
	catalog, err := c.api.Store.FetchServiceCatalog(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "catch-up failed", slog.String("err", err.Error()))
		return
	}
	var stats catchUpStats
	repos := catalog.Repos(c.api.Forge.ParseRepoURL)
	stats.repos = len(repos)
	for _, r := range repos {
		if ctx.Err() != nil {
			break
		}
		listedAt := time.Now()
		prs, listErr := c.api.Forge.ListOpenPullRequests(ctx, r.Owner, r.Repo)
		if listErr != nil {
			stats.listErrors++
			slog.WarnContext(ctx, "catch-up list failed",
				slog.String("repo", r.Owner+"/"+r.Repo),
				slog.String("err", listErr.Error()),
			)
			continue
		}
		for _, pr := range prs {
			if ctx.Err() != nil {
				break
			}
			c.consider(ctx, catalog.Label, pr, listedAt, &stats)
		}
	}
	slog.InfoContext(ctx, "catch-up",
		slog.Int("repos", stats.repos),
		slog.Int("open_labelled", stats.openLabelled),
		slog.Int("reconciled", stats.reconciled),
		slog.Int("skipped_pinned", stats.skippedPinned),
		slog.Int("skipped_seen", stats.skippedSeen),
		slog.Int("list_errors", stats.listErrors),
	)
}

// consider applies Run's decision order to pr, listed at listedAt, label
// being the preview label, and counts it in stats.
func (c *CatchUp) consider(ctx context.Context, label string, pr forge.PullRequest, listedAt time.Time,
	stats *catchUpStats,
) {
	if !hasPreviewLabel(pr.Labels, label) {
		c.skipped(pr, NotLabeledReason, listedAt)
		return
	}
	stats.openLabelled++
	if pr.Fork() {
		c.skipped(pr, ForkPRReason, listedAt)
		return
	}
	if t := preview.ResolveTarget(pr.Labels, pr.HeadRef, label); t.Slug != "" && c.pinnedAtHead(ctx, t.Slug, pr) {
		stats.skippedPinned++
		return
	}
	if ctx.Err() != nil {
		return
	}
	if !c.claim(pr) {
		stats.skippedSeen++
		return
	}
	stats.reconciled++
	c.api.Recorder.CatchUpReconcile()
	c.reconcile(ctx, catchUpEvent(pr), func(res ReconcileResult) { c.release(pr, res) })
}

// skipped passes pr, listed at listedAt and left alone for reason, to the
// Skipped callback when it is set.
func (c *CatchUp) skipped(pr forge.PullRequest, reason string, listedAt time.Time) {
	if c.api.Skipped == nil {
		return
	}
	c.api.Skipped(catchUpEvent(pr).Target,
		ReconcileResult{Outcome: OutcomeSkipped, Reason: reason, HeadSHA: pr.HeadSHA}, listedAt)
}

// pinnedAtHead reports whether the preview slug has an entry pinned by pr at
// its head SHA: its pr-url names pr (preview.MatchesPR) and its commitSha is
// pr's head SHA. A preview that does not exist pins nothing; one that can't
// be read is logged and counted as not pinned, so the PR is reconciled.
func (c *CatchUp) pinnedAtHead(ctx context.Context, slug string, pr forge.PullRequest) bool {
	vals, _, err := c.api.Store.GetPreview(ctx, slug)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.WarnContext(ctx, "catch-up preview read failed",
				slog.String("slug", slug),
				slog.String("repo", pr.FullName),
				slog.Int("pr_number", pr.Number),
				slog.String("err", err.Error()),
			)
		}
		return false
	}
	owner, repo, _ := strings.Cut(pr.FullName, "/")
	return slices.ContainsFunc(vals.Services, func(svc preview.ServiceEntry) bool {
		return svc.Metadata != nil && svc.CommitSha == pr.HeadSHA &&
			preview.MatchesPR(c.api.Forge.ParsePRURL, svc.Metadata.PRURL, owner, repo, pr.Number)
	})
}

// claim marks pr's reconcile as queued and returns true, unless pr is in the
// seen map at its head SHA or already has a reconcile queued or running.
func (c *CatchUp) claim(pr forge.PullRequest) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[pr.URL] {
		return false
	}
	if sha, ok := c.seen[pr.URL]; ok && sha == pr.HeadSHA {
		return false
	}
	c.pending[pr.URL] = true
	return true
}

// release ends pr's queued reconcile, which completed with res: pr enters
// the seen map at its listed head SHA unless res failed.
func (c *CatchUp) release(pr forge.PullRequest, res ReconcileResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, pr.URL)
	if res.Outcome != OutcomeFailed {
		c.seen[pr.URL] = pr.HeadSHA
	}
}

// catchUpEvent is the synchronize event of the listed pr, with its labels
// and head SHA.
func catchUpEvent(pr forge.PullRequest) Event {
	owner, repo, _ := strings.Cut(pr.FullName, "/")
	return Event{
		Target:  Target{Owner: owner, Repo: repo, FullName: pr.FullName, Number: pr.Number},
		Action:  catchUpAction,
		HeadRef: pr.HeadRef,
		Labels:  pr.Labels,
		HeadSHA: pr.HeadSHA,
	}
}
