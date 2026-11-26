package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/badilhq/badil/internal/catalog"
	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
)

// Sweep rules that delete a preview, the rule label of Recorder.SweepDeleted.
const (
	ruleNoPin = "no_pin"
	ruleTTL   = "ttl"
)

// SweepStore is the subset of store.GitHubStore used by Sweep.
type SweepStore interface {
	Applier
	ListPreviews(ctx context.Context) ([]string, error)
	GetPreview(ctx context.Context, slug string) (*preview.Values, string, error)
}

// SweepAPI is what Sweep uses: the store, the forge, the Recorder, the
// comment fan-out and, when set, the expiry store.
type SweepAPI struct {
	Store    SweepStore
	Forge    forge.Forge
	Recorder Recorder
	FanOut   FanOutFunc
	// Expiry, when set, records the open PRs of the previews rule 2 deletes.
	Expiry ExpiryStore
}

// teardownAPI narrows a to what a teardown uses.
func (a SweepAPI) teardownAPI() TeardownAPI {
	return TeardownAPI{Store: a.Store, Forge: a.Forge, Recorder: a.Recorder, FanOut: a.FanOut}
}

// Sweep visits every preview of the gitops repo once, in one sweep
// operation, applies the first matching rule 1-3, then syncs every preview
// that rules 1 and 2 did not match (step 4):
//
//  1. no pinned entry: the preview is deleted;
//  2. ttl > 0 and the newest updated-at of its pinned entries is before
//     now - ttl: the preview is deleted, its open PRs are recorded in
//     api.Expiry at their head SHA, when set, and told it expired;
//  3. otherwise every distinct PR pinning an entry is looked at. A closed one
//     is torn down, its comment saying so. An open one whose labels
//     (preview.ResolveTarget) conflict, or target another preview, is
//     unpinned without a comment; one with no target, pinned outside its
//     branch slug, is torn down with a comment. Other open PRs are left
//     alone.
//  4. after rule 3, the preview is rewritten to match the catalog
//     (syncPreview). A rewrite is recorded and logged; when it changed
//     the service list or a host, the open PRs' comments are refreshed.
//
// The rules are decided on the tree each attempt of the operation reads.
// The states of the PRs pinned in the previews are read before the
// operation, once per distinct PR: a PR whose state can't be read, or that a
// retried attempt finds pinned without having been read, keeps its pins
// until the next sweep. The catalog of the tree gives the preview label and
// step 4; when it can't be read, a warning is logged, only closed PRs are
// torn down by rule 3 and step 4 is skipped.
//
// A ttl of 0 disables rule 2. A preview that can't be read or synced is
// logged and counted, and the sweep goes on; Sweep then returns an error
// carrying the count. A failed comment is only logged. A ListPreviews error,
// or a failed operation, is returned at once.
//
// The number of listed previews and each rule 1-2 deletion are recorded
// through the Recorder; teardowns are recorded with the sweep trigger.
func Sweep(ctx context.Context, api SweepAPI, now time.Time, ttl time.Duration) error {
	slugs, err := api.Store.ListPreviews(ctx)
	if err != nil {
		return err
	}
	api.Recorder.SweepListed(len(slugs))
	slog.InfoContext(ctx, "cleanup started",
		slog.Int("previews", len(slugs)),
		slog.String("ttl", ttl.String()),
	)
	if len(slugs) == 0 {
		slog.InfoContext(ctx, "cleanup finished", slog.Int("previews", 0), slog.Int("failed", 0))
		return nil
	}
	p := &sweepPlan{now: now, ttl: ttl, states: prStates(ctx, api, slugs), parsePR: api.Forge.ParsePRURL}
	res, err := api.Store.Apply(ctx, Op{Kind: OpSweep, Change: p.change})
	if err != nil {
		for _, s := range p.steps {
			afterTeardown(ctx, api.teardownAPI(), s, res, err, true)
		}
		return fmt.Errorf("cleanup: %w", err)
	}
	failed := p.finish(ctx, api, res)
	slog.InfoContext(ctx, "cleanup finished",
		slog.Int("previews", len(slugs)),
		slog.Int("failed", failed),
	)
	if failed > 0 {
		return fmt.Errorf("cleanup: %d preview(s) failed", failed)
	}
	return nil
}

