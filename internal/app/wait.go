package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

// States of a wait.
const (
	statePending     = "pending"
	stateProgressing = "progressing"
	stateReady       = "ready"
	stateFailed      = "failed"
	stateRefused     = "refused"
	stateSkipped     = "skipped"
)

// Reasons of a wait state that no reconcile reason carries.
const (
	reasonTornDown        = "torn down"
	reasonReconcileFailed = "reconcile failed"
)

// PRQuery is the PR a wait is for, and the SHA waited for, "" when any.
type PRQuery struct {
	Owner, Repo string
	Number      int
	SHA         string
}

// WaitResult is the state of a wait: its state, the reason of a failed,
// refused or skipped state, the preview and the SHA waited for when known,
// and the wait set's entries with the URLs of those that have one.
type WaitResult struct {
	State    string
	Reason   string
	Slug     string
	SHA      string
	Services []WaitEntry
	URLs     map[string]string
}

// Final reports whether a wait in r's state answers at once.
func (r *WaitResult) Final() bool {
	switch r.State {
	case stateReady, stateFailed, stateRefused, stateSkipped:
		return true
	}
	return false
}

// WaitEntry is an entry of a wait set: its latest status (StatusUnknown when
// none) and revision, whether it is ready, and its URL.
type WaitEntry struct {
	Name     string
	Kind     string
	Status   string
	Revision string
	Ready    bool
	URL      string
}

// EvaluatePRWait evaluates the wait of the PR of q, in this order: refused
// when the catalog has no service of its repo; the state its latest recorded
// outcome decides, when that outcome's head SHA matches q's SHA or q has
// none; pending when no preview has an entry pinned by the PR (at q's SHA,
// when set); otherwise the state of the PR's wait set in that preview.
func (a *App) EvaluatePRWait(ctx context.Context, q PRQuery) (*WaitResult, error) {
	catalog, err := a.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if len(catalog.ServicesForRepo(a.forge.ParseRepoURL, q.Owner, q.Repo)) == 0 {
		return newWaitResult(stateRefused, lifecycle.RepoNotInCatalogReason, "", q.SHA), nil
	}
	target := lifecycle.Target{Owner: q.Owner, Repo: q.Repo, Number: q.Number}
	if o, ok := a.outcomes.get(lifecycle.PRKey(target)); ok && (q.SHA == "" || shaMatch(q.SHA, o.HeadSHA)) {
		if state, reason, decided := outcomeState(o.ReconcileResult); decided {
			return newWaitResult(state, reason, "", cmp.Or(q.SHA, o.HeadSHA)), nil
		}
	}
	views, err := a.listPreviews(ctx, catalog)
	if err != nil {
		return nil, err
	}
	byPR := func(svc *preview.ServiceEntry) bool {
		return svc.CommitSha != "" && svc.Metadata != nil &&
			preview.MatchesPR(a.forge.ParsePRURL, svc.Metadata.PRURL, q.Owner, q.Repo, q.Number)
	}
	for _, v := range views {
		for i := range v.Values.Services {
			svc := &v.Values.Services[i]
			if !byPR(svc) || (q.SHA != "" && !shaMatch(q.SHA, svc.CommitSha)) {
				continue
			}
			return a.waitSetState(v, byPR, cmp.Or(q.SHA, svc.CommitSha)), nil
		}
	}
	return newWaitResult(statePending, "", "", q.SHA), nil
}

// EvaluateSlugWait evaluates the wait of the preview slug: ErrNoPreview when
// it does not exist, pending when it has no pinned entry, otherwise the state
// of its wait set.
func (a *App) EvaluateSlugWait(ctx context.Context, slug string) (*WaitResult, error) {
	v, err := a.Preview(ctx, slug)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(v.Values.Services, func(e preview.ServiceEntry) bool { return e.CommitSha != "" }) {
		return a.waitSetState(*v, func(*preview.ServiceEntry) bool { return true }, ""), nil
	}
	return newWaitResult(statePending, "", v.Slug, ""), nil
}

// waitSetState returns the state of the wait set of v whose roots are its
// pinned service entries for which root is true: those entries and every
// service and dependency in the transitive needs of the catalog services
// among them (preview.Catalog.PinnedClosure). It is failed when an entry is
// failed, with the first one, in file order, as the reason; ready when every
// entry is ready; otherwise progressing.
func (a *App) waitSetState(v PreviewView, root func(*preview.ServiceEntry) bool, sha string) *WaitResult {
	set := v.Catalog.PinnedClosure(v.Values.Services, root)
	statuses := a.PreviewStatus(v.Slug)
	res := newWaitResult(stateReady, "", v.Slug, sha)
	for _, e := range v.Entries() {
		if !set[e.Name] {
			continue
		}
		st, known := statuses[e.Name]
		state := entryState(e, st, known)
		j := WaitEntry{Name: e.Name, Kind: e.Kind, Status: StatusUnknown, Ready: state == stateReady, URL: e.URL}
		if known {
			j.Status, j.Revision = st.Status, st.Revision
		}
		res.Services = append(res.Services, j)
		if e.URL != "" {
			res.URLs[e.Name] = e.URL
		}
		switch {
		case state == stateFailed && res.State != stateFailed:
			res.State, res.Reason = stateFailed, fmt.Sprintf("%s is %s at %s", e.Name, st.Status, st.Revision)
		case state != stateReady && res.State == stateReady:
			res.State = stateProgressing
		}
	}
	return res
}

// newWaitResult returns a result in state with no entry.
func newWaitResult(state, reason, slug, sha string) *WaitResult {
	return &WaitResult{
		State: state, Reason: reason, Slug: slug, SHA: sha,
		Services: []WaitEntry{}, URLs: map[string]string{},
	}
}

// shaMatch reports whether the SHAs a and b match: both are non-empty and,
// in lowercase, the longer starts with the shorter.
func shaMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = strings.ToLower(a), strings.ToLower(b)
	if len(a) < len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(a, b)
}

// outcomeState returns the wait state and reason a recorded reconcile
// outcome decides, and false when it decides none.
func outcomeState(res lifecycle.ReconcileResult) (string, string, bool) {
	switch res.Outcome {
	case lifecycle.OutcomeTornDown:
		return stateRefused, reasonTornDown, true
	case lifecycle.OutcomeFailed:
		return stateFailed, reasonReconcileFailed, true
	case lifecycle.OutcomeSkipped:
		switch res.Reason {
		case lifecycle.ServiceHeldReason,
			lifecycle.PreviewLimitReason,
			lifecycle.NamespaceInUseReason,
			lifecycle.LabellerReason,
			lifecycle.LabelConflictReason,
			lifecycle.ForkPRReason,
			lifecycle.RepoNotInCatalogReason:
			return stateRefused, res.Reason, true
		case lifecycle.NoServiceReason, lifecycle.NotLabeledReason:
			return stateSkipped, res.Reason, true
		}
	}
	return "", "", false
}

// entryState returns the state of e, whose latest status is st when known:
// a pinned entry is ready, or failed, when its status is ready, or degraded
// or sync-failed, at a revision at its pin; any other entry when its status
// is. Anything else is progressing.
func entryState(e Entry, st preview.ServiceStatus, known bool) string {
	if !known || (e.Pinned() && !preview.AtPin(st.Revision, e.Service.CommitSha)) {
		return stateProgressing
	}
	switch st.Status {
	case preview.StatusReady:
		return stateReady
	case preview.StatusDegraded, preview.StatusSyncFailed:
		return stateFailed
	}
	return stateProgressing
}
