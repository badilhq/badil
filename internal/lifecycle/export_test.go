package lifecycle

import (
	"context"

	"github.com/badilhq/badil/internal/catalog"
	"github.com/badilhq/badil/internal/preview"
)

// TeardownPRs exports teardownPRs to the external tests.
func TeardownPRs(ctx context.Context, api TeardownAPI, tt Target, slugs []string, reason string) {
	teardownPRs(ctx, api, tt, slugs, reason)
}

// DeployPreview exports deployPreview to the external tests.
func DeployPreview(
	tree Tree,
	slug string,
	c *catalog.Catalog,
	serviceName, commitSha string,
	inputs preview.DeployInputs,
	skipUnchanged bool,
) (*WriteResult, error) {
	return deployPreview(tree, slug, c, serviceName, commitSha, inputs, skipUnchanged)
}

// TeardownPreview exports teardownPreview to the external tests.
func TeardownPreview(
	tree Tree, parsePR preview.PRURLParser, slug, owner, repo string, prNumber int,
) (*TeardownResult, error) {
	return teardownPreview(tree, parsePR, slug, owner, repo, prNumber)
}

// UnpinPreviewServices exports unpinPreviewServices to the external tests.
func UnpinPreviewServices(
	tree Tree, parsePR preview.PRURLParser, slug, owner, repo string, prNumber int, services []string,
) (*TeardownResult, error) {
	return unpinPreviewServices(tree, parsePR, slug, owner, repo, prNumber, services)
}

// SyncPreview exports syncPreview to the external tests.
func SyncPreview(tree Tree, slug string, c *catalog.Catalog) (*SyncResult, error) {
	return syncPreview(tree, slug, c)
}