// prStates reads the state of each distinct PR pinned in the previews slugs,
// keyed by PRKey. A preview that can't be read is skipped: the sweep counts
// it. A PR whose state can't be read is logged and left out.
func prStates(ctx context.Context, api SweepAPI, slugs []string) map[string]*forge.PullRequest {
	states := map[string]*forge.PullRequest{}
	tried := map[string]bool{}
	for _, slug := range slugs {
		vals, _, err := api.Store.GetPreview(ctx, slug)
		if err != nil {
			continue
		}
		for _, t := range pinnedPRs(api.Forge.ParsePRURL, slug, *vals) {
			key := PRKey(t)
			if tried[key] {
				continue
			}
			tried[key] = true
			pr, prErr := api.Forge.GetPullRequest(ctx, t.Owner, t.Repo, t.Number)
			if prErr != nil {
				slog.WarnContext(ctx, "cleanup pr state unknown",
					slog.String("slug", slug),
					slog.String("repo", t.FullName),
					slog.Int("pr_number", t.Number),
					slog.String("err", prErr.Error()),
				)
				continue
			}
			states[key] = pr
		}
	}
	return states
}

// PRKey identifies the PR of t: its owner/repo, lowercased as GitHub
// compares them case-insensitively, and number.
func PRKey(t Target) string {
	return fmt.Sprintf("%s/%s#%d", strings.ToLower(t.Owner), strings.ToLower(t.Repo), t.Number)
}

// sweepPlan is a sweep once the PR states were read. change runs it on the
// gitops tree, as many times as Apply attempts; the fields below the line
// hold the last attempt's outcome, which finish acts on.
type sweepPlan struct {
	now    time.Time
	ttl    time.Duration
	states map[string]*forge.PullRequest
	// parsePR is the forge's pull request URL rule.
	parsePR preview.PRURLParser

	catalogErr error
	deleted    []sweepDelete
	steps      []*teardownStep
	synced     []sweepSync
	failed     []sweepFailure
}

// sweepDelete is a preview deleted by rule 1 or 2, with its values.
type sweepDelete struct {
	slug string
	rule string
	vals preview.Values
}

// sweepSync is a preview step 4 rewrote.
type sweepSync struct {
	slug    string
	result  *SyncResult
	catalog *catalog.Catalog
}

// sweepFailure is a preview that could not be read or synced.
type sweepFailure struct {
	slug string
	err  error
}

// change runs the sweep on tree, as Sweep describes.
func (p *sweepPlan) change(tree Tree) (Summary, error) {
	p.deleted, p.steps, p.synced, p.failed = nil, nil, nil, nil
	catalog, catalogErr := tree.Catalog()
	p.catalogErr = catalogErr
	slugs, err := tree.ListPreviews()
	if err != nil {
		return Summary{}, err
	}
	for _, slug := range slugs {
		if err = p.sweepPreview(tree, slug, catalog); err != nil {
			p.failed = append(p.failed, sweepFailure{slug: slug, err: err})
		}
	}
	return p.summary(), nil
}

// sweepPreview applies the first matching rule 1-3 to the preview slug of
// tree, then syncs it when it was kept (step 4). A preview with no values
// file is skipped. A nil catalog skips rule 3's open-PR cases and step 4.
func (p *sweepPlan) sweepPreview(tree Tree, slug string, catalog *catalog.Catalog) error {
	vals, err := tree.GetPreview(slug)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !hasPin(*vals) {
		tree.DeletePreview(slug)
		p.deleted = append(p.deleted, sweepDelete{slug: slug, rule: ruleNoPin, vals: *vals})
		return nil
	}
	if p.ttl > 0 && expired(*vals, p.now.Add(-p.ttl)) {
		tree.DeletePreview(slug)
		p.deleted = append(p.deleted, sweepDelete{slug: slug, rule: ruleTTL, vals: *vals})
		return nil
	}
	label := ""
	if catalog != nil {
		label = catalog.Label
	}
	for _, t := range pinnedPRs(p.parsePR, slug, *vals) {
		pr, ok := p.states[PRKey(t)]
		if !ok {
			continue
		}
		if s := ruleThree(t, pr, label); s != nil {
			s.run(tree, p.parsePR)
			p.steps = append(p.steps, s)
		}
	}
	if catalog == nil {
		return nil
	}
	res, err := syncPreview(tree, slug, catalog)
	if err != nil {
		return err
	}
	if res.Changed {
		p.synced = append(p.synced, sweepSync{slug: slug, result: res, catalog: catalog})
	}
	return nil
}

