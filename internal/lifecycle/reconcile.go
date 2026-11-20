package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// actionUnlabeled is the webhook action of a removed label.
const actionUnlabeled = "unlabeled"

// alreadyDeployedReason is the skip reason of a reconcile whose every service
// was already deployed as it would be written.
const alreadyDeployedReason = "already deployed"

// Skip reasons of a reconcile.
const (
	// NoServiceReason is the skip reason of a reconcile whose PR changes no
	// service of its repo: no changed file matches their paths.
	NoServiceReason = "no service changed"
	// PreviewLimitReason is the skip reason of a reconcile refused because
	// creating its preview would exceed preview.maxPreviews.
	PreviewLimitReason = "preview limit reached"
	// NotLabeledReason is the skip reason of an opened, reopened or
	// synchronize event of a PR without a preview label.
	NotLabeledReason = "not labeled"
	// OtherLabelReason is the skip reason of a labeled or unlabeled event of
	// a label that is not a preview label.
	OtherLabelReason = "other label"
	// ForkPRReason is the skip reason of a fork PR.
	ForkPRReason = "fork pr"
	// RepoNotInCatalogReason is the skip reason of a PR of a repo with no
	// service in the catalog.
	RepoNotInCatalogReason = "repo not in catalog"
	// ServiceHeldReason is the skip reason of a reconcile refused because
	// another PR pins one of its services in its target preview.
	ServiceHeldReason = "service held by another pr"
	// LabelConflictReason is the skip reason of an open PR whose named
	// preview labels conflict.
	LabelConflictReason = "conflicting preview labels"
)

// Reconcile outcomes, the outcome label of Recorder.Reconcile.
const (
	// OutcomeDeployed is the outcome of a reconcile that wrote a service.
	OutcomeDeployed = "deployed"
	// OutcomeSkipped is the outcome of a skipped reconcile, with its reason.
	OutcomeSkipped = "skipped"
	// OutcomeTornDown is the outcome of a reconcile that only tore down.
	OutcomeTornDown = "torn_down"
	// OutcomeFailed is the outcome of a reconcile that failed.
	OutcomeFailed = "failed"
)

// ReconcileStore is the subset of store.GitHubStore used by ReconcilePR.
type ReconcileStore interface {
	Applier
	FetchServiceCatalog(ctx context.Context) (*store.ServiceCatalog, error)
}

// ReconcileAPI is what ReconcilePR uses: the store, the forge, the Recorder
// and the comment fan-out.
type ReconcileAPI struct {
	Store    ReconcileStore
	Forge    forge.Forge
	Recorder Recorder
	FanOut   FanOutFunc
}

// teardownAPI narrows a to what a teardown uses.
func (a ReconcileAPI) teardownAPI() TeardownAPI {
	return TeardownAPI{Store: a.Store, Forge: a.Forge, Recorder: a.Recorder, FanOut: a.FanOut}
}

// Event is a pull_request webhook delivery to reconcile. Its Target's Slug is
// empty: the slug comes from the PR read from GitHub.
type Event struct {
	Target

	// Action is the webhook action (opened, labeled, ...).
	Action string
	// HeadRef is the PR's head branch in the payload. Only the payload
	// teardown of a closed PR uses it.
	HeadRef string
	// Label is the added or removed label, on labeled and unlabeled.
	Label string
	// Labels holds the names of the PR's labels in the payload.
	Labels []string
	// HeadSHA is the PR's head SHA in the payload, or in the listing of a
	// catch-up event.
	HeadSHA string
}

