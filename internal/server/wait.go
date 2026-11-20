package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/status"
)

// waitTimeout is the longest a wait blocks before it answers its current
// state.
const waitTimeout = 25 * time.Second

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

// final reports whether a wait in state answers at once.
func final(state string) bool {
	switch state {
	case stateReady, stateFailed, stateRefused, stateSkipped:
		return true
	}
	return false
}

// WaitResponse is the body of a wait's answer: its state, the reason of a
// failed, refused or skipped state, the preview and the SHA waited for when
// known, and the wait set's entries with the URLs of those that have one.
type WaitResponse struct {
	State    string            `json:"state"`
	Reason   string            `json:"reason,omitempty"`
	Slug     string            `json:"slug,omitempty"`
	SHA      string            `json:"sha,omitempty"`
	Services []WaitEntryJSON   `json:"services"`
	URLs     map[string]string `json:"urls"`
}

// WaitEntryJSON is an entry of a wait set: its latest status ("unknown"
// when none) and revision, whether it is ready, and its URL.
type WaitEntryJSON struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
	Ready    bool   `json:"ready"`
	URL      string `json:"url,omitempty"`
}

// newWaitResponse returns a response in state with no entry.
func newWaitResponse(state, reason, slug, sha string) *WaitResponse {
	return &WaitResponse{
		State: state, Reason: reason, Slug: slug, SHA: sha,
		Services: []WaitEntryJSON{}, URLs: map[string]string{},
	}
}

// prQuery is the query of GET /v1/wait.
type prQuery struct {
	owner, repo string
	number      int
	sha         string
}

// parsePRQuery validates the query of GET /v1/wait: repo as owner/repo
// (exactly one "/", both parts non-empty), pr a positive integer and an
// optional sha.
func parsePRQuery(q url.Values) (prQuery, error) {
	repo, pr := q.Get("repo"), q.Get("pr")
	if repo == "" || pr == "" {
		return prQuery{}, errors.New("repo and pr are required")
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return prQuery{}, errors.New("repo must be owner/repo")
	}
	number, err := strconv.Atoi(pr)
	if err != nil || number <= 0 {
		return prQuery{}, errors.New("pr must be a positive integer")
	}
	return prQuery{owner: owner, repo: name, number: number, sha: q.Get("sha")}, nil
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

// atPin reports whether revision is at the pin commitSha: it is non-empty
// and starts with commitSha.
func atPin(revision, commitSha string) bool {
	return revision != "" && strings.HasPrefix(revision, commitSha)
}

// handleWait answers GET /v1/wait: the state of a PR's preview, long-polled.
func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
	q, err := parsePRQuery(r.URL.Query())
	if err != nil {
		writeJSONError(r.Context(), w, http.StatusBadRequest, err.Error())
		return
	}
	s.longPoll(w, r, func(ctx context.Context) (*WaitResponse, error) { return s.evalPR(ctx, q) })
}

// handleSlugWait answers GET /v1/previews/{slug}/wait: the state of a
// preview, long-polled.
func (s *Server) handleSlugWait(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	s.longPoll(w, r, func(ctx context.Context) (*WaitResponse, error) { return s.evalSlug(ctx, slug) })
}

// longPoll answers r with eval's response: at once when it is final,
// errNoPreview (404) or another error (503); otherwise once a
// re-evaluation, on each Notify of s.wake, changes its state or reason or is
// final or an error. When
// waitTimeout passes, or the HTTP server's shutdown starts, it answers the
// current response. The client's disconnect ends it without an answer.
func (s *Server) longPoll(w http.ResponseWriter, r *http.Request, eval func(context.Context) (*WaitResponse, error)) {
	ctx := r.Context()
	route := routePattern(r)
	timer := time.NewTimer(s.waitTimeout)
	defer timer.Stop()
	woken := s.wake.wait()
	first, err := eval(ctx)
	current := first
	for {
		switch {
		case errors.Is(err, errNoPreview):
			writeJSONError(ctx, w, http.StatusNotFound, err.Error())
			return
		case err != nil:
			readFailed(ctx, w, route, err)
			return
		case final(current.State) || current.State != first.State || current.Reason != first.Reason:
			writeJSON(ctx, w, http.StatusOK, current)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			writeJSON(ctx, w, http.StatusOK, current)
			return
		case <-s.shutdown:
			writeJSON(ctx, w, http.StatusOK, current)
			return
		case <-woken:
			woken = s.wake.wait()
			current, err = eval(ctx)
		}
	}
}

