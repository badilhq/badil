package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/badilhq/badil/internal/api"
	"github.com/badilhq/badil/internal/app"
)

// waitTimeout is the longest a wait blocks before it answers its current
// state.
const waitTimeout = 25 * time.Second

// waitResponse returns the body of the wait answer r.
func waitResponse(r *app.WaitResult) *api.WaitResponse {
	out := &api.WaitResponse{
		State: r.State, Reason: r.Reason, Slug: r.Slug, SHA: r.SHA,
		Services: make([]api.WaitEntryJSON, 0, len(r.Services)), URLs: r.URLs,
	}
	for _, e := range r.Services {
		out.Services = append(out.Services, api.WaitEntryJSON{
			Name: e.Name, Kind: e.Kind, Status: e.Status, Revision: e.Revision, Ready: e.Ready, URL: e.URL,
		})
	}
	return out
}

// parsePRQuery validates the query of GET /v1/wait: repo as owner/repo
// (exactly one "/", both parts non-empty), pr a positive integer and an
// optional sha.
func parsePRQuery(q url.Values) (app.PRQuery, error) {
	repo, pr := q.Get("repo"), q.Get("pr")
	if repo == "" || pr == "" {
		return app.PRQuery{}, errors.New("repo and pr are required")
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return app.PRQuery{}, errors.New("repo must be owner/repo")
	}
	number, err := strconv.Atoi(pr)
	if err != nil || number <= 0 {
		return app.PRQuery{}, errors.New("pr must be a positive integer")
	}
	return app.PRQuery{Owner: owner, Repo: name, Number: number, SHA: q.Get("sha")}, nil
}

// handleWait answers GET /v1/wait: the state of a PR's preview, long-polled.
func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
	q, err := parsePRQuery(r.URL.Query())
	if err != nil {
		writeJSONError(r.Context(), w, http.StatusBadRequest, err.Error())
		return
	}
	s.longPoll(w, r, func(ctx context.Context) (*app.WaitResult, error) { return s.app.EvaluatePRWait(ctx, q) })
}

// handleSlugWait answers GET /v1/previews/{slug}/wait: the state of a
// preview, long-polled.
func (s *Server) handleSlugWait(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	s.longPoll(w, r, func(ctx context.Context) (*app.WaitResult, error) { return s.app.EvaluateSlugWait(ctx, slug) })
}

// longPoll answers r with eval's response: at once when it is final,
// app.ErrNoPreview (404) or another error (503); otherwise once a
// re-evaluation, on each Notify of the app's wake, changes its state or
// reason or is final or an error. When
// waitTimeout passes, or the HTTP server's shutdown starts, it answers the
// current response. The client's disconnect ends it without an answer.
func (s *Server) longPoll(w http.ResponseWriter, r *http.Request, eval func(context.Context) (*app.WaitResult, error)) {
	ctx := r.Context()
	route := routePattern(r)
	timer := time.NewTimer(s.waitTimeout)
	defer timer.Stop()
	woken := s.app.Wake().Wait()
	first, err := eval(ctx)
	current := first
	for {
		switch {
		case errors.Is(err, app.ErrNoPreview):
			writeJSONError(ctx, w, http.StatusNotFound, err.Error())
			return
		case err != nil:
			readFailed(ctx, w, route, err)
			return
		case current.Final() || current.State != first.State || current.Reason != first.Reason:
			writeJSON(ctx, w, http.StatusOK, waitResponse(current))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			writeJSON(ctx, w, http.StatusOK, waitResponse(current))
			return
		case <-s.shutdown:
			writeJSON(ctx, w, http.StatusOK, waitResponse(current))
			return
		case <-woken:
			woken = s.app.Wake().Wait()
			current, err = eval(ctx)
		}
	}
}