// ReconcilePR brings the preview in line with the PR's current state on
// GitHub. When the payload shows the event cannot concern a preview label,
// nothing is read. Otherwise the PR is read and its labels resolve its target
// preview (preview.ResolveTarget). The PR is first torn down from every other
// preview it may still be pinned in. Then, when it is open and has a target,
// the catalog services of its repo that it touches (touchedServices) are
// deployed there at its head SHA, unless another PR already pins one of them,
// or the preview does not exist and preview.maxPreviews previews already do;
// before that deploy, the PR's entries there for the other services of its
// repo are unpinned. A PR that touches no service is torn down from its
// target and told so, and the reconcile is skipped. An open PR with
// conflicting named labels is told so. Fork PRs and repos outside the catalog
// are skipped. Every outcome is logged.
//
// The forge is read first. The teardowns and the deploy are then one
// operation of the store, one commit, decided on the tree each push attempt
// reads (reconcilePlan.change): a deploy operation when the PR is open with
// a target, a teardown otherwise. The comments are posted once it is pushed.
//
// A closed event is not guarded by its payload nor skipped for a repo outside
// the catalog, and a closed fork PR is still torn down. When the catalog or
// the PR can't be read, it falls back to tearing down the payload's head
// branch and named labels as closed.
//
// A service already deployed as it would be written (only its updated-at
// would change) is not written nor commented again, unless the cleanup removed
// the PR from another preview. When every service is, the reconcile is
// skipped as already deployed.
//
// Each call records exactly one outcome through the Recorder: failed when the
// catalog or the PR can't be read on a non-closed event, when the PR's files
// can't be listed, the target preview can't be read or the previews can't be
// listed (the teardowns from the PR's other previews are still committed),
// or when the commit of a PR open with a target can't be pushed;
// otherwise skipped (with its reason) when the reconcile was skipped;
// otherwise deployed when a service was written; otherwise torn_down. It
// returns that outcome and reason, with the head SHA of the PR it read, or
// ev's when it read none.
func ReconcilePR(ctx context.Context, api ReconcileAPI, ev Event) ReconcileResult {
	head := ev.HeadSHA
	o, r := reconcile(ctx, api, ev, &head)
	api.Recorder.Reconcile(o, r)
	return ReconcileResult{Outcome: o, Reason: r, HeadSHA: head}
}

// ReconcileResult is the outcome ReconcilePR records, the reason when it is
// skipped ("" otherwise), and the head SHA of the PR it reconciled: the one
// GetPullRequest read when that read succeeded, else the event's.
type ReconcileResult struct {
	Outcome string
	Reason  string
	HeadSHA string
}

