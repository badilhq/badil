package preview

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"
)

// BuildPreviewValues constructs a fresh PreviewValues for a slug given the
// service catalog. The triggering service (serviceName) is pinned to
// commitSha and carries metadata; every other catalog service renders as a
// bare entry tracking its branch. The result has apiVersion APIVersion;
// ApplyCatalog sets each entry's repoURL, branch, chart and imageTag.
//
// The order of services in the result matches the order of the catalog slice.
func BuildPreviewValues(serviceName, commitSha string, catalog []string, inputs DeployInputs) PreviewValues {
	services := make([]ServiceEntry, 0, len(catalog))
	for _, name := range catalog {
		entry := ServiceEntry{Name: name}
		if name == serviceName {
			entry.CommitSha = commitSha
			entry.Metadata = BuildServiceMetadata(inputs)
		}
		services = append(services, entry)
	}
	return PreviewValues{APIVersion: APIVersion, Services: services}
}

// UpdatePreviewValues returns a copy of existing with a deploy event merged in:
//   - if serviceName already has an entry, overwrite commitSha and metadata
//     but preserve the original created-at;
//   - if the service is absent, append a new pinned entry.
//
// Other services and the root fields are left untouched (branch-tracking
// entries keep their state). existing is never mutated.
func UpdatePreviewValues(existing PreviewValues, serviceName, commitSha string, inputs DeployInputs) PreviewValues {
	services := make([]ServiceEntry, len(existing.Services), len(existing.Services)+1)
	copy(services, existing.Services)

	for i := range services {
		if services[i].Name != serviceName {
			continue
		}
		md := BuildServiceMetadata(inputs)
		if old := services[i].Metadata; old != nil && old.CreatedAt != "" {
			md.CreatedAt = old.CreatedAt
		}
		services[i].CommitSha = commitSha
		services[i].Metadata = md
		out := existing
		out.Services = services
		return out
	}
	services = append(services, ServiceEntry{
		Name:      serviceName,
		CommitSha: commitSha,
		Metadata:  BuildServiceMetadata(inputs),
	})
	out := existing
	out.Services = services
	return out
}

// Catalog is what ApplyCatalog projects a preview file on: the services of
// badil.yaml, in file order, and their settings.
type Catalog struct {
	// Services lists the catalog's services, in order.
	Services []string
	// Repos maps each service to its git URL.
	Repos map[string]string
	// Branches maps a service to the branch it tracks; DefaultBranch when
	// absent.
	Branches map[string]string
	// ImageTag is the image tag format of a service without one in
	// ImageTags; DefaultImageTag when empty.
	ImageTag string
	// ImageTags maps a service to its own image tag format.
	ImageTags map[string]string
	// Charts maps a service to its chart settings; an empty field, or an
	// absent service, takes its default (DefaultChartPath,
	// DefaultImageTagParam, DefaultHostParam, no values files).
	Charts map[string]ChartSettings
	// Namespace is the extra metadata of the preview Namespace; nil for none.
	Namespace *NamespaceMetadata
}

// chart returns the chart settings of name, its defaults applied.
func (c Catalog) chart(name string) ChartSettings {
	set := c.Charts[name]
	out := ChartSettings{
		Path:          cmp.Or(set.Path, DefaultChartPath),
		ImageTagParam: cmp.Or(set.ImageTagParam, DefaultImageTagParam),
		HostParam:     cmp.Or(set.HostParam, DefaultHostParam),
	}
	if len(set.ValueFiles) > 0 {
		out.ValueFiles = slices.Clone(set.ValueFiles)
	}
	return out
}

// imageTag returns the image tag format of name.
func (c Catalog) imageTag(name string) string {
	return cmp.Or(c.ImageTags[name], c.ImageTag, DefaultImageTag)
}

// ApplyCatalog returns vals projected on cat, never mutating vals:
//   - apiVersion is APIVersion and namespace is cat.Namespace;
//   - one entry per name of cat.Services, in that order: the existing entry of
//     that name (keeping its commitSha, metadata and host) or a bare {name},
//     with repoURL from cat.Repos, branch from cat.Branches (DefaultBranch
//     when absent), chart from cat.Charts with its defaults applied, and
//     imageTag its format expanded for the entry's commitSha
//     (ExpandImageTag);
//   - then, in file order, every entry whose service is not in cat.Services,
//     is pinned (commitSha set) and has a repoURL, unchanged (it keeps the
//     chart and imageTag it was last written with);
//   - a service listed twice keeps only its first entry;
//   - every other entry is dropped.
func ApplyCatalog(vals PreviewValues, cat Catalog) PreviewValues {
	inCatalog := make(map[string]bool, len(cat.Services))
	out := make([]ServiceEntry, 0, len(cat.Services)+len(vals.Services))
	for _, name := range cat.Services {
		inCatalog[name] = true
		entry := ServiceEntry{Name: name}
		if i := slices.IndexFunc(vals.Services, func(s ServiceEntry) bool { return s.Name == name }); i >= 0 {
			entry = vals.Services[i]
		}
		entry.RepoURL = cat.Repos[name]
		entry.Branch = cmp.Or(cat.Branches[name], DefaultBranch)
		entry.Chart = cat.chart(name)
		entry.ImageTag = ExpandImageTag(cat.imageTag(name), entry.CommitSha)
		out = append(out, entry)
	}
	kept := make(map[string]bool)
	for _, svc := range vals.Services {
		if inCatalog[svc.Name] || kept[svc.Name] || svc.CommitSha == "" || svc.RepoURL == "" {
			continue
		}
		kept[svc.Name] = true
		out = append(out, svc)
	}
	res := vals
	res.APIVersion = APIVersion
	res.Namespace = cat.Namespace
	res.Services = out
	return res
}

