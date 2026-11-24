package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/preview"
)

// PreviewList is the body of GET /v1/previews.
type PreviewList struct {
	Previews []PreviewJSON `json:"previews"`
}

// PreviewJSON is a preview in the read API: its file in the gitops clone and
// its status in memory. CreatedAt and UpdatedAt are the oldest created-at and
// the newest updated-at of its pinned entries, and ExpiresAt is UpdatedAt
// plus the in-server sweep's TTL; each is omitted when unknown.
type PreviewJSON struct {
	Slug      string             `json:"slug"`
	CreatedAt string             `json:"createdAt,omitempty"`
	UpdatedAt string             `json:"updatedAt,omitempty"`
	ExpiresAt string             `json:"expiresAt,omitempty"`
	Services  []PreviewEntryJSON `json:"services"`
}

// PreviewEntryJSON is a service or dependency entry of a preview.
type PreviewEntryJSON struct {
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Pinned   *PinnedJSON `json:"pinned,omitempty"`
	Branch   string      `json:"branch,omitempty"`
	URL      string      `json:"url,omitempty"`
	Status   string      `json:"status"`
	Revision string      `json:"revision,omitempty"`
	StatusAt string      `json:"statusAt,omitempty"`
}

// PinnedJSON is the pin of a pinned entry: its commit and the PR that pinned
// it.
type PinnedJSON struct {
	SHA    string `json:"sha"`
	PR     PRJSON `json:"pr"`
	Author string `json:"author"`
}

// PRJSON is a pull request: owner/repo, number and URL, from the pr-url of
// the entry.
type PRJSON struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// readFailed logs a failed read of the gitops clone on route and answers 503
// with its error. When ctx is done the client has gone and the read failed
// for that reason: it neither logs nor answers.
func readFailed(ctx context.Context, w http.ResponseWriter, route string, err error) {
	if ctx.Err() != nil {
		return
	}
	slog.Default().ErrorContext(ctx, "read failed", slog.String("route", route), slog.String("err", err.Error()))
	writeJSONError(ctx, w, http.StatusServiceUnavailable, err.Error())
}

// handleListPreviews answers GET /v1/previews with every preview of the
// gitops clone, sorted by slug.
func (s *Server) handleListPreviews(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	views, err := s.app.Previews(ctx)
	if err != nil {
		readFailed(ctx, w, routePattern(r), err)
		return
	}
	out := PreviewList{Previews: make([]PreviewJSON, 0, len(views))}
	for _, v := range views {
		out.Previews = append(out.Previews, s.previewJSON(v))
	}
	writeJSON(ctx, w, http.StatusOK, out)
}

// handleGetPreview answers GET /v1/previews/{slug} with the preview, or 404.
func (s *Server) handleGetPreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, err := s.app.Preview(ctx, chi.URLParam(r, "slug"))
	switch {
	case errors.Is(err, app.ErrNoPreview):
		writeJSONError(ctx, w, http.StatusNotFound, err.Error())
	case err != nil:
		readFailed(ctx, w, routePattern(r), err)
	default:
		writeJSON(ctx, w, http.StatusOK, s.previewJSON(*v))
	}
}

// routePattern returns the route pattern r matched, as chi records it.
func routePattern(r *http.Request) string {
	return chi.RouteContext(r.Context()).RoutePattern()
}

// previewJSON returns the read API view of v, with its status from memory.
func (s *Server) previewJSON(v app.PreviewView) PreviewJSON {
	statuses := s.app.PreviewStatus(v.Slug)
	out := PreviewJSON{
		Slug:     v.Slug,
		Services: make([]PreviewEntryJSON, 0, len(v.Values.Services)+len(v.Values.Dependencies)),
	}
	if created, ok := preview.FirstCreated(*v.Values); ok {
		out.CreatedAt = created.UTC().Format(time.RFC3339)
	}
	if updated, ok := preview.LastDeploy(*v.Values); ok {
		out.UpdatedAt = updated.UTC().Format(time.RFC3339)
		if s.previewTTL > 0 {
			out.ExpiresAt = updated.Add(s.previewTTL).UTC().Format(time.RFC3339)
		}
	}
	for _, e := range v.Entries() {
		j := PreviewEntryJSON{Name: e.Name, Kind: e.Kind, Branch: e.Branch, URL: e.URL, Status: app.StatusUnknown}
		if e.Pinned() {
			j.Pinned = pinnedJSON(s.forge.ParsePRURL, e.Service)
		}
		if st, ok := statuses[e.Name]; ok {
			j.Status, j.Revision, j.StatusAt = st.Status, st.Revision, st.At.UTC().Format(time.RFC3339)
		}
		out.Services = append(out.Services, j)
	}
	return out
}

// pinnedJSON returns the pin of the pinned entry svc, its PR's repo and number
// parsed from its pr-url with parse.
func pinnedJSON(parse preview.PRURLParser, svc *preview.ServiceEntry) *PinnedJSON {
	p := &PinnedJSON{SHA: svc.CommitSha}
	if svc.Metadata == nil {
		return p
	}
	p.Author = svc.Metadata.PRAuthor
	p.PR.URL = svc.Metadata.PRURL
	if owner, repo, number, ok := parse(svc.Metadata.PRURL); ok {
		p.PR.Repo, p.PR.Number = owner+"/"+repo, number
	}
	return p
}
