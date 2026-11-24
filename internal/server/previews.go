package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// Kinds of a preview's entries in the read API.
const (
	kindService    = "service"
	kindDependency = "dependency"
)

// statusUnknown is the status of an entry no status was recorded for.
const statusUnknown = "unknown"

// errNoPreview is a preview whose file is not in the gitops repo, answered
// with 404 and its message.
var errNoPreview = errors.New("preview not found")

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

// previewView is a preview file read from the gitops clone, with the catalog
// it is read with.
type previewView struct {
	slug    string
	vals    *preview.Values
	catalog *store.ServiceCatalog
}

// readCatalog reads badil.yaml from the gitops clone. A missing file is an
// empty catalog; any other failure is returned.
func (s *Server) readCatalog(ctx context.Context) (*store.ServiceCatalog, error) {
	catalog, err := s.store.FetchServiceCatalog(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return &store.ServiceCatalog{}, nil
	}
	return catalog, err
}

// listPreviews reads every preview file of the gitops clone, sorted by slug,
// each with catalog. A preview whose file is gone by the time it is read is
// left out.
func (s *Server) listPreviews(ctx context.Context, catalog *store.ServiceCatalog) ([]previewView, error) {
	slugs, err := s.store.ListPreviewFiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]previewView, 0, len(slugs))
	for _, slug := range slugs {
		vals, _, readErr := s.store.GetPreview(ctx, slug)
		switch {
		case errors.Is(readErr, store.ErrNotFound):
			continue
		case readErr != nil:
			return nil, readErr
		}
		out = append(out, previewView{slug: slug, vals: vals, catalog: catalog})
	}
	return out, nil
}

// readPreview reads the preview slug from the gitops clone, with the
// catalog. It returns errNoPreview when the preview does not exist, or slug
// is not a slug.
func (s *Server) readPreview(ctx context.Context, slug string) (*previewView, error) {
	if slug == "" || preview.Slugify(slug) != slug {
		return nil, errNoPreview
	}
	catalog, err := s.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	vals, _, err := s.store.GetPreview(ctx, slug)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, errNoPreview
	case err != nil:
		return nil, err
	}
	return &previewView{slug: slug, vals: vals, catalog: catalog}, nil
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
	catalog, err := s.readCatalog(ctx)
	if err != nil {
		readFailed(ctx, w, routePattern(r), err)
		return
	}
	views, err := s.listPreviews(ctx, catalog)
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
	v, err := s.readPreview(ctx, chi.URLParam(r, "slug"))
	switch {
	case errors.Is(err, errNoPreview):
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

// previewStatus returns the status of the preview slug's entries, by name.
func (s *Server) previewStatus(slug string) map[string]preview.ServiceStatus {
	if s.status == nil {
		return map[string]preview.ServiceStatus{}
	}
	return s.status.Preview(slug)
}

// previewJSON returns the read API view of v, with its status from memory.
func (s *Server) previewJSON(v previewView) PreviewJSON {
	statuses := s.previewStatus(v.slug)
	out := PreviewJSON{
		Slug:     v.slug,
		Services: make([]PreviewEntryJSON, 0, len(v.vals.Services)+len(v.vals.Dependencies)),
	}
	if created, ok := preview.FirstCreated(*v.vals); ok {
		out.CreatedAt = created.UTC().Format(time.RFC3339)
	}
	if updated, ok := preview.LastDeploy(*v.vals); ok {
		out.UpdatedAt = updated.UTC().Format(time.RFC3339)
		if s.previewTTL > 0 {
			out.ExpiresAt = updated.Add(s.previewTTL).UTC().Format(time.RFC3339)
		}
	}
	for _, e := range entries(v) {
		j := PreviewEntryJSON{Name: e.name, Kind: e.kind, Branch: e.branch, URL: e.url, Status: statusUnknown}
		if e.pinned() {
			j.Pinned = pinnedJSON(s.forge.ParsePRURL, e.svc)
		}
		if st, ok := statuses[e.name]; ok {
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

// entry is a service or dependency entry of a preview file: its name, kind,
// branch (services only), the URL of its host (when it has one) and, for a
// service, its file entry.
type entry struct {
	name, kind, branch, url string
	svc                     *preview.ServiceEntry
}

// pinned reports whether e is a pinned service entry.
func (e entry) pinned() bool {
	return e.svc != nil && e.svc.CommitSha != ""
}

// entries returns the service entries then the dependency entries of v, in
// file order.
func entries(v previewView) []entry {
	link := func(name, host string) string {
		_, u, _ := v.catalog.URLs.Link(name, host)
		return u
	}
	out := make([]entry, 0, len(v.vals.Services)+len(v.vals.Dependencies))
	for i := range v.vals.Services {
		svc := &v.vals.Services[i]
		out = append(
			out,
			entry{name: svc.Name, kind: kindService, branch: svc.Branch, url: link(svc.Name, svc.Host), svc: svc},
		)
	}
	for _, dep := range v.vals.Dependencies {
		out = append(out, entry{name: dep.Name, kind: kindDependency, url: link(dep.Name, dep.Host)})
	}
	return out
}