// reconcile is ReconcilePR without the recording. It returns the outcome and,
// when the outcome is skipped, the reason ("" otherwise). Once the PR is
// read, its head SHA is stored in headSHA.
func reconcile(ctx context.Context, api ReconcileAPI, ev Event, headSHA *string) (string, string) {
	closed := ev.Action == "closed"
	catalog, err := api.Store.FetchServiceCatalog(ctx)
	if err != nil {
		if closed {
			payloadTeardown(ctx, api.teardownAPI(), ev, store.DefaultLabel, err)
			return OutcomeTornDown, ""
		}
		return reconcileFailed(ctx, ev, err)
	}
	services := catalog.ServicesForRepo(ev.Owner, ev.Repo)
	if !closed {
		if reason := payloadSkipReason(ev, catalog.Label); reason != "" {
			return reconcileSkipped(ctx, ev, reason)
		}
		if len(services) == 0 {
			return reconcileSkipped(ctx, ev, RepoNotInCatalogReason)
		}
	}
	pr, err := api.Forge.GetPullRequest(ctx, ev.Owner, ev.Repo, ev.Number)
	if err != nil {
		if closed {
			payloadTeardown(ctx, api.teardownAPI(), ev, catalog.Label, err)
			return OutcomeTornDown, ""
		}
		return reconcileFailed(ctx, ev, err)
	}
	*headSHA = pr.HeadSHA

	t := preview.ResolveTarget(pr.Labels, pr.HeadRef, catalog.Label)
	logIgnored(ctx, ev, t)
	deploy := pr.Open && t.Slug != ""
	if (deploy || !closed) && pr.Fork() {
		return reconcileSkipped(ctx, ev, ForkPRReason)
	}
	if deploy && len(services) == 0 {
		return reconcileSkipped(ctx, ev, RepoNotInCatalogReason)
	}
	p := &reconcilePlan{
		ev: ev, pr: pr, t: t, catalog: catalog, deploy: deploy, repoServices: services,
		cleanupSlugs: cleanupSlugs(t, pr, ev, catalog.Label), cleanupReason: cleanupReason(t, pr, ev, catalog.Label),
		timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if deploy {
		p.services, p.listErr = touchedServices(ctx, api, catalog, ev, pr, services)
		if p.listErr != nil && len(p.cleanupSlugs) == 0 {
			return reconcileFailed(ctx, ev, p.listErr)
		}
	}
	kind := store.OpTeardown
	if deploy {
		kind = store.OpDeploy
	}
	res, applyErr := api.Store.Apply(ctx, store.Op{Kind: kind, Change: p.change})
	return p.finish(ctx, api, res, applyErr)
}

// reconcilePlan is a reconcile once the forge was read: the PR, its target,
// the catalog, the services of its repo and those it touches, and the
// previews it leaves. change runs it on the gitops tree, as many times as
// Apply attempts; the fields below the line hold the last attempt's
// outcome, which finish acts on.
type reconcilePlan struct {
	ev            Event
	pr            *forge.PullRequest
	t             preview.Resolution
	catalog       *store.ServiceCatalog
	deploy        bool
	repoServices  []string
	services      []string
	cleanupSlugs  []string
	cleanupReason string
	timestamp     string
	// listErr is the error of the PR's file listing: the change only tears
	// the PR down from cleanupSlugs, and the reconcile fails.
	listErr error

	ran bool
	// readErr is the error of reading the target preview or listing the
	// previews in the last attempt: the change only tears the PR down from
	// cleanupSlugs, and the reconcile fails.
	readErr   error
	cleanup   []*teardownStep
	noService *teardownStep
	held      *heldPin
	limited   bool
	unpin     *teardownStep
	deploys   []*deployStep
}

// heldPin is a service of the target preview pinned by another PR.
type heldPin struct {
	service, holder string
}

// deployStep is the deploy of one service in the last attempt: its result,
// or its error.
type deployStep struct {
	service string
	result  *store.WriteResult
	err     error
}

// target returns the PR's Target in the preview slug.
func (p *reconcilePlan) target(slug string) Target {
	tt := p.ev.Target
	tt.Slug = slug
	return tt
}

// change runs the reconcile on tree. The PR is first torn down from every
// preview of cleanupSlugs. Then, when it is open and has a target: when it
// touches no service, it is torn down from the target too; otherwise it is
// refused when one of its services is pinned there by another PR, or the
// target does not exist and preview.maxPreviews previews already do; it is
// not refused, its entries for the services of its repo it does not touch
// are unpinned there, then each service it touches is deployed. A service
// already deployed as it would be written is left alone unless the PR left
// another preview. Teardowns and deploys that fail are recorded, not
// returned. When the PR's files could not be listed, or the target preview
// can't be read or the previews listed, only the teardowns from cleanupSlugs
// are kept, and the error is recorded in listErr or readErr.
func (p *reconcilePlan) change(tree *store.Tree) (store.Summary, error) {
	p.ran, p.readErr, p.noService, p.held, p.limited, p.unpin, p.deploys = true, nil, nil, nil, false, nil, nil
	p.cleanup = newSteps(p.ev.Target, p.cleanupSlugs, p.cleanupReason)
	for _, s := range p.cleanup {
		s.run(tree)
	}
	removed := len(removedSlugs(p.cleanup)) > 0
	switch {
	case !p.deploy, p.listErr != nil:
	case len(p.services) == 0:
		p.noService = &teardownStep{tt: p.target(p.t.Slug), reason: NoServiceReason, trigger: triggerWebhook}
		p.noService.run(tree)
	default:
		vals, err := tree.GetPreview(p.t.Slug)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			p.readErr = err
			break
		}
		if vals != nil {
			if service, holder, held := heldBy(vals, p.services, p.ev.Target); held {
				p.held = &heldPin{service: service, holder: holder}
				break
			}
		} else if limited, listErr := limitReached(tree, p.catalog.MaxPreviews); listErr != nil || limited {
			p.limited, p.readErr = limited && listErr == nil, listErr
			break
		}
		if p.unpin = p.untouchedUnpin(vals); p.unpin != nil {
			p.unpin.run(tree)
			removed = removed || p.unpin.removed()
		}
		p.deployAll(tree, !removed)
	}
	return p.summary(), nil
}

// limitReached reports whether maxPreviews previews exist in tree, with no
// cap when maxPreviews is 0.
func limitReached(tree *store.Tree, maxPreviews int) (bool, error) {
	if maxPreviews <= 0 {
		return false, nil
	}
	slugs, err := tree.ListPreviews()
	return len(slugs) >= maxPreviews, err
}

