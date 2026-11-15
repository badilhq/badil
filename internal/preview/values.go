package preview

import (
	"slices"
	"strings"
	"time"
)

// BuildPreviewValues constructs a fresh PreviewValues for a slug given the
// service catalog. The triggering service (serviceName) is pinned to
// commitSha and carries metadata; every other catalog service renders as a
// bare entry tracking its branch. The result has apiVersion APIVersion;
// ApplyCatalog sets each entry's repoURL and branch.
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

// ApplyCatalog returns vals projected on the catalog, never mutating vals:
//   - apiVersion is APIVersion;
//   - one entry per name of services, in that order: the existing entry of
//     that name (keeping its commitSha, metadata and host) or a bare {name},
//     with repoURL from repos and branch from branches (DefaultBranch when
//     absent);
//   - then, in file order, every entry whose service is not in services, is
//     pinned (commitSha set) and has a repoURL, unchanged;
//   - a service listed twice keeps only its first entry;
//   - every other entry is dropped.
func ApplyCatalog(vals PreviewValues, services []string, repos, branches map[string]string) PreviewValues {
	inCatalog := make(map[string]bool, len(services))
	out := make([]ServiceEntry, 0, len(services)+len(vals.Services))
	for _, name := range services {
		inCatalog[name] = true
		entry := ServiceEntry{Name: name}
		if i := slices.IndexFunc(vals.Services, func(s ServiceEntry) bool { return s.Name == name }); i >= 0 {
			entry = vals.Services[i]
		}
		entry.RepoURL = repos[name]
		entry.Branch = branches[name]
		if entry.Branch == "" {
			entry.Branch = DefaultBranch
		}
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
	res.Services = out
	return res
}

// Equal reports whether a and b have the same apiVersion and hold the same
// entries, in the same order, every field compared (metadata by value).
func Equal(a, b PreviewValues) bool {
	return equalValues(a, b, "", false)
}

// EqualExceptUpdatedAt reports whether a and b have the same apiVersion and
// hold the same entries, in the same order, ignoring only the updated-at of
// serviceName's entry. Metadata is compared by value.
func EqualExceptUpdatedAt(a, b PreviewValues, serviceName string) bool {
	return equalValues(a, b, serviceName, true)
}

// equalValues compares a and b as Equal does, ignoring the updated-at of
// serviceName's entry when ignoreUpdatedAt is set.
func equalValues(a, b PreviewValues, serviceName string, ignoreUpdatedAt bool) bool {
	if a.APIVersion != b.APIVersion {
		return false
	}
	return slices.EqualFunc(a.Services, b.Services, func(x, y ServiceEntry) bool {
		if x.Name != y.Name || x.RepoURL != y.RepoURL || x.Branch != y.Branch ||
			x.CommitSha != y.CommitSha || x.Host != y.Host {
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

// UnpinPR returns a copy of vals where every entry deployed by the PR
// owner/repo#number (matched on its metadata pr-url; owner and repo compare
// case-insensitively) is reset to a bare branch-tracking entry. It also returns
// the names of the unpinned services in file order, and whether any entry is
// still pinned to a commit. The root fields and every entry's repoURL and
// branch are kept as written. vals is never mutated.
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
