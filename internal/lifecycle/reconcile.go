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

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/preview"
)

// actionUnlabeled is the webhook action of a removed label.
const actionUnlabeled = "unlabeled"

// alreadyDeployedReason is the skip reason of a reconcile whose every service
// was already deployed as it would be written.
const alreadyDeployedReason = "already deployed"

// noServiceReason is the skip reason of a reconcile whose PR changes no
// service of its repo: no changed file matches their paths.
const noServiceReason = "no service changed"

// previewLimitReason is the skip reason of a reconcile refused because
// creating its preview would exceed preview.maxPreviews.
const previewLimitReason = "preview limit reached"

// Reconcile outcomes, the outcome label of Recorder.Reconcile.
const (
	outcomeDeployed = "deployed"
	outcomeSkipped  = "skipped"
	outcomeFailed   = "failed"
	outcomeTornDown = "torn_down"
)

// ReconcileAPI is the subset of gitops.Client used by ReconcilePR.
type ReconcileAPI interface {
	DeployAPI
	TeardownAPI
	FetchServiceCatalog(ctx context.Context) (*gitops.ServiceCatalog, error)
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*gitops.PullRequest, error)
	GetPreview(ctx context.Context, slug string) (*preview.PreviewValues, string, error)
	ListPreviews(ctx context.Context) ([]string, error)
	ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]gitops.ChangedFile, error)
	UnpinServicesWithRetry(
		ctx context.Context,
		slug, owner, repo string,
		prNumber int,
		services []string,
	) (*gitops.TeardownResult, error)
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
// catalog, the PR or the target preview can't be read, or the previews can't
// be listed, on a non-closed event, when the PR's files can't be listed or the
// unpin of its untouched services fails, on any event, or when no service
// write succeeded and at least one failed; otherwise skipped (with its
// reason) when the reconcile was skipped; otherwise deployed when a service
// write succeeded; otherwise torn_down.
func ReconcilePR(ctx context.Context, api ReconcileAPI, ev Event) {
	o, r := reconcile(ctx, api, ev)
	api.Reconcile(o, r)
}