// untouchedUnpin returns the unpin, in the target preview whose values are
// vals, of the PR's entries for the services of its repo it does not touch,
// which never deletes the file; nil when vals is nil (the preview does not
// exist) or the PR pins none of them there.
func (p *reconcilePlan) untouchedUnpin(vals *preview.PreviewValues) *teardownStep {
	if vals == nil {
		return nil
	}
	untouched := slices.DeleteFunc(slices.Clone(p.repoServices), func(name string) bool {
		return slices.Contains(p.services, name)
	})
	_, names, _ := preview.UnpinPRServices(*vals, p.ev.Owner, p.ev.Repo, p.ev.Number, untouched)
	if len(names) == 0 {
		return nil
	}
	return &teardownStep{tt: p.target(p.t.Slug), trigger: triggerWebhook, services: names}
}

// deployAll deploys each touched service, in order, at the PR's head SHA in
// the target preview. With skipUnchanged, a service whose deploy would change
// nothing but its updated-at is left alone.
func (p *reconcilePlan) deployAll(tree *store.Tree, skipUnchanged bool) {
	for _, service := range p.services {
		step := &deployStep{service: service}
		step.result, step.err = tree.Deploy(p.t.Slug, p.catalog, service, p.pr.HeadSHA, preview.DeployInputs{
			ServiceName:    service,
			HeadRef:        p.pr.HeadRef,
			CommitSha:      p.pr.HeadSHA,
			PRAuthor:       p.pr.Author,
			PRURL:          p.pr.URL,
			PRNumber:       strconv.Itoa(p.ev.Number),
			Timestamp:      p.timestamp,
			WorkflowRunURL: "",
		}, skipUnchanged)
		p.deploys = append(p.deploys, step)
	}
}

// written returns the deploy steps that changed the target preview.
func (p *reconcilePlan) written() []*deployStep {
	var out []*deployStep
	for _, d := range p.deploys {
		if d.err == nil && !d.result.Unchanged {
			out = append(out, d)
		}
	}
	return out
}

// summary returns the commit subject of the last attempt: a deploy to the
// target when a service was written there, naming the other previews the PR
// left; otherwise the previews the PR left.
func (p *reconcilePlan) summary() store.Summary {
	tt := p.ev.Target
	others := removedSlugs(p.cleanup)
	if len(p.written()) > 0 {
		subject := fmt.Sprintf("chore(preview): deploy %s#%d to %s", tt.FullName, tt.Number, p.t.Slug)
		if len(others) > 0 {
			subject += ", unpin from " + strings.Join(others, ", ")
		}
		return store.Summary{Subject: subject}
	}
	steps := slices.Clone(p.cleanup)
	for _, s := range []*teardownStep{p.noService, p.unpin} {
		if s != nil {
			steps = append(steps, s)
		}
	}
	return store.Summary{Subject: unpinSubject(tt, removedSlugs(steps))}
}

// finish handles the reconcile's operation, which ended with res or
// applyErr: it records and comments each teardown (afterTeardown), then
// returns the outcome. A PR whose files or target preview could not be read
// fails, as does a deploy whose operation failed. A reconcile that only
// tears down keeps the torn_down outcome when its operation failed, unless
// its open PR's named labels conflict: it is then told to keep one, and
// skipped. Otherwise a PR that
// touches no service is told so and skipped; a refused one is told why and
// skipped; deployed services are logged and their preview's summary posted
// once on its open PRs; an open PR whose named labels conflict is told to
// keep one, and skipped.
func (p *reconcilePlan) finish(
	ctx context.Context, api ReconcileAPI, res store.Result, applyErr error,
) (string, string) {
	tapi := api.teardownAPI()
	if !p.ran {
		p.cleanup = newSteps(p.ev.Target, p.cleanupSlugs, p.cleanupReason)
	}
	for _, s := range p.cleanup {
		afterTeardown(ctx, tapi, s, res, applyErr, true)
	}
	if p.noService != nil {
		afterTeardown(ctx, tapi, p.noService, res, applyErr, true)
		body := preview.BuildNoServiceSummary(p.t.Slug)
		if err := api.Forge.UpsertComment(ctx, p.ev.Owner, p.ev.Repo, p.ev.Number, body); err != nil {
			conflictCommentFailed(ctx, p.ev, err)
		}
		return reconcileSkipped(ctx, p.ev, NoServiceReason)
	}
	if p.listErr != nil {
		return reconcileFailed(ctx, p.ev, p.listErr)
	}
	if p.readErr != nil {
		return reconcileFailed(ctx, p.ev, p.readErr)
	}
	if applyErr != nil {
		if p.unpin != nil {
			afterTeardown(ctx, tapi, p.unpin, res, applyErr, false)
		}
		if p.deploy {
			return reconcileFailed(ctx, p.ev, applyErr)
		}
		if o, r := postLabelConflict(ctx, tapi, p.ev, p.pr, p.catalog.Label, p.t); o != "" {
			return o, r
		}
		return OutcomeTornDown, ""
	}
	if o, r := p.refusal(ctx, api); o != "" {
		return o, r
	}
	outcome, reason := OutcomeTornDown, ""
	if p.deploy {
		outcome, reason = p.afterDeploy(ctx, api, res)
	}
	// A conflict leaves the PR without a target, so nothing was deployed.
	if o, r := postLabelConflict(ctx, api.teardownAPI(), p.ev, p.pr, p.catalog.Label, p.t); o != "" {
		return o, r
	}
	return outcome, reason
}

