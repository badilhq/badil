package lifecycle

import (
	"errors"

	"github.com/badilhq/badil/internal/catalog"
	"github.com/badilhq/badil/internal/preview"
)

// ErrNotFound is returned when a file of the gitops repo does not exist.
// Callers should test with errors.Is.
var ErrNotFound = errors.New("gitops: not found")

// ErrUnauthorized is returned when the gitops remote refused the credentials.
// Callers should test with errors.Is.
var ErrUnauthorized = errors.New("gitops: unauthorized")

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

// WriteResult is the outcome of deployPreview. Unchanged is true when
// skipUnchanged left the file as it was: Values then holds that file.
// Created is true when the deploy created the file. Excluded is the
// projection's (preview.Projection.Excluded).
type WriteResult struct {
	Values    preview.Values
	Excluded  []string
	Unchanged bool
	Created   bool
}

// TeardownResult is the outcome of teardownPreview and unpinPreviewServices.
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

// SyncResult is the outcome of syncPreview. Changed is true when the file was
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

	// PutPreview puts vals at previews/<slug>/values.yaml. It records
	// nothing.
	PutPreview(slug string, vals preview.Values) error

	// DeletePreview deletes previews/<slug>/values.yaml, recorded as
	// "delete". The preview must exist.
	DeletePreview(slug string)

	// Record adds change to the commit body lines of the preview slug.
	Record(slug, change string)

	// GitopsRepoURL returns the gitops repo URL the tree's previews are
	// projected with: the gitopsRepoURL of their files.
	GitopsRepoURL() string
}
