package store

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/badilhq/badil/internal/preview"
)

// Tree is what an operation of Apply reads and changes: the gitops repo at
// the fetched remote head, plus the preview files the operation put or
// deleted so far. Its reads see those changes. It records, per preview, the
// changes of the commit body.
type Tree struct {
	root    *object.Tree
	repoURL string
	// parsePR is the forge's pull request URL rule, which Teardown and
	// UnpinServices match pr-urls with.
	parsePR preview.PRURLParser

	// files holds the content put at a path, nil when the path was deleted.
	files map[string][]byte
	// changes holds, per preview slug, the commit body lines' changes.
	changes map[string][]string
}

// newTree returns a Tree over root, the tree of the remote head, whose
// previews are projected with the gitops repo URL repoURL and whose pr-urls
// are parsed with parsePR.
func newTree(root *object.Tree, repoURL string, parsePR preview.PRURLParser) *Tree {
	return &Tree{
		root: root, repoURL: repoURL, parsePR: parsePR,
		files: map[string][]byte{}, changes: map[string][]string{},
	}
}

// staged reports whether the operation put or deleted a file.
func (t *Tree) staged() bool {
	return len(t.files) > 0
}

// body returns the commit body: one line per change, "<slug>: <change>", by
// slug, each slug's changes in the order they were made.
func (t *Tree) body() []string {
	var lines []string
	for _, slug := range slices.Sorted(maps.Keys(t.changes)) {
		for _, change := range t.changes[slug] {
			lines = append(lines, slug+": "+change)
		}
	}
	return lines
}

// deleted returns the sorted slugs of the previews whose values.yaml the
// operation deleted from the tree it read.
func (t *Tree) deleted() []string {
	var slugs []string
	for path, content := range t.files {
		slug, ok := previewSlug(path)
		if !ok || content != nil || path != previewPath(slug) {
			continue
		}
		if _, err := t.root.FindEntry(path); err == nil {
			slugs = append(slugs, slug)
		}
	}
	slices.Sort(slugs)
	return slugs
}

// record adds change to the commit body lines of the preview slug.
func (t *Tree) record(slug, change string) {
	t.changes[slug] = append(t.changes[slug], change)
}

// read returns the content of path, ErrNotFound when it does not exist.
func (t *Tree) read(path string) ([]byte, error) {
	if content, ok := t.files[path]; ok {
		if content == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return content, nil
	}
	content, _, err := readTreeFile(t.root, path)
	return content, err
}

// Catalog returns the service catalog of the tree's badil.yaml.
func (t *Tree) Catalog() (*ServiceCatalog, error) {
	raw, err := t.read(configFile)
	if err != nil {
		return nil, err
	}
	return ParseCatalog(raw)
}

// ListPreviews returns the sorted names of the directories under previews/,
// with the operation's changes: a preview whose last file was deleted is
// gone, one whose file was put is there.
func (t *Tree) ListPreviews() ([]string, error) {
	dirs, err := dirNames(t.root, previewsDir)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, name := range dirs {
		names[name] = true
	}
	for path, content := range t.files {
		slug, ok := previewSlug(path)
		if !ok {
			continue
		}
		if content != nil {
			names[slug] = true
			continue
		}
		if !t.dirHasOtherFiles(slug, path) {
			delete(names, slug)
		}
	}
	return slices.Sorted(maps.Keys(names)), nil
}

// dirHasOtherFiles reports whether the directory of the preview slug holds a
// file other than deleted, after the operation's changes.
func (t *Tree) dirHasOtherFiles(slug, deleted string) bool {
	dir := previewsDir + "/" + slug
	for path, content := range t.files {
		if content != nil && path != deleted && strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	sub, err := t.root.Tree(dir)
	if err != nil {
		return false
	}
	found := false
	_ = sub.Files().ForEach(func(f *object.File) error {
		path := dir + "/" + f.Name
		if content, ok := t.files[path]; path != deleted && (!ok || content != nil) {
			found = true
		}
		return nil
	})
	return found
}

// previewSlug returns the slug of a path under previews/<slug>/.
func previewSlug(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, previewsDir+"/")
	if !ok {
		return "", false
	}
	slug, _, ok := strings.Cut(rest, "/")
	return slug, ok && slug != ""
}