// refusal tells the PR why the last attempt refused to deploy it, and
// returns the skipped outcome and its reason; "" when it was not refused.
func (p *reconcilePlan) refusal(ctx context.Context, api ReconcileAPI) (string, string) {
	var body string
	var o, r string
	switch {
	case p.held != nil:
		body = preview.BuildServiceHeldSummary(p.t.Slug, p.held.service, p.held.holder, p.catalog.Label)
		o, r = reconcileSkipped(ctx, p.ev, ServiceHeldReason, slog.String("holder", p.held.holder))
	case p.limited:
		body = preview.BuildPreviewLimitSummary(p.t.Slug, p.catalog.MaxPreviews)
		o, r = reconcileSkipped(ctx, p.ev, PreviewLimitReason, slog.Int("max_previews", p.catalog.MaxPreviews))
	default:
		return "", ""
	}
	if err := api.Forge.UpsertComment(ctx, p.ev.Owner, p.ev.Repo, p.ev.Number, body); err != nil {
		conflictCommentFailed(ctx, p.ev, err)
	}
	return o, r
}

// logIgnored logs each named label of t whose name has no slug.
func logIgnored(ctx context.Context, ev Event, t preview.Resolution) {
	for _, label := range t.Ignored {
		slog.InfoContext(ctx, "preview label ignored",
			slog.String("repo", ev.FullName),
			slog.Int("pr_number", ev.Number),
			slog.String("label", label),
		)
	}
}

// cleanupSlugs returns, sorted and deduplicated, the previews the PR may still
// be pinned in: its branch slug, every named slug of t and, on unlabeled, the
// slug of the removed label. Empty slugs are dropped, and so is t.Slug when the
// PR is deployed there.
func cleanupSlugs(t preview.Resolution, pr *forge.PullRequest, ev Event, label string) []string {
	branch := preview.Slugify(pr.HeadRef)
	slugs := append([]string{branch}, t.Named...)
	if ev.Action == actionUnlabeled {
		slug, named, plain := preview.ParseLabel(ev.Label, label)
		switch {
		case named:
			slugs = append(slugs, slug)
		case plain:
			slugs = append(slugs, branch)
		}
	}
	deploy := pr.Open && t.Slug != ""
	slugs = slices.DeleteFunc(slugs, func(s string) bool { return s == "" || (deploy && s == t.Slug) })
	slices.Sort(slugs)
	return slices.Compact(slugs)
}

// cleanupReason returns why the PR leaves the previews of cleanupSlugs.
func cleanupReason(t preview.Resolution, pr *forge.PullRequest, ev Event, label string) string {
	switch {
	case !pr.Open:
		return PRClosedReason
	case ev.Action == actionUnlabeled:
		return labelRemovedReason(ev.Label)
	case t.Conflict:
		return conflictReason(label)
	case t.Slug != "":
		return movedReason(t.Slug)
	}
	return labelRemovedReason(label)
}

func labelRemovedReason(label string) string {
	return fmt.Sprintf("label `%s` removed", label)
}

func conflictReason(label string) string {
	return fmt.Sprintf("conflicting `%s:` labels", label)
}

func movedReason(slug string) string {
	return fmt.Sprintf("moved to preview `%s`", slug)
}