// ruleThree returns the teardown of the PR pr from the preview t.Slug that
// rule 3 makes, nil when it leaves the PR alone. A closed PR is torn down; an
// open one, when label is set, is unpinned without a comment when its labels
// conflict or target another preview, and torn down when they target none
// while t.Slug is not its branch slug.
func ruleThree(t Target, pr *forge.PullRequest, label string) *teardownStep {
	if !pr.Open {
		return &teardownStep{tt: t, reason: PRClosedReason, trigger: triggerSweep, commentPR: true}
	}
	if label == "" {
		return nil
	}
	r := preview.ResolveTarget(pr.Labels, pr.HeadRef, label)
	switch {
	case r.Conflict:
		return &teardownStep{tt: t, reason: conflictReason(label), trigger: triggerSweep}
	case r.Slug != "" && r.Slug != t.Slug:
		return &teardownStep{tt: t, reason: movedReason(r.Slug), trigger: triggerSweep}
	case r.Slug == "" && t.Slug != preview.Slugify(pr.HeadRef):
		return &teardownStep{tt: t, reason: labelRemovedReason(label), trigger: triggerSweep, commentPR: true}
	}
	return nil
}

// summary returns the commit subject of the last attempt: the previews it
// deleted (by a rule or once their last PR was torn down), the previews it
// unpinned and kept, and the previews it synced.
func (p *sweepPlan) summary() Summary {
	deleted := map[string]bool{}
	for _, d := range p.deleted {
		deleted[d.slug] = true
	}
	unpinned := map[string]bool{}
	for _, s := range p.steps {
		switch {
		case !s.removed():
		case s.result.Deleted:
			deleted[s.tt.Slug] = true
		default:
			unpinned[s.tt.Slug] = true
		}
	}
	for slug := range deleted {
		delete(unpinned, slug)
	}
	return Summary{Subject: fmt.Sprintf("chore(preview): sweep: delete %d, unpin %d, sync %d",
		len(deleted), len(unpinned), len(p.synced))}
}

// finish handles the sweep's operation, pushed as res: it records and logs
// each deletion and tells an expired preview's open PRs, handles each
// teardown (afterTeardown), records and logs each sync and refreshes the open
// PRs' comments of a preview whose sync changed its service list or a host,
// or that a teardown unpinned, once per preview. It logs each failed preview
// and returns their count.
func (p *sweepPlan) finish(ctx context.Context, api SweepAPI, res Result) int {
	if p.catalogErr != nil {
		slog.WarnContext(ctx, "cleanup catalog unavailable", slog.String("err", p.catalogErr.Error()))
	}
	for _, d := range p.deleted {
		p.afterDelete(ctx, api, d, res)
	}
	synced := map[string]bool{}
	for _, s := range p.synced {
		synced[s.slug] = true
	}
	// A preview's summary is refreshed once, by its last step that removed a
	// PR, whose values are the preview's as committed, unless step 4 rewrote
	// it, or that step deleted it.
	last := map[string]*teardownStep{}
	for _, s := range p.steps {
		if s.removed() {
			last[s.tt.Slug] = s
		}
	}
	unpinned := map[string]bool{}
	for _, s := range p.steps {
		fanOut := last[s.tt.Slug] == s && !synced[s.tt.Slug]
		if afterTeardown(ctx, api.teardownAPI(), s, res, nil, fanOut) && !s.result.Deleted {
			unpinned[s.tt.Slug] = true
		}
	}
	for _, s := range p.synced {
		afterSync(ctx, api.Recorder, api.Forge, api.FanOut, s.slug, s.result, s.catalog, res, unpinned[s.slug])
	}
	for _, f := range p.failed {
		slog.ErrorContext(ctx, "cleanup preview failed",
			slog.String("slug", f.slug),
			slog.String("err", f.err.Error()),
		)
	}
	return len(p.failed)
}

