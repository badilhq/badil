package preview

import (
	"bytes"
	"cmp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	// Dependencies are the upstream charts of badil.yaml, in order, as
	// parsed: HostParam as written, Host empty.
	Dependencies []DependencyEntry
	// Needs maps a service or dependency to the services and dependencies it
	// needs. When empty, a preview runs every service and dependency;
	// otherwise only its pinned entries and what they need (included).
	Needs map[string][]string
	// GitopsRepoURL is the git URL of the gitops repo.
	GitopsRepoURL string
}

// chart returns the chart settings of name, its defaults applied.
func (c Catalog) chart(name string) ChartSettings {
	set := c.Charts[name]
	return ChartSettings{
		Path:          cmp.Or(set.Path, DefaultChartPath),
		ImageTagParam: cmp.Or(set.ImageTagParam, DefaultImageTagParam),
		HostParam:     cmp.Or(set.HostParam, DefaultHostParam),
		ValueFiles:    slices.Clone(set.ValueFiles),
	}
}

// imageTag returns the image tag format of name.
func (c Catalog) imageTag(name string) string {
	return cmp.Or(c.ImageTags[name], c.ImageTag, DefaultImageTag)
}

// Projection is a preview file projected on a Catalog (ApplyCatalog).
type Projection struct {
	// Values is the projected file.
	Values PreviewValues
	// Excluded lists the catalog services then the catalog dependencies, in
	// badil.yaml order, that Values leaves out because no pinned service
	// needs them, directly or transitively. It is nil when the catalog
	// declares no needs or every part runs.
	Excluded []string
	// Shadowed lists the catalog dependencies, in order, left out because a
	// kept pinned entry of a service removed from the catalog has their name.
	Shadowed []string
}

// ApplyCatalog returns vals projected on cat, never mutating vals:
//   - apiVersion is APIVersion, gitopsRepoURL is cat.GitopsRepoURL and
//     namespace is cat.Namespace;
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
//   - every other entry is dropped;
//   - dependencies are cat.Dependencies, in that order, except one named like
//     a service entry of the result (a kept pinned entry of a service removed
//     from the catalog), which is left out (Shadowed). Their host is
//     ApplyHosts's to set;
//   - when cat declares needs, only the included services and dependencies
//     are kept: the pinned entries and their transitive needs, every other
//     one left out (Excluded).
func ApplyCatalog(vals PreviewValues, cat Catalog) Projection {
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
	in := cat.included(out)
	var excluded []string
	if in != nil {
		out = slices.DeleteFunc(out, func(s ServiceEntry) bool { return !in[s.Name] })
		excluded = cat.excluded(in)
	}
	res := vals
	res.APIVersion = APIVersion
	res.GitopsRepoURL = cat.GitopsRepoURL
	res.Namespace = cat.Namespace
	res.Services = out
	var shadowed []string
	res.Dependencies = make([]DependencyEntry, 0, len(cat.Dependencies))
	for _, dep := range cat.Dependencies {
		switch {
		case slices.ContainsFunc(out, func(s ServiceEntry) bool { return s.Name == dep.Name }):
			shadowed = append(shadowed, dep.Name)
		case in == nil || in[dep.Name]:
			dep.ValueFiles = slices.Clone(dep.ValueFiles)
			res.Dependencies = append(res.Dependencies, dep)
		}
	}
	return Projection{Values: res, Excluded: excluded, Shadowed: shadowed}
}