// touchedServices returns, in catalog order, the services of its repo,
// services, that the PR touches: a service without paths always; a service
// with paths when a file the PR changes, or the path it was renamed from,
// matches one of them. The PR's files are listed only when one of services
// has paths and the PR changes at most forge.MaxListedFiles
// files; otherwise every service is touched. A failed listing is returned.
func touchedServices(
	ctx context.Context,
	api ReconcileAPI,
	catalog *store.ServiceCatalog,
	ev Event,
	pr *forge.PullRequest,
	services []string,
) ([]string, error) {
	filtered := slices.ContainsFunc(services, func(name string) bool { return catalog.Paths[name] != nil })
	if !filtered || pr.ChangedFiles > forge.MaxListedFiles {
		return services, nil
	}
	files, err := api.Forge.ListChangedFiles(ctx, ev.Owner, ev.Repo, ev.Number)
	if err != nil {
		return nil, err
	}
	var touched []string
	for _, name := range services {
		patterns := catalog.Paths[name]
		if patterns == nil || touches(patterns, files) {
			touched = append(touched, name)
		}
	}
	return touched, nil
}

// touches reports whether a file of files, or the path it was renamed from,
// matches one of patterns. The patterns are valid: ParseCatalog checked them.
func touches(patterns []string, files []forge.ChangedFile) bool {
	match := func(name string) bool {
		return name != "" && slices.ContainsFunc(patterns, func(p string) bool {
			return doublestar.MatchUnvalidated(p, name)
		})
	}
	return slices.ContainsFunc(files, func(f forge.ChangedFile) bool {
		return match(f.Filename) || match(f.PreviousFilename)
	})
}

// heldBy returns the first entry of vals, in file order, that pins one of
// services for a PR other than self: its service, its holder's PR URL and
// true. It returns false when there is none.
func heldBy(vals *preview.PreviewValues, services []string, self Target) (string, string, bool) {
	for _, svc := range vals.Services {
		if !slices.Contains(services, svc.Name) || svc.CommitSha == "" || svc.Metadata == nil {
			continue
		}
		owner, repo, number, ok := preview.ParsePRURL(svc.Metadata.PRURL)
		if !ok {
			continue
		}
		if !strings.EqualFold(owner, self.Owner) || !strings.EqualFold(repo, self.Repo) || number != self.Number {
			return svc.Name, svc.Metadata.PRURL, true
		}
	}
	return "", "", false
}

// postLabelConflict tells an open PR whose named labels conflict to keep one,
// and returns the skipped outcome and its reason. It returns "" when there is
// no conflict.
func postLabelConflict(
	ctx context.Context,
	api TeardownAPI,
	ev Event,
	pr *forge.PullRequest,
	label string,
	t preview.Resolution,
) (string, string) {
	if !pr.Open || !t.Conflict {
		return "", ""
	}
	body := preview.BuildLabelConflictSummary(label, t.NamedLabels)
	if err := api.Forge.UpsertComment(ctx, ev.Owner, ev.Repo, ev.Number, body); err != nil {
		conflictCommentFailed(ctx, ev, err)
	}
	return reconcileSkipped(ctx, ev, LabelConflictReason)
}

// payloadTeardown tears down a closed PR from its payload alone, after err
// prevented reading the catalog or the PR: from its head branch's slug and
// from the slug of each named label of label in the payload, in one
// teardown operation.
func payloadTeardown(ctx context.Context, api TeardownAPI, ev Event, label string, err error) {
	slog.WarnContext(ctx, "preview reconcile fell back to payload teardown",
		slog.String("repo", ev.FullName),
		slog.Int("pr_number", ev.Number),
		slog.String("err", err.Error()),
	)
	branch := preview.Slugify(ev.HeadRef)
	var slugs []string
	if branch != "" {
		slugs = append(slugs, branch)
	}
	for _, slug := range preview.ResolveTarget(ev.Labels, ev.HeadRef, label).Named {
		if slug != branch {
			slugs = append(slugs, slug)
		}
	}
	if len(slugs) > 0 {
		teardownPRs(ctx, api, ev.Target, slugs, PRClosedReason)
	}
}

// payloadSkipReason returns why ev cannot concern label or its named labels,
// or "" when the PR must be read.
func payloadSkipReason(ev Event, label string) string {
	switch ev.Action {
	case "labeled", actionUnlabeled:
		if !previewLabel(ev.Label, label) {
			return OtherLabelReason
		}
	case "opened", "reopened", "synchronize":
		if !hasPreviewLabel(ev.Labels, label) {
			return NotLabeledReason
		}
	}
	return ""
}