// evalPR evaluates the wait of the PR of q, in this order: refused when the
// catalog has no service of its repo; the state its latest recorded outcome
// decides, when that outcome's head SHA matches q's sha or q has none;
// pending when no preview has an entry pinned by the PR (at q's sha, when
// set); otherwise the state of the PR's wait set in that preview.
func (s *Server) evalPR(ctx context.Context, q prQuery) (*WaitResponse, error) {
	catalog, err := s.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if len(catalog.ServicesForRepo(q.owner, q.repo)) == 0 {
		return newWaitResponse(stateRefused, lifecycle.RepoNotInCatalogReason, "", q.sha), nil
	}
	if o, ok := s.outcomes.get(
		outcomeKey(q.owner, q.repo, q.number),
	); ok &&
		(q.sha == "" || shaMatch(q.sha, o.HeadSHA)) {
		if state, reason, decided := outcomeState(o.ReconcileResult); decided {
			return newWaitResponse(state, reason, "", cmp.Or(q.sha, o.HeadSHA)), nil
		}
	}
	views, err := s.listPreviews(ctx, catalog)
	if err != nil {
		return nil, err
	}
	byPR := func(svc *preview.ServiceEntry) bool {
		return svc.CommitSha != "" && svc.Metadata != nil &&
			preview.MatchesPR(svc.Metadata.PRURL, q.owner, q.repo, q.number)
	}
	for _, v := range views {
		for i := range v.vals.Services {
			svc := &v.vals.Services[i]
			if !byPR(svc) || (q.sha != "" && !shaMatch(q.sha, svc.CommitSha)) {
				continue
			}
			return s.waitSetState(v, byPR, cmp.Or(q.sha, svc.CommitSha)), nil
		}
	}
	return newWaitResponse(statePending, "", "", q.sha), nil
}

// evalSlug evaluates the wait of the preview slug: errNoPreview when it does
// not exist, pending when it has no pinned entry, otherwise the state of its
// wait set.
func (s *Server) evalSlug(ctx context.Context, slug string) (*WaitResponse, error) {
	v, err := s.readPreview(ctx, slug)
	if err != nil {
		return nil, err
	}
	pinned := func(svc *preview.ServiceEntry) bool { return svc.CommitSha != "" }
	for i := range v.vals.Services {
		if pinned(&v.vals.Services[i]) {
			return s.waitSetState(*v, pinned, ""), nil
		}
	}
	return newWaitResponse(statePending, "", v.slug, ""), nil
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
		case lifecycle.ServiceHeldReason, lifecycle.PreviewLimitReason, lifecycle.LabelConflictReason,
			lifecycle.ForkPRReason, lifecycle.RepoNotInCatalogReason:
			return stateRefused, res.Reason, true
		case lifecycle.NoServiceReason, lifecycle.NotLabeledReason:
			return stateSkipped, res.Reason, true
		}
	}
	return "", "", false
}

// waitSetState returns the state of the wait set of v whose roots are its
// pinned service entries for which root is true: those entries and every
// service and dependency in the transitive needs of the catalog services
// among them (preview.Catalog.NeedsClosure). It is failed when an entry is
// failed, with the first one, in file order, as the reason; ready when every
// entry is ready; otherwise progressing.
func (s *Server) waitSetState(v previewView, root func(*preview.ServiceEntry) bool, sha string) *WaitResponse {
	inCatalog := map[string]bool{}
	for _, name := range v.catalog.Services {
		inCatalog[name] = true
	}
	set := map[string]bool{}
	var roots []string
	for i := range v.vals.Services {
		svc := &v.vals.Services[i]
		if svc.CommitSha == "" || !root(svc) {
			continue
		}
		set[svc.Name] = true
		if inCatalog[svc.Name] {
			roots = append(roots, svc.Name)
		}
	}
	for name := range v.catalog.NeedsClosure(roots) {
		set[name] = true
	}

	statuses := s.previewStatus(v.slug)
	res := newWaitResponse(stateReady, "", v.slug, sha)
	for _, e := range entries(v) {
		if !set[e.name] {
			continue
		}
		st, known := statuses[e.name]
		state := entryState(e, st, known)
		j := WaitEntryJSON{Name: e.name, Kind: e.kind, Status: statusUnknown, Ready: state == stateReady, URL: e.url}
		if known {
			j.Status, j.Revision = st.Status, st.Revision
		}
		res.Services = append(res.Services, j)
		if e.url != "" {
			res.URLs[e.name] = e.url
		}
		switch {
		case state == stateFailed && res.State != stateFailed:
			res.State, res.Reason = stateFailed, fmt.Sprintf("%s is %s at %s", e.name, st.Status, st.Revision)
		case state != stateReady && res.State == stateReady:
			res.State = stateProgressing
		}
	}
	return res
}

// entryState returns the state of e, whose latest status is st when known:
// a pinned entry is ready, or failed, when its status is ready, or degraded
// or sync-failed, at a revision at its pin; any other entry when its status
// is. Anything else is progressing.
func entryState(e entry, st status.Entry, known bool) string {
	if !known || (e.pinned() && !atPin(st.Revision, e.svc.CommitSha)) {
		return stateProgressing
	}
	switch st.Status {
	case status.Ready:
		return stateReady
	case status.Degraded, status.SyncFailed:
		return stateFailed
	}
	return stateProgressing
}