// reconcile is ReconcilePR without the recording. It returns the outcome and,
// when the outcome is skipped, the reason ("" otherwise).
func reconcile(ctx context.Context, api ReconcileAPI, ev Event) (string, string) {
	closed := ev.Action == "closed"
	catalog, err := api.FetchServiceCatalog(ctx)
	if err != nil {
		if closed {
			payloadTeardown(ctx, api, ev, gitops.DefaultLabel, err)
			return outcomeTornDown, ""
		}
		return reconcileFailed(ctx, ev, err)
	}
	services := catalog.ServicesForRepo(ev.Owner, ev.Repo)
	if !closed {
		if reason := payloadSkipReason(ev, catalog.Label); reason != "" {
			return reconcileSkipped(ctx, ev, reason)
		}
		if len(services) == 0 {
			return reconcileSkipped(ctx, ev, "repo not in catalog")
		}
	}
	pr, err := api.GetPullRequest(ctx, ev.Owner, ev.Repo, ev.Number)
	if err != nil {
		if closed {
			payloadTeardown(ctx, api, ev, catalog.Label, err)
			return outcomeTornDown, ""
		}
		return reconcileFailed(ctx, ev, err)
	}

	t := preview.ResolveTarget(pr.Labels, pr.HeadRef, catalog.Label)
	logIgnored(ctx, ev, t)
	deploy := pr.Open && t.Slug != ""
	if (deploy || !closed) && !strings.EqualFold(pr.HeadFullName, pr.FullName) {
		return reconcileSkipped(ctx, ev, "fork pr")
	}
	if deploy && len(services) == 0 {
		return reconcileSkipped(ctx, ev, "repo not in catalog")
	}
	removed := cleanup(ctx, api, ev, pr, t, catalog.Label)
	repoServices := services
	if deploy {
		if services, err = touchedServices(ctx, api, catalog, ev, pr, services); err != nil {
			return reconcileFailed(ctx, ev, err)
		}
		if len(services) == 0 {
			return noServiceChanged(ctx, api, ev, t.Slug)
		}
	}
	o, r, vals := refused(ctx, api, catalog, ev, pr, t, services)
	if o != "" {
		return o, r
	}
	if deploy {
		unpinned, unpinErr := unpinUntouched(ctx, api, ev, t.Slug, vals, repoServices, services)
		if unpinErr != nil {
			return reconcileFailed(ctx, ev, unpinErr)
		}
		removed = removed || unpinned
	}
	outcome, reason := outcomeTornDown, ""
	if deploy {
		// After a teardown in this reconcile, the deploy always writes and
		// comments, so its summary stays the last write to the PR's comment.
		outcome, reason = deployServices(ctx, api, catalog, services, ev, pr, t.Slug, !removed)
	}
	// A conflict leaves the PR without a target, so nothing was deployed.
	if o, r := postLabelConflict(ctx, api, ev, pr, catalog.Label, t); o != "" {
		return o, r
	}
	return outcome, reason
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

// cleanup tears the PR down from every slug of cleanupSlugs, with
// cleanupReason. It reports whether any teardown removed the PR from a
// preview.
func cleanup(
	ctx context.Context,
	api TeardownAPI,
	ev Event,
	pr *gitops.PullRequest,
	t preview.Resolution,
	label string,
) bool {
	reason := cleanupReason(t, pr, ev, label)
	removed := false
	for _, slug := range cleanupSlugs(t, pr, ev, label) {
		tt := ev.Target
		tt.Slug = slug
		if teardown(ctx, api, tt, reason, triggerWebhook, true) {
			removed = true
		}
	}
	return removed
}

// cleanupSlugs returns, sorted and deduplicated, the previews the PR may still
// be pinned in: its branch slug, every named slug of t and, on unlabeled, the
// slug of the removed label. Empty slugs are dropped, and so is t.Slug when the
// PR is deployed there.
func cleanupSlugs(t preview.Resolution, pr *gitops.PullRequest, ev Event, label string) []string {
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
func cleanupReason(t preview.Resolution, pr *gitops.PullRequest, ev Event, label string) string {
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
// has paths and the PR changes at most gitops.MaxListedPullRequestFiles
// files; otherwise every service is touched. A failed listing is returned.
func touchedServices(
	ctx context.Context,
	api ReconcileAPI,
	catalog *gitops.ServiceCatalog,
	ev Event,
	pr *gitops.PullRequest,
	services []string,
) ([]string, error) {
	filtered := slices.ContainsFunc(services, func(name string) bool { return catalog.Paths[name] != nil })
	if !filtered || pr.ChangedFiles > gitops.MaxListedPullRequestFiles {
		return services, nil
	}
	files, err := api.ListPullRequestFiles(ctx, ev.Owner, ev.Repo, ev.Number)
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
func touches(patterns []string, files []gitops.ChangedFile) bool {
	match := func(name string) bool {
		return name != "" && slices.ContainsFunc(patterns, func(p string) bool {
			return doublestar.MatchUnvalidated(p, name)
		})
	}
	return slices.ContainsFunc(files, func(f gitops.ChangedFile) bool {
		return match(f.Filename) || match(f.PreviousFilename)
	})
}

// noServiceChanged handles an open PR with the target slug that touches no
// service of its repo: it is torn down from slug, without editing its comment,
// then told that nothing is deployed, and the reconcile is skipped with
// noServiceReason. A failed teardown or comment does not change the outcome.
func noServiceChanged(ctx context.Context, api ReconcileAPI, ev Event, slug string) (string, string) {
	tt := ev.Target
	tt.Slug = slug
	teardown(ctx, api, tt, noServiceReason, triggerWebhook, false)
	body := preview.BuildNoServiceSummary(slug)
	if err := api.UpsertPRComment(ctx, ev.Owner, ev.Repo, ev.Number, body); err != nil {
		conflictCommentFailed(ctx, ev, err)
	}
	return reconcileSkipped(ctx, ev, noServiceReason)
}

// unpinUntouched unpins, in the preview slug whose values refused read, the
// PR's entries for the services of its repo, repoServices, that are not in
// touched, in one write (unpinServices). Nothing is written when vals is nil
// (the preview does not exist) or the PR pins none of them there. It reports
// whether it unpinned an entry, and the write's error.
func unpinUntouched(
	ctx context.Context,
	api ReconcileAPI,
	ev Event,
	slug string,
	vals *preview.PreviewValues,
	repoServices, touched []string,
) (bool, error) {
	if vals == nil {
		return false, nil
	}
	untouched := slices.DeleteFunc(slices.Clone(repoServices), func(name string) bool {
		return slices.Contains(touched, name)
	})
	_, names, _ := preview.UnpinPRServices(*vals, ev.Owner, ev.Repo, ev.Number, untouched)
	if len(names) == 0 {
		return false, nil
	}
	tt := ev.Target
	tt.Slug = slug
	return unpinServices(ctx, api, tt, names)
}

// refused reports whether the PR must not be deployed to t.Slug, as the
// reconcile's outcome and reason: refusedByHolder's, then, when the preview
// does not exist, refusedByLimit's. It returns "" when it is not refused. Its
// third result is the preview t.Slug that refusedByHolder read, nil when the
// preview does not exist or nothing was read.
func refused(
	ctx context.Context,
	api ReconcileAPI,
	catalog *gitops.ServiceCatalog,
	ev Event,
	pr *gitops.PullRequest,
	t preview.Resolution,
	services []string,
) (string, string, *preview.PreviewValues) {
	o, r, vals := refusedByHolder(ctx, api, ev, pr, t, services, catalog.Label)
	if o != "" || vals != nil {
		return o, r, vals
	}
	o, r = refusedByLimit(ctx, api, ev, pr, t, catalog.MaxPreviews)
	return o, r, nil
}

// refusedByHolder reports whether the PR must not be deployed to t.Slug, as
// the reconcile's outcome and reason: skipped when one of its services is
// already pinned there by another PR (it is then told so), failed when the
// preview can't be read, and "" when it is not refused. It reads nothing
// unless the PR is open and has a target. Its third result is the preview
// t.Slug it read, nil when the preview does not exist or nothing was read.
func refusedByHolder(
	ctx context.Context,
	api ReconcileAPI,
	ev Event,
	pr *gitops.PullRequest,
	t preview.Resolution,
	services []string,
	label string,
) (string, string, *preview.PreviewValues) {
	if !pr.Open || t.Slug == "" {
		return "", "", nil
	}
	vals, _, err := api.GetPreview(ctx, t.Slug)
	if errors.Is(err, gitops.ErrNotFound) {
		return "", "", nil
	}
	if err != nil {
		o, r := reconcileFailed(ctx, ev, err)
		return o, r, nil
	}
	service, holder, held := heldBy(vals, services, ev.Target)
	if !held {
		return "", "", vals
	}
	body := preview.BuildServiceHeldSummary(t.Slug, service, holder, label)
	if upsertErr := api.UpsertPRComment(ctx, ev.Owner, ev.Repo, ev.Number, body); upsertErr != nil {
		conflictCommentFailed(ctx, ev, upsertErr)
	}
	o, r := reconcileSkipped(ctx, ev, "service held by another pr", slog.String("holder", holder))
	return o, r, vals
}

// refusedByLimit reports whether the PR must not create the preview t.Slug,
// which does not exist, because maxPreviews previews already exist, as the
// reconcile's outcome and reason: skipped when the cap is reached (the PR is
// then told so), failed when the previews can't be listed, and "" when it is
// not refused. It reads nothing unless the PR is open, has a target and
// maxPreviews is set.
func refusedByLimit(
	ctx context.Context,
	api ReconcileAPI,
	ev Event,
	pr *gitops.PullRequest,
	t preview.Resolution,
	maxPreviews int,
) (string, string) {
	if !pr.Open || t.Slug == "" || maxPreviews <= 0 {
		return "", ""
	}
	slugs, err := api.ListPreviews(ctx)
	if err != nil {
		return reconcileFailed(ctx, ev, err)
	}
	if len(slugs) < maxPreviews {
		return "", ""
	}
	body := preview.BuildPreviewLimitSummary(t.Slug, maxPreviews)
	if upsertErr := api.UpsertPRComment(ctx, ev.Owner, ev.Repo, ev.Number, body); upsertErr != nil {
		conflictCommentFailed(ctx, ev, upsertErr)
	}
	return reconcileSkipped(ctx, ev, previewLimitReason, slog.Int("max_previews", maxPreviews))
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
	pr *gitops.PullRequest,
	label string,
	t preview.Resolution,
) (string, string) {
	if !pr.Open || !t.Conflict {
		return "", ""
	}
	body := preview.BuildLabelConflictSummary(label, t.NamedLabels)
	if err := api.UpsertPRComment(ctx, ev.Owner, ev.Repo, ev.Number, body); err != nil {
		conflictCommentFailed(ctx, ev, err)
	}
	return reconcileSkipped(ctx, ev, "conflicting preview labels")
}

// payloadTeardown tears down a closed PR from its payload alone, after err
// prevented reading the catalog or the PR: from its head branch's slug and
// from the slug of each named label of label in the payload.
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
	for _, slug := range slugs {
		t := ev.Target
		t.Slug = slug
		TeardownPR(ctx, api, t, PRClosedReason, triggerWebhook)
	}
}

// payloadSkipReason returns why ev cannot concern label or its named labels,
// or "" when the PR must be read.
func payloadSkipReason(ev Event, label string) string {
	switch ev.Action {
	case "labeled", actionUnlabeled:
		if !previewLabel(ev.Label, label) {
			return "other label"
		}
	case "opened", "reopened", "synchronize":
		if !slices.ContainsFunc(ev.Labels, func(l string) bool { return previewLabel(l, label) }) {
			return "not labeled"
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

// deployServices deploys each of services, in order, at pr's head SHA in the
// preview slug. A failed service is logged and the next one is still deployed.
// With skipUnchanged, a service whose write would change nothing but its
// updated-at is not written nor commented, and is logged as unchanged. It
// returns, as reconcile does, outcomeDeployed when at least one service write
// succeeded, outcomeFailed when none did and at least one failed, and
// otherwise, every service being unchanged, the skip of reconcileSkipped with
// reason alreadyDeployedReason.
func deployServices(
	ctx context.Context,
	api DeployAPI,
	catalog *gitops.ServiceCatalog,
	services []string,
	ev Event,
	pr *gitops.PullRequest,
	slug string,
	skipUnchanged bool,
) (string, string) {
	written, failed := false, false
	timestamp := time.Now().UTC().Format(time.RFC3339)
	for _, service := range services {
		attrs := func(extra ...any) []any {
			return append([]any{
				slog.String("slug", slug),
				slog.String("service", service),
				slog.String("repo", ev.FullName),
				slog.Int("pr_number", ev.Number),
			}, extra...)
		}
		err := ctx.Err()
		var result *gitops.WriteResult
		var fanErr error
		if err == nil {
			result, fanErr, err = DeployPR(ctx, api, catalog, slug, preview.DeployInputs{
				ServiceName:    service,
				HeadRef:        pr.HeadRef,
				CommitSha:      pr.HeadSHA,
				PRAuthor:       pr.Author,
				PRURL:          pr.URL,
				PRNumber:       strconv.Itoa(ev.Number),
				Timestamp:      timestamp,
				WorkflowRunURL: "",
			}, skipUnchanged)
		}
		if err != nil {
			failed = true
			slog.ErrorContext(ctx, "preview deploy failed",
				attrs(slog.String("commit_sha", pr.HeadSHA), slog.String("err", err.Error()))...)
			continue
		}
		if result.Unchanged {
			slog.InfoContext(ctx, "preview deploy unchanged", attrs(slog.String("commit_sha", pr.HeadSHA))...)
			continue
		}
		written = true
		slog.InfoContext(ctx, "preview deploy",
			attrs(slog.String("commit_sha", pr.HeadSHA), slog.String("gitops_commit_url", result.CommitURL))...)
		if fanErr != nil {
			slog.WarnContext(ctx, "preview deploy comment failed", attrs(slog.String("err", fanErr.Error()))...)
		}
	}
	switch {
	case written:
		return outcomeDeployed, ""
	case failed:
		return outcomeFailed, ""
	}
	return reconcileSkipped(ctx, ev, alreadyDeployedReason)
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
	return outcomeSkipped, reason
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
	return outcomeFailed, ""
}