// Equal reports whether a and b have the same apiVersion and namespace and
// hold the same entries, in the same order, every field compared (chart and
// metadata by value).
func Equal(a, b PreviewValues) bool {
	return equalValues(a, b, "", false)
}

// EqualExceptUpdatedAt reports whether a and b have the same apiVersion and
// namespace and hold the same entries, in the same order, ignoring only the
// updated-at of serviceName's entry. Chart and metadata are compared by value.
func EqualExceptUpdatedAt(a, b PreviewValues, serviceName string) bool {
	return equalValues(a, b, serviceName, true)
}

// equalValues compares a and b as Equal does, ignoring the updated-at of
// serviceName's entry when ignoreUpdatedAt is set.
func equalValues(a, b PreviewValues, serviceName string, ignoreUpdatedAt bool) bool {
	if a.APIVersion != b.APIVersion || !equalNamespace(a.Namespace, b.Namespace) {
		return false
	}
	return slices.EqualFunc(a.Services, b.Services, func(x, y ServiceEntry) bool {
		if x.Name != y.Name || x.RepoURL != y.RepoURL || x.Branch != y.Branch ||
			x.CommitSha != y.CommitSha || x.ImageTag != y.ImageTag || x.Host != y.Host ||
			!equalChart(x.Chart, y.Chart) {
			return false
		}
		if x.Metadata == nil || y.Metadata == nil {
			return x.Metadata == nil && y.Metadata == nil
		}
		mx, my := *x.Metadata, *y.Metadata
		if ignoreUpdatedAt && x.Name == serviceName {
			mx.UpdatedAt, my.UpdatedAt = "", ""
		}
		return mx == my
	})
}

// equalChart reports whether a and b hold the same settings; a nil and an
// empty ValueFiles are equal.
func equalChart(a, b ChartSettings) bool {
	return a.Path == b.Path && a.ImageTagParam == b.ImageTagParam && a.HostParam == b.HostParam &&
		slices.Equal(a.ValueFiles, b.ValueFiles)
}

// equalNamespace reports whether a and b hold the same labels and
// annotations; nil, an empty NamespaceMetadata and nil or empty maps are
// equal.
func equalNamespace(a, b *NamespaceMetadata) bool {
	var x, y NamespaceMetadata
	if a != nil {
		x = *a
	}
	if b != nil {
		y = *b
	}
	return maps.Equal(x.Labels, y.Labels) && maps.Equal(x.Annotations, y.Annotations)
}

// UnpinPR returns a copy of vals where every entry deployed by the PR
// owner/repo#number (matched on its metadata pr-url; owner and repo compare
// case-insensitively) is reset to a bare branch-tracking entry. It also returns
// the names of the unpinned services in file order, and whether any entry is
// still pinned to a commit. The root fields and every entry's other fields
// (repoURL, branch, imageTag, chart, host) are kept as written: ApplyCatalog
// then gives an unpinned entry its branch-tracking imageTag. vals is never
// mutated.
func UnpinPR(vals PreviewValues, owner, repo string, number int) (PreviewValues, []string, bool) {
	services := make([]ServiceEntry, len(vals.Services))
	copy(services, vals.Services)

	var removed []string
	pinned := false
	for i := range services {
		if belongsToPR(services[i], owner, repo, number) {
			services[i].CommitSha = ""
			services[i].Metadata = nil
			removed = append(removed, services[i].Name)
			continue
		}
		if services[i].CommitSha != "" {
			pinned = true
		}
	}
	out := vals
	out.Services = services
	return out, removed, pinned
}

// ApplyHosts returns a copy of vals where every entry exposed by urls carries
// its preview host (URLConfig.Host for slug) and every other entry has an
// empty Host. When urls is not enabled, every Host is empty. The root fields
// are kept. vals is never mutated.
func ApplyHosts(vals PreviewValues, slug string, urls URLConfig) PreviewValues {
	services := make([]ServiceEntry, len(vals.Services))
	copy(services, vals.Services)

	for i := range services {
		services[i].Host = ""
		if !urls.Enabled() {
			continue
		}
		if _, exposed := urls.Expose[services[i].Name]; exposed {
			services[i].Host = urls.Host(services[i].Name, slug)
		}
	}
	out := vals
	out.Services = services
	return out
}

// belongsToPR reports whether entry was deployed by owner/repo#number.
func belongsToPR(entry ServiceEntry, owner, repo string, number int) bool {
	if entry.Metadata == nil {
		return false
	}
	o, r, n, ok := ParsePRURL(entry.Metadata.PRURL)
	return ok && n == number && strings.EqualFold(o, owner) && strings.EqualFold(r, repo)
}

// LastDeploy returns the newest updated-at (RFC3339) across the pinned entries
// of vals. Entries without a commitSha, without metadata or whose updated-at
// does not parse are ignored. The bool is false when no pinned entry has a
// parseable updated-at.
func LastDeploy(vals PreviewValues) (time.Time, bool) {
	var last time.Time
	found := false
	for _, svc := range vals.Services {
		if svc.CommitSha == "" || svc.Metadata == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, svc.Metadata.UpdatedAt)
		if err != nil {
			continue
		}
		if !found || t.After(last) {
			last, found = t, true
		}
	}
	return last, found
}