// GetPreview returns the parsed previews/<slug>/values.yaml, ErrNotFound when
// it does not exist.
func (t *Tree) GetPreview(slug string) (*preview.Values, error) {
	raw, err := t.read(previewPath(slug))
	if err != nil {
		return nil, err
	}
	return parsePreview(slug, raw)
}

// putPreview puts vals at previews/<slug>/values.yaml.
func (t *Tree) putPreview(slug string, vals preview.Values) error {
	body, err := marshalPreview(vals)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", previewPath(slug), err)
	}
	t.files[previewPath(slug)] = body
	return nil
}

// DeletePreview deletes previews/<slug>/values.yaml, recorded as "delete".
// The preview must exist.
func (t *Tree) DeletePreview(slug string) {
	t.files[previewPath(slug)] = nil
	t.record(slug, "delete")
}

// applyCatalog returns vals projected on catalog (preview.ApplyCatalog) with
// its hosts (preview.ApplyHosts), and logs a dependency left out because a
// kept pinned entry has its name.
func (t *Tree) applyCatalog(vals preview.Values, slug string, catalog *ServiceCatalog) preview.Projection {
	cat := catalog.Catalog
	cat.GitopsRepoURL = t.repoURL
	projected := preview.ApplyCatalog(vals, cat)
	for _, name := range projected.Shadowed {
		slog.Warn("dependency left out: a pinned service has its name",
			slog.String("slug", slug),
			slog.String("name", name),
		)
	}
	projected.Values = preview.ApplyHosts(projected.Values, slug, catalog.URLs)
	return projected
}

// WriteResult is the outcome of Tree.Deploy. Unchanged is true when
// skipUnchanged left the file as it was: Values then holds that file.
// Created is true when the deploy created the file. Excluded is the
// projection's (preview.Projection.Excluded).
type WriteResult struct {
	Values    preview.Values
	Excluded  []string
	Unchanged bool
	Created   bool
}

// Deploy pins serviceName at commitSha in previews/<slug>/values.yaml,
// recorded as "pin <service>@<sha7>". An existing file is merged with
// preview.UpdatePreviewValues; a missing one is built from the full catalog
// (preview.BuildPreviewValues), every other service tracking its branch.
// Either is then projected on catalog (applyCatalog). With skipUnchanged,
// when the file exists and the projection differs from it only in
// serviceName's updated-at (preview.EqualExceptUpdatedAt), nothing is put and
// the result is Unchanged.
func (t *Tree) Deploy(
	slug string,
	catalog *ServiceCatalog,
	serviceName, commitSha string,
	inputs preview.DeployInputs,
	skipUnchanged bool,
) (*WriteResult, error) {
	existing, err := t.GetPreview(slug)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var proj preview.Projection
	if exists {
		updated := preview.UpdatePreviewValues(*existing, serviceName, commitSha, inputs)
		proj = t.applyCatalog(updated, slug, catalog)
		if skipUnchanged && preview.EqualExceptUpdatedAt(*existing, proj.Values, serviceName) {
			return &WriteResult{Values: *existing, Excluded: proj.Excluded, Unchanged: true}, nil
		}
	} else {
		built := preview.BuildPreviewValues(serviceName, commitSha, catalog.Services, inputs)
		proj = t.applyCatalog(built, slug, catalog)
	}
	if err = t.putPreview(slug, proj.Values); err != nil {
		return nil, err
	}
	t.record(slug, "pin "+serviceName+"@"+preview.ShortSHA(commitSha))
	return &WriteResult{Values: proj.Values, Excluded: proj.Excluded, Created: !exists}, nil
}

// TeardownResult is the outcome of Tree.Teardown and Tree.UnpinServices.
// Removed lists the services unpinned from the PR; it is empty when there was
// nothing to tear down. Values, Excluded (preview.Projection.Excluded) and
// Catalog, the badil.yaml the file was projected on, are set only when
// Deleted is false.
type TeardownResult struct {
	Values   preview.Values
	Excluded []string
	Catalog  *ServiceCatalog
	Removed  []string
	Deleted  bool
}

// Teardown unpins every service of previews/<slug>/values.yaml deployed by
// the PR owner/repo#prNumber, its pr-url parsed with the store's pull request
// URL rule. The file is deleted when no pin remains,
// recorded as "delete". Otherwise it is put, projected on the tree's
// badil.yaml after the unpin (applyCatalog), so that an unpinned entry gets
// its branch-tracking imageTag in the same commit, recorded as one "unpin
// <service>" per service; a badil.yaml that can't be read or parsed is then
// an error. A missing file or a PR that pinned nothing is a no-op: an empty
// TeardownResult and a nil error.
func (t *Tree) Teardown(slug, owner, repo string, prNumber int) (*TeardownResult, error) {
	return t.unpin("teardown", slug, func(vals preview.Values) (preview.Values, []string, bool) {
		return preview.UnpinPR(vals, t.parsePR, owner, repo, prNumber)
	}, false)
}

