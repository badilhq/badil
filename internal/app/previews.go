package app

import (
	"context"
	"errors"

	"github.com/badilhq/badil/internal/catalog"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

// Kinds of a preview's entries.
const (
	KindService    = "service"
	KindDependency = "dependency"
)

// StatusUnknown is the status of an entry no status was recorded for.
const StatusUnknown = "unknown"

// ErrNoPreview is a preview whose file is not in the gitops repo.
var ErrNoPreview = errors.New("preview not found")

// StatusRepository holds the status the renderer reports for each service of
// each preview. *status.Store implements it.
type StatusRepository interface {
	// Preview returns the latest status of each service of the preview
	// slug, by name.
	Preview(slug string) map[string]preview.ServiceStatus
}

// PreviewView is a preview file read from the gitops clone, with the catalog
// it is read with.
type PreviewView struct {
	Slug    string
	Values  *preview.Values
	Catalog *catalog.Catalog
}

// Entries returns the service entries then the dependency entries of v, in
// file order.
func (v PreviewView) Entries() []Entry {
	link := func(name, host string) string {
		_, u, _ := v.Catalog.URLs.Link(name, host)
		return u
	}
	out := make([]Entry, 0, len(v.Values.Services)+len(v.Values.Dependencies))
	for i := range v.Values.Services {
		svc := &v.Values.Services[i]
		out = append(
			out,
			Entry{Name: svc.Name, Kind: KindService, Branch: svc.Branch, URL: link(svc.Name, svc.Host), Service: svc},
		)
	}
	for _, dep := range v.Values.Dependencies {
		out = append(out, Entry{Name: dep.Name, Kind: KindDependency, URL: link(dep.Name, dep.Host)})
	}
	return out
}

// Entry is a service or dependency entry of a preview file: its name, kind,
// branch (services only), the URL of its host (when it has one) and, for a
// service, its file entry.
type Entry struct {
	Name, Kind, Branch, URL string
	Service                 *preview.ServiceEntry
}

// Pinned reports whether e is a pinned service entry.
func (e Entry) Pinned() bool {
	return e.Service != nil && e.Service.CommitSha != ""
}

// Previews returns every preview file of the gitops clone, sorted by slug,
// each with the catalog. A preview whose file is gone by the time it is read
// is left out.
func (a *App) Previews(ctx context.Context) ([]PreviewView, error) {
	catalog, err := a.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return a.listPreviews(ctx, catalog)
}

// Preview reads the preview slug from the gitops clone, with the catalog. It
// returns ErrNoPreview when the preview does not exist, or slug is not a
// slug.
func (a *App) Preview(ctx context.Context, slug string) (*PreviewView, error) {
	if slug == "" || preview.Slugify(slug) != slug {
		return nil, ErrNoPreview
	}
	catalog, err := a.readCatalog(ctx)
	if err != nil {
		return nil, err
	}
	vals, _, err := a.store.GetPreview(ctx, slug)
	switch {
	case errors.Is(err, lifecycle.ErrNotFound):
		return nil, ErrNoPreview
	case err != nil:
		return nil, err
	}
	return &PreviewView{Slug: slug, Values: vals, Catalog: catalog}, nil
}

// PreviewStatus returns the status of the preview slug's entries, by name:
// empty when the app has no status repository.
func (a *App) PreviewStatus(slug string) map[string]preview.ServiceStatus {
	if a.status == nil {
		return map[string]preview.ServiceStatus{}
	}
	return a.status.Preview(slug)
}

// readCatalog reads badil.yaml from the gitops clone. A missing file is an
// empty catalog; any other failure is returned.
func (a *App) readCatalog(ctx context.Context) (*catalog.Catalog, error) {
	cat, err := a.store.FetchServiceCatalog(ctx)
	if errors.Is(err, lifecycle.ErrNotFound) {
		return &catalog.Catalog{}, nil
	}
	return cat, err
}

// listPreviews reads every preview file of the gitops clone, sorted by slug,
// each with catalog. A preview whose file is gone by the time it is read is
// left out.
func (a *App) listPreviews(ctx context.Context, catalog *catalog.Catalog) ([]PreviewView, error) {
	slugs, err := a.store.ListPreviewFiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PreviewView, 0, len(slugs))
	for _, slug := range slugs {
		vals, _, readErr := a.store.GetPreview(ctx, slug)
		switch {
		case errors.Is(readErr, lifecycle.ErrNotFound):
			continue
		case readErr != nil:
			return nil, readErr
		}
		out = append(out, PreviewView{Slug: slug, Values: vals, Catalog: catalog})
	}
	return out, nil
}