// afterDelete records and logs a preview deleted by rule 1 or 2, pushed as
// res, and, for an expired one, records its open PRs in api.Expiry and tells
// them.
func (p *sweepPlan) afterDelete(ctx context.Context, api SweepAPI, d sweepDelete, res Result) {
	api.Recorder.SweepDeleted(d.rule)
	if d.rule == ruleNoPin {
		slog.InfoContext(ctx, "preview deleted",
			slog.String("slug", d.slug),
			slog.String("reason", "no pins"),
			slog.String("gitops_commit_url", res.URL),
		)
		return
	}
	markExpired(ctx, api.Expiry, p.parsePR, d.slug, d.vals, p.states, p.now)
	last, _ := preview.LastDeploy(d.vals)
	slog.InfoContext(ctx, "preview deleted",
		slog.String("slug", d.slug),
		slog.String("reason", "expired"),
		slog.String("gitops_commit_url", res.URL),
		slog.String("last_deploy", last.Format(time.RFC3339)),
	)
	body := preview.BuildExpirySummary(d.slug, p.ttl, res.Subject, res.URL)
	if fanErr := api.FanOut(ctx, api.Forge, d.vals, body); fanErr != nil {
		slog.WarnContext(ctx, "preview expiry comment failed",
			slog.String("slug", d.slug),
			slog.String("err", fanErr.Error()),
		)
	}
}

// afterSync records and logs a preview rewritten to match catalog, pushed as
// res, and, when the rewrite changed its service list or a host, or notify
// is set, refreshes its open PRs' comments.
func afterSync(
	ctx context.Context,
	rec Recorder,
	f forge.Forge,
	fanOut FanOutFunc,
	slug string,
	result *SyncResult,
	catalog *catalog.Catalog,
	res Result,
	notify bool,
) {
	rec.PreviewSynced()
	slog.InfoContext(ctx, "preview synced",
		slog.String("slug", slug),
		slog.String("gitops_commit_url", res.URL),
	)
	if !result.Notify && !notify {
		return
	}
	body := preview.BuildSummary(
		f.ParsePRURL, slug, result.Values, catalog.URLs, catalog.ArgoCDURL, result.Excluded, res.Subject, res.URL,
	)
	if fanErr := fanOut(ctx, f, result.Values, body); fanErr != nil {
		slog.WarnContext(ctx, "preview sync comment failed",
			slog.String("slug", slug),
			slog.String("err", fanErr.Error()),
		)
	}
}

// hasPin reports whether any entry of vals is pinned to a commit.
func hasPin(vals preview.Values) bool {
	for _, svc := range vals.Services {
		if svc.CommitSha != "" {
			return true
		}
	}
	return false
}

// expired reports whether the newest updated-at of vals' pinned entries is
// before cutoff. A preview without a parseable updated-at never expires.
func expired(vals preview.Values, cutoff time.Time) bool {
	last, ok := preview.LastDeploy(vals)
	return ok && last.Before(cutoff)
}

// pinnedPRs returns one Target per distinct PR referenced by a pinned entry of
// vals, in file order. PRs compare on lowercase owner/repo and number. Entries
// without metadata or with a pr-url parse rejects are skipped.
func pinnedPRs(parse preview.PRURLParser, slug string, vals preview.Values) []Target {
	var targets []Target
	seen := map[string]bool{}
	for _, svc := range vals.Services {
		if svc.CommitSha == "" || svc.Metadata == nil {
			continue
		}
		owner, repo, number, ok := parse(svc.Metadata.PRURL)
		if !ok {
			continue
		}
		t := Target{Slug: slug, Owner: owner, Repo: repo, FullName: owner + "/" + repo, Number: number}
		if seen[PRKey(t)] {
			continue
		}
		seen[PRKey(t)] = true
		targets = append(targets, t)
	}
	return targets
}
