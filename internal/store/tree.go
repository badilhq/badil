package store

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/badilhq/badil/internal/catalog"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

var _ lifecycle.Tree = (*Tree)(nil)

// Tree is what an operation of Apply reads and changes: the gitops repo at
// the fetched remote head, plus the preview files the operation put or
// deleted so far. Its reads see those changes. It records, per preview, the
// changes of the commit body.
type Tree struct {
	root    *object.Tree
	repoURL string

	// files holds the content put at a path, nil when the path was deleted.
	files map[string][]byte
	// changes holds, per preview slug, the commit body lines' changes.
	changes map[string][]string
}

// newTree returns a Tree over root, the tree of the remote head, whose
// previews are projected with the gitops repo URL repoURL.
func newTree(root *object.Tree, repoURL string) *Tree {
	return &Tree{
		root: root, repoURL: repoURL,
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

// Record adds change to the commit body lines of the preview slug.
func (t *Tree) Record(slug, change string) {
	t.changes[slug] = append(t.changes[slug], change)
}

// read returns the content of path, lifecycle.ErrNotFound when it does not
// exist.
func (t *Tree) read(path string) ([]byte, error) {
	if content, ok := t.files[path]; ok {
		if content == nil {
			return nil, fmt.Errorf("%w: %s", lifecycle.ErrNotFound, path)
		}
		return content, nil
	}
	content, _, err := readTreeFile(t.root, path)
	return content, err
}

// Catalog returns the service catalog of the tree's badil.yaml.
func (t *Tree) Catalog() (*catalog.Catalog, error) {
	raw, err := t.read(configFile)
	if err != nil {
		return nil, err
	}
	return catalog.Parse(raw)
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

// GetPreview returns the parsed previews/<slug>/values.yaml,
// lifecycle.ErrNotFound when it does not exist.
func (t *Tree) GetPreview(slug string) (*preview.Values, error) {
	raw, err := t.read(previewPath(slug))
	if err != nil {
		return nil, err
	}
	return parsePreview(slug, raw)
}

// PutPreview puts vals at previews/<slug>/values.yaml. It records nothing.
func (t *Tree) PutPreview(slug string, vals preview.Values) error {
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
	t.Record(slug, "delete")
}

// GitopsRepoURL returns the gitops repo URL the tree's previews are
// projected with: the gitopsRepoURL of their files.
func (t *Tree) GitopsRepoURL() string {
	return t.repoURL
}
