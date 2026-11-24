package preview

import "slices"

// Project returns vals projected on cat (ApplyCatalog) with its hosts
// (ApplyHosts for slug and urls) in Values. cat.GitopsRepoURL is the
// caller's to set. vals is never mutated.
func Project(vals Values, slug string, cat Catalog, urls URLConfig) Projection {
	projected := ApplyCatalog(vals, cat)
	projected.Values = ApplyHosts(projected.Values, slug, urls)
	return projected
}

// MergeDeploy returns the projection (Project) of a deploy that pins
// serviceName at commitSha in the preview slug. An existing file is merged
// with UpdatePreviewValues; a nil one is built from the full catalog
// (BuildPreviewValues), every other service tracking its branch. The bool is
// true when skipUnchanged is set, the file exists and the projection differs
// from it only in serviceName's updated-at (EqualExceptUpdatedAt): the file
// is then to be left as it is. existing is never mutated.
func MergeDeploy(
	existing *Values,
	slug string,
	cat Catalog,
	urls URLConfig,
	serviceName, commitSha string,
	inputs DeployInputs,
	skipUnchanged bool,
) (Projection, bool) {
	if existing == nil {
		return Project(BuildPreviewValues(serviceName, commitSha, cat.Services, inputs), slug, cat, urls), false
	}
	projection := Project(UpdatePreviewValues(*existing, serviceName, commitSha, inputs), slug, cat, urls)
	return projection, skipUnchanged && EqualExceptUpdatedAt(*existing, projection.Values, serviceName)
}

// UnpinDecision is what an unpin does with a preview file (DecideUnpin).
type UnpinDecision int

const (
	// UnpinNone leaves the file as it is: the PR pinned nothing in it.
	UnpinNone UnpinDecision = iota
	// UnpinDelete deletes the file: no pin remains.
	UnpinDelete
	// UnpinRewrite rewrites the file, projected after the unpin.
	UnpinRewrite
)

// DecideUnpin returns what an unpin that removed the services removed does
// with the file: UnpinNone when removed is empty, UnpinDelete when no entry
// is still pinned (pinned false) and the file is not to be kept (keepFile
// false), UnpinRewrite otherwise.
func DecideUnpin(removed []string, pinned, keepFile bool) UnpinDecision {
	switch {
	case len(removed) == 0:
		return UnpinNone
	case !pinned && !keepFile:
		return UnpinDelete
	default:
		return UnpinRewrite
	}
}

// SyncDiff returns the projection (Project) of the existing file of the
// preview slug, and whether it differs from that file (Equal): the file is
// then to be rewritten as it.
func SyncDiff(existing Values, slug string, cat Catalog, urls URLConfig) (Projection, bool) {
	projection := Project(existing, slug, cat, urls)
	return projection, !Equal(existing, projection.Values)
}

// SummaryChanged reports whether the ordered list of service names, the
// ordered list of dependency names or any entry's host differs between a and
// b: the parts of the PR comment a sync can change.
func SummaryChanged(a, b Values) bool {
	return !slices.EqualFunc(a.Services, b.Services, func(x, y ServiceEntry) bool {
		return x.Name == y.Name && x.Host == y.Host
	}) || !slices.EqualFunc(a.Dependencies, b.Dependencies, func(x, y DependencyEntry) bool {
		return x.Name == y.Name && x.Host == y.Host
	})
}
