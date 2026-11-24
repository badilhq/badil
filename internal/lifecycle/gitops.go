package lifecycle

import (
	"errors"

	"github.com/badilhq/badil/internal/catalog"
	"github.com/badilhq/badil/internal/preview"
)

// ErrNotFound is returned when a file of the gitops repo does not exist.
// Callers should test with errors.Is.
var ErrNotFound = errors.New("gitops: not found")

// Operation kinds, the op label of an Apply's duration and push conflicts.
const (
	OpDeploy   = "deploy"
	OpTeardown = "teardown"
	OpSweep    = "sweep"
	OpSync     = "sync"
)

// Summary describes the change an operation made: the subject of its commit.
type Summary struct {
	Subject string
}

// Op is one write to the gitops repo. Kind is OpDeploy, OpTeardown, OpSweep
// or OpSync. Change reads the tree of the remote head and puts or deletes its
// files through it; it may run several times, once per attempt, so it reads
// nothing but tree and has no other side effect. An error from Change fails
// the operation with nothing committed.
type Op struct {
	Kind   string
	Change func(tree Tree) (Summary, error)
}

// Result is the outcome of a successful Apply. NoOp is true when the last
// attempt's Change changed nothing: nothing was committed, and Commit is the
// remote head it ran on. Otherwise Commit is the pushed commit. Subject is
// Commit's subject line, URL its GitHub URL. Deleted holds the sorted slugs
// of the previews whose previews/<slug>/values.yaml the pushed commit
// deleted, empty when none.
type Result struct {
	NoOp    bool
	Commit  string
	Subject string
	URL     string
	Deleted []string
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

// TeardownResult is the outcome of Tree.Teardown and Tree.UnpinServices.
// Removed lists the services unpinned from the PR; it is empty when there was
// nothing to tear down. Values, Excluded (preview.Projection.Excluded) and
// Catalog, the badil.yaml the file was projected on, are set only when
// Deleted is false.
type TeardownResult struct {
	Values   preview.Values
	Excluded []string
	Catalog  *catalog.Catalog
	Removed  []string
	Deleted  bool
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

// Tree is what an operation of Apply reads and changes: the gitops repo at
// the fetched remote head, plus the preview files the operation put or
// deleted so far. Its reads see those changes.
type Tree interface {
	// Catalog returns the service catalog of the tree's badil.yaml.
	Catalog() (*catalog.Catalog, error)

	// ListPreviews returns the sorted names of the directories under
	// previews/, with the operation's changes: a preview whose last file was
	// deleted is gone, one whose file was put is there.
	ListPreviews() ([]string, error)

	// GetPreview returns the parsed previews/<slug>/values.yaml, ErrNotFound
	// when it does not exist.
	GetPreview(slug string) (*preview.Values, error)

	// DeletePreview deletes previews/<slug>/values.yaml, recorded as
	// "delete". The preview must exist.
	DeletePreview(slug string)

	// Deploy pins serviceName at commitSha in previews/<slug>/values.yaml,
	// recorded as "pin <service>@<sha7>". An existing file is merged with
	// preview.UpdatePreviewValues; a missing one is built from the full
	// catalog (preview.BuildPreviewValues), every other service tracking its
	// branch. Either is then projected on c. With skipUnchanged, when the
	// file exists and the projection differs from it only in serviceName's
	// updated-at (preview.EqualExceptUpdatedAt), nothing is put and the
	// result is Unchanged.
	Deploy(
		slug string,
		c *catalog.Catalog,
		serviceName, commitSha string,
		inputs preview.DeployInputs,
		skipUnchanged bool,
	) (*WriteResult, error)

	// Teardown unpins every service of previews/<slug>/values.yaml deployed
	// by the PR owner/repo#prNumber, its pr-url parsed with the store's pull
	// request URL rule. The file is deleted when no pin remains, recorded as
	// "delete". Otherwise it is put, projected on the tree's badil.yaml after
	// the unpin, so that an unpinned entry gets its branch-tracking imageTag
	// in the same commit, recorded as one "unpin <service>" per service; a
	// badil.yaml that can't be read or parsed is then an error. A missing
	// file or a PR that pinned nothing is a no-op: an empty TeardownResult
	// and a nil error.
	Teardown(slug, owner, repo string, prNumber int) (*TeardownResult, error)

	// UnpinServices unpins, in previews/<slug>/values.yaml, the entries
	// deployed by the PR owner/repo#prNumber whose service is in services
	// (preview.UnpinPRServices); the PR's other entries stay pinned. It is
	// Teardown except that the file is never deleted: it is put, projected on
	// badil.yaml, even when no pin remains, so Deleted is always false.
	UnpinServices(slug, owner, repo string, prNumber int, services []string) (*TeardownResult, error)

	// Sync rewrites previews/<slug>/values.yaml as its projection on c when
	// that differs from the file (preview.Equal), recorded as "sync". A file
	// that already matches is left alone. A missing file is a no-op: a
	// SyncResult with only NotFound set and a nil error.
	Sync(slug string, c *catalog.Catalog) (*SyncResult, error)
}