// UnpinServices unpins, in previews/<slug>/values.yaml, the entries deployed
// by the PR owner/repo#prNumber whose service is in services
// (preview.UnpinPRServices); the PR's other entries stay pinned. It is
// Teardown except that the file is never deleted: it is put, projected on
// badil.yaml, even when no pin remains, so Deleted is always false.
func (t *Tree) UnpinServices(slug, owner, repo string, prNumber int, services []string) (*TeardownResult, error) {
	return t.unpin("unpin", slug, func(vals preview.Values) (preview.Values, []string, bool) {
		return preview.UnpinPRServices(vals, t.parsePR, owner, repo, prNumber, services)
	}, true)
}

// unpin applies unpin to previews/<slug>/values.yaml as Teardown describes.
// The file is deleted when no pin remains, unless keepFile is set. op
// prefixes its errors.
func (t *Tree) unpin(
	op, slug string,
	unpin func(preview.Values) (preview.Values, []string, bool),
	keepFile bool,
) (*TeardownResult, error) {
	existing, err := t.GetPreview(slug)
	if errors.Is(err, ErrNotFound) {
		return &TeardownResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	vals, removed, pinned := unpin(*existing)
	if len(removed) == 0 {
		return &TeardownResult{}, nil
	}
	if !pinned && !keepFile {
		t.DeletePreview(slug)
		return &TeardownResult{Removed: removed, Deleted: true}, nil
	}
	catalog, err := t.Catalog()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", op, previewPath(slug), err)
	}
	proj := t.applyCatalog(vals, slug, catalog)
	if err = t.putPreview(slug, proj.Values); err != nil {
		return nil, err
	}
	for _, name := range removed {
		t.record(slug, "unpin "+name)
	}
	return &TeardownResult{
		Values: proj.Values, Excluded: proj.Excluded, Catalog: catalog, Removed: removed,
	}, nil
}

// SyncResult is the outcome of Tree.Sync. Changed is true when the file was
// rewritten; Values and Excluded (preview.Projection.Excluded) then describe
// it. Notify is true when the rewritten file's ordered list of service or
// dependency names, or any entry's host, differs from the file it replaced.
// NotFound is true when the file does not exist.
type SyncResult struct {
	Values   preview.Values
	Excluded []string
	Changed  bool
	Notify   bool
	NotFound bool
}

// Sync rewrites previews/<slug>/values.yaml as its projection on catalog
// (applyCatalog) when that differs from the file (preview.Equal), recorded as
// "sync". A file that already matches is left alone. A missing file is a
// no-op: a SyncResult with only NotFound set and a nil error.
func (t *Tree) Sync(slug string, catalog *ServiceCatalog) (*SyncResult, error) {
	existing, err := t.GetPreview(slug)
	if errors.Is(err, ErrNotFound) {
		return &SyncResult{NotFound: true}, nil
	}
	if err != nil {
		return nil, err
	}
	proj := t.applyCatalog(*existing, slug, catalog)
	if preview.Equal(*existing, proj.Values) {
		return &SyncResult{}, nil
	}
	if err = t.putPreview(slug, proj.Values); err != nil {
		return nil, err
	}
	t.record(slug, "sync")
	return &SyncResult{
		Values:   proj.Values,
		Excluded: proj.Excluded,
		Changed:  true,
		Notify:   summaryChanged(*existing, proj.Values),
	}, nil
}

// summaryChanged reports whether the ordered list of service names, the
// ordered list of dependency names or any entry's host differs between a and
// b: the parts of the PR comment a sync can change.
func summaryChanged(a, b preview.Values) bool {
	return !slices.EqualFunc(a.Services, b.Services, func(x, y preview.ServiceEntry) bool {
		return x.Name == y.Name && x.Host == y.Host
	}) || !slices.EqualFunc(a.Dependencies, b.Dependencies, func(x, y preview.DependencyEntry) bool {
		return x.Name == y.Name && x.Host == y.Host
	})
}