// previewLabel reports whether name is the plain label or a named label of
// label with a non-empty slug.
func previewLabel(name, label string) bool {
	slug, _, plain := preview.ParseLabel(name, label)
	return plain || slug != ""
}

// hasPreviewLabel reports whether one of labels is a preview label of label,
// as previewLabel says.
func hasPreviewLabel(labels []string, label string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return previewLabel(l, label) })
}

// afterDeploy handles the deploys of the last attempt, pushed as res: it
// handles the unpin of the PR's untouched services (afterTeardown, its
// summary left to the deploy's when a service was written), logs each
// service, records a created preview, and posts the preview's summary once
// on its open PRs. It returns, as reconcile does, OutcomeDeployed when at
// least one service was written, OutcomeFailed when none was and at least
// one failed, and otherwise, every service being unchanged, the skip of
// reconcileSkipped with reason alreadyDeployedReason.
func (p *reconcilePlan) afterDeploy(ctx context.Context, api ReconcileAPI, res store.Result) (string, string) {
	written := p.written()
	if p.unpin != nil {
		afterTeardown(ctx, api.teardownAPI(), p.unpin, res, nil, len(written) == 0)
	}
	failed, created := false, false
	for _, d := range p.deploys {
		attrs := []any{
			slog.String("slug", p.t.Slug),
			slog.String("service", d.service),
			slog.String("repo", p.ev.FullName),
			slog.Int("pr_number", p.ev.Number),
			slog.String("commit_sha", p.pr.HeadSHA),
		}
		switch {
		case d.err != nil:
			failed = true
			slog.ErrorContext(ctx, "preview deploy failed", append(attrs, slog.String("err", d.err.Error()))...)
		case d.result.Unchanged:
			slog.InfoContext(ctx, "preview deploy unchanged", attrs...)
		default:
			created = created || d.result.Created
			slog.InfoContext(ctx, "preview deploy", append(attrs, slog.String("gitops_commit_url", res.URL))...)
		}
	}
	if created {
		api.Recorder.PreviewCreated()
	}
	switch {
	case len(written) > 0:
		last := written[len(written)-1].result
		summary := preview.BuildSummary(
			p.t.Slug, last.Values, p.catalog.URLs, p.catalog.ArgoCDURL, last.Excluded, res.Subject, res.URL,
		)
		if fanErr := api.FanOut(ctx, api.Forge, last.Values, summary); fanErr != nil {
			slog.WarnContext(ctx, "preview deploy comment failed",
				slog.String("slug", p.t.Slug),
				slog.String("repo", p.ev.FullName),
				slog.Int("pr_number", p.ev.Number),
				slog.String("err", fanErr.Error()),
			)
		}
		return OutcomeDeployed, ""
	case failed:
		return OutcomeFailed, ""
	}
	return reconcileSkipped(ctx, p.ev, alreadyDeployedReason)
}

// reconcileSkipped logs a skipped reconcile and returns the skipped outcome
// with reason.
func reconcileSkipped(ctx context.Context, ev Event, reason string, extra ...slog.Attr) (string, string) {
	args := []any{
		slog.String("repo", ev.FullName),
		slog.Int("pr_number", ev.Number),
		slog.String("action", ev.Action),
		slog.String("reason", reason),
	}
	for _, a := range extra {
		args = append(args, a)
	}
	slog.InfoContext(ctx, "preview reconcile skipped", args...)
	return OutcomeSkipped, reason
}

func conflictCommentFailed(ctx context.Context, ev Event, err error) {
	slog.WarnContext(ctx, "preview conflict comment failed",
		slog.String("repo", ev.FullName),
		slog.Int("pr_number", ev.Number),
		slog.String("err", err.Error()),
	)
}

// reconcileFailed logs a failed reconcile and returns the failed outcome.
func reconcileFailed(ctx context.Context, ev Event, err error) (string, string) {
	slog.ErrorContext(ctx, "preview reconcile failed",
		slog.String("repo", ev.FullName),
		slog.Int("pr_number", ev.Number),
		slog.String("err", err.Error()),
	)
	return OutcomeFailed, ""
}