// Equal reports whether a and b encode to the same YAML, so that a nil and an
// empty namespace, list or map are equal, and so is a number in a
// dependency's values whether decoded as a float or an integer.
func Equal(a, b PreviewValues) bool {
	x, errA := yaml.Marshal(canonical(a))
	y, errB := yaml.Marshal(canonical(b))
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// EqualExceptUpdatedAt reports whether a and b are Equal, ignoring only the
// updated-at of serviceName's entry.
func EqualExceptUpdatedAt(a, b PreviewValues, serviceName string) bool {
	return Equal(withoutUpdatedAt(a, serviceName), withoutUpdatedAt(b, serviceName))
}

// canonical returns vals with an empty namespace set to nil, which yaml.v3
// would otherwise encode as {}.
func canonical(vals PreviewValues) PreviewValues {
	if ns := vals.Namespace; ns != nil && len(ns.Labels) == 0 && len(ns.Annotations) == 0 {
		vals.Namespace = nil
	}
	return vals
}

// withoutUpdatedAt returns a copy of vals whose serviceName entry has an
// empty updated-at. vals is never mutated.
func withoutUpdatedAt(vals PreviewValues, serviceName string) PreviewValues {
	vals.Services = slices.Clone(vals.Services)
	for i, svc := range vals.Services {
		if svc.Name == serviceName && svc.Metadata != nil {
			md := *svc.Metadata
			md.UpdatedAt = ""
			vals.Services[i].Metadata = &md
		}
	}
	return vals
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
	return unpin(vals, func(entry ServiceEntry) bool { return belongsToPR(entry, owner, repo, number) })
}

// UnpinPRServices is UnpinPR restricted to the entries whose name is in
// services: the PR's entries for other services stay pinned.
func UnpinPRServices(
	vals PreviewValues, owner, repo string, number int, services []string,
) (PreviewValues, []string, bool) {
	return unpin(vals, func(entry ServiceEntry) bool {
		return slices.Contains(services, entry.Name) && belongsToPR(entry, owner, repo, number)
	})
}

// unpin returns a copy of vals where every entry for which match holds is
// reset to a bare branch-tracking entry, the names of those entries in file
// order, and whether any entry is still pinned to a commit.
func unpin(vals PreviewValues, match func(ServiceEntry) bool) (PreviewValues, []string, bool) {
	services := make([]ServiceEntry, len(vals.Services))
	copy(services, vals.Services)

	var removed []string
	pinned := false
	for i := range services {
		if match(services[i]) {
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

// ApplyHosts returns a copy of vals where every service entry exposed by urls
// carries its preview host (URLConfig.Host for slug) and every other service
// entry has an empty Host. A dependency exposed by urls with a HostParam
// carries its host too; every other dependency has an empty Host and an
// empty HostParam. When urls is not enabled, every Host is empty. The root
// fields are kept. vals is never mutated.
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
	deps := make([]DependencyEntry, len(vals.Dependencies))
	copy(deps, vals.Dependencies)
	for i := range deps {
		_, exposed := urls.Expose[deps[i].Name]
		if urls.Enabled() && exposed && deps[i].HostParam != "" {
			deps[i].Host = urls.Host(deps[i].Name, slug)
			continue
		}
		deps[i].Host, deps[i].HostParam = "", ""
	}
	out := vals
	out.Services = services
	out.Dependencies = deps
	return out
}

// belongsToPR reports whether entry was deployed by owner/repo#number.
func belongsToPR(entry ServiceEntry, owner, repo string, number int) bool {
	return entry.Metadata != nil && MatchesPR(entry.Metadata.PRURL, owner, repo, number)
}

// LastDeploy returns the newest updated-at (RFC3339) across the pinned entries
// of vals. Entries without a commitSha, without metadata or whose updated-at
// does not parse are ignored. The bool is false when no pinned entry has a
// parseable updated-at.
func LastDeploy(vals PreviewValues) (time.Time, bool) {
	ts := pinnedTimes(vals, func(m *Metadata) string { return m.UpdatedAt })
	if len(ts) == 0 {
		return time.Time{}, false
	}
	return slices.MaxFunc(ts, time.Time.Compare), true
}

// FirstCreated returns the oldest created-at (RFC3339) across the pinned
// entries of vals, ignoring the same entries as LastDeploy. The bool is false
// when no pinned entry has a parseable created-at.
func FirstCreated(vals PreviewValues) (time.Time, bool) {
	ts := pinnedTimes(vals, func(m *Metadata) string { return m.CreatedAt })
	if len(ts) == 0 {
		return time.Time{}, false
	}
	return slices.MinFunc(ts, time.Time.Compare), true
}

// pinnedTimes returns the metadata field of each pinned entry of vals,
// parsed as RFC3339. Entries without metadata or whose field does not parse
// are left out.
func pinnedTimes(vals PreviewValues, field func(*Metadata) string) []time.Time {
	var ts []time.Time
	for _, svc := range vals.Services {
		if svc.CommitSha == "" || svc.Metadata == nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, field(svc.Metadata)); err == nil {
			ts = append(ts, t)
		}
	}
	return ts
}

// AtPin reports whether revision is at the pin commitSha: commitSha is
// non-empty and revision starts with it.
func AtPin(revision, commitSha string) bool {
	return commitSha != "" && strings.HasPrefix(revision, commitSha)
}
