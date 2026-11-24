package lifecycle

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/badilhq/badil/internal/catalog"
	"github.com/badilhq/badil/internal/preview"
)

// previewCatalog returns the preview catalog of c, its gitopsRepoURL the
// tree's (Tree.GitopsRepoURL).
func previewCatalog(tree Tree, c *catalog.Catalog) preview.Catalog {
	cat := c.Catalog
	cat.GitopsRepoURL = tree.GitopsRepoURL()
	return cat
}

// logShadowed logs each dependency of the preview slug left out because a
// kept pinned entry has its name (preview.Projection.Shadowed).
func logShadowed(slug string, names []string) {
	for _, name := range names {
		slog.Warn("dependency left out: a pinned service has its name",
			slog.String("slug", slug),
			slog.String("name", name),
		)
	}
}

// deployPreview pins serviceName at commitSha in previews/<slug>/values.yaml
// of tree, recorded as "pin <service>@<sha7>". An existing file is merged
// with preview.UpdatePreviewValues; a missing one is built from the full
// catalog (preview.BuildPreviewValues), every other service tracking its
// branch. Either is then projected on c (preview.MergeDeploy). With
// skipUnchanged, when the file exists and the projection differs from it
// only in serviceName's updated-at (preview.EqualExceptUpdatedAt), nothing is
// put and the result is Unchanged.
func deployPreview(
	tree Tree,
	slug string,
	c *catalog.Catalog,
	serviceName, commitSha string,
	inputs preview.DeployInputs,
	skipUnchanged bool,
) (*WriteResult, error) {
	existing, err := tree.GetPreview(slug)
	switch {
	case errors.Is(err, ErrNotFound):
		existing = nil
	case err != nil:
		return nil, err
	}
	projection, unchanged := preview.MergeDeploy(
		existing, slug, previewCatalog(tree, c), c.URLs, serviceName, commitSha, inputs, skipUnchanged,
	)
	logShadowed(slug, projection.Shadowed)
	if unchanged {
		return &WriteResult{Values: *existing, Excluded: projection.Excluded, Unchanged: true}, nil
	}
	if err = tree.PutPreview(slug, projection.Values); err != nil {
		return nil, err
	}
	tree.Record(slug, "pin "+serviceName+"@"+preview.ShortSHA(commitSha))
	return &WriteResult{Values: projection.Values, Excluded: projection.Excluded, Created: existing == nil}, nil
}

// teardownPreview unpins every service of previews/<slug>/values.yaml of
// tree deployed by the PR owner/repo#prNumber, its pr-url parsed with
// parsePR. The file is deleted when no pin remains, recorded as "delete".
// Otherwise it is put, projected on the tree's badil.yaml after the unpin, so
// that an unpinned entry gets its branch-tracking imageTag in the same
// commit, recorded as one "unpin <service>" per service; a badil.yaml that
// can't be read or parsed is then an error. A missing file or a PR that
// pinned nothing is a no-op: an empty TeardownResult and a nil error.
func teardownPreview(
	tree Tree, parsePR preview.PRURLParser, slug, owner, repo string, prNumber int,
) (*TeardownResult, error) {
	return unpinPreview(tree, "teardown", slug, func(vals preview.Values) (preview.Values, []string, bool) {
		return preview.UnpinPR(vals, parsePR, owner, repo, prNumber)
	}, false)
}

// unpinPreviewServices unpins, in previews/<slug>/values.yaml of tree, the
// entries deployed by the PR owner/repo#prNumber whose service is in services
// (preview.UnpinPRServices), their pr-url parsed with parsePR; the PR's
// other entries stay pinned. It is teardownPreview except that the file is
// never deleted: it is put, projected on badil.yaml, even when no pin
// remains, so Deleted is always false.
func unpinPreviewServices(
	tree Tree, parsePR preview.PRURLParser, slug, owner, repo string, prNumber int, services []string,
) (*TeardownResult, error) {
	return unpinPreview(tree, "unpin", slug, func(vals preview.Values) (preview.Values, []string, bool) {
		return preview.UnpinPRServices(vals, parsePR, owner, repo, prNumber, services)
	}, true)
}

// unpinPreview applies unpin to previews/<slug>/values.yaml of tree as
// teardownPreview describes (preview.DecideUnpin). The file is deleted when
// no pin remains, unless keepFile is set. op prefixes its errors.
func unpinPreview(
	tree Tree,
	op, slug string,
	unpin func(preview.Values) (preview.Values, []string, bool),
	keepFile bool,
) (*TeardownResult, error) {
	existing, err := tree.GetPreview(slug)
	if errors.Is(err, ErrNotFound) {
		return &TeardownResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	vals, removed, pinned := unpin(*existing)
	switch preview.DecideUnpin(removed, pinned, keepFile) {
	case preview.UnpinNone:
		return &TeardownResult{}, nil
	case preview.UnpinDelete:
		tree.DeletePreview(slug)
		return &TeardownResult{Removed: removed, Deleted: true}, nil
	case preview.UnpinRewrite:
	}
	c, err := tree.Catalog()
	if err != nil {
		return nil, fmt.Errorf("%s previews/%s/values.yaml: %w", op, slug, err)
	}
	projection := preview.Project(vals, slug, previewCatalog(tree, c), c.URLs)
	logShadowed(slug, projection.Shadowed)
	if err = tree.PutPreview(slug, projection.Values); err != nil {
		return nil, err
	}
	for _, name := range removed {
		tree.Record(slug, "unpin "+name)
	}
	return &TeardownResult{
		Values: projection.Values, Excluded: projection.Excluded, Catalog: c, Removed: removed,
	}, nil
}

// syncPreview rewrites previews/<slug>/values.yaml of tree as its projection
// on c when that differs from the file (preview.SyncDiff), recorded as
// "sync". A file that already matches is left alone. A missing file is a
// no-op: a SyncResult with only NotFound set and a nil error.
func syncPreview(tree Tree, slug string, c *catalog.Catalog) (*SyncResult, error) {
	existing, err := tree.GetPreview(slug)
	if errors.Is(err, ErrNotFound) {
		return &SyncResult{NotFound: true}, nil
	}
	if err != nil {
		return nil, err
	}
	projection, changed := preview.SyncDiff(*existing, slug, previewCatalog(tree, c), c.URLs)
	logShadowed(slug, projection.Shadowed)
	if !changed {
		return &SyncResult{}, nil
	}
	if err = tree.PutPreview(slug, projection.Values); err != nil {
		return nil, err
	}
	tree.Record(slug, "sync")
	return &SyncResult{
		Values:   projection.Values,
		Excluded: projection.Excluded,
		Changed:  true,
		Notify:   preview.SummaryChanged(*existing, projection.Values),
	}, nil
}
