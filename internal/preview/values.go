package preview

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// BuildPreviewValues constructs a fresh PreviewValues for a slug given the
// service catalog. The triggering service (serviceName) is pinned to
// commitSha and carries metadata; every other catalog service renders as a
// bare entry tracking its branch.
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
	return PreviewValues{Services: services}
}

// UpdatePreviewValues returns a copy of existing with a deploy event merged in:
//   - if serviceName already has an entry, overwrite commitSha and metadata
//     but preserve the original created-at;
//   - if the service is absent, append a new pinned entry.
//
// Other services are left untouched (branch-tracking entries keep their
// state). existing is never mutated.
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
		return PreviewValues{Services: services}
	}
	services = append(services, ServiceEntry{
		Name:      serviceName,
		CommitSha: commitSha,
		Metadata:  BuildServiceMetadata(inputs),
	})
	return PreviewValues{Services: services}
}

// EqualExceptUpdatedAt reports whether a and b hold the same entries, in the
// same order, ignoring only the updated-at of serviceName's entry. Metadata is
// compared by value; a nil and an empty Extra are equal.
func EqualExceptUpdatedAt(a, b PreviewValues, serviceName string) bool {
	return slices.EqualFunc(a.Services, b.Services, func(x, y ServiceEntry) bool {
		if x.Name != y.Name || x.CommitSha != y.CommitSha || x.Host != y.Host {
			return false
		}
		if x.Metadata == nil || y.Metadata == nil {
			return x.Metadata == nil && y.Metadata == nil
		}
		mx, my := *x.Metadata, *y.Metadata
		if x.Name == serviceName {
			mx.UpdatedAt, my.UpdatedAt = "", ""
		}
		return mx.PRAuthor == my.PRAuthor && mx.PRURL == my.PRURL && mx.PRNumber == my.PRNumber &&
			mx.CreatedAt == my.CreatedAt && mx.UpdatedAt == my.UpdatedAt && mx.VCSRefName == my.VCSRefName &&
			mx.PipelineRunURL == my.PipelineRunURL && maps.Equal(mx.Extra, my.Extra)
	})
}

// UnpinPR returns a copy of vals where every entry deployed by the PR
// owner/repo#number (matched on its metadata pr-url; owner and repo compare
// case-insensitively) is reset to a bare branch-tracking entry. It also returns
// the names of the unpinned services in file order, and whether any entry is
// still pinned to a commit. vals is never mutated.
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
	return PreviewValues{Services: services}, removed, pinned
}

// ApplyHosts returns a copy of vals where every entry exposed by urls carries
// its preview host (URLConfig.Host for slug) and every other entry has an
// empty Host. When urls is not enabled, every Host is empty. vals is never
// mutated.
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
	return PreviewValues{Services: services}
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
