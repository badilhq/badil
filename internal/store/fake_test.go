package store_test

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

// newRemote returns a bare repository whose one commit holds files.
func newRemote(t *testing.T, files map[string]string) *storetest.Remote {
	t.Helper()
	return storetest.NewRemote(t, files)
}

// blobSHA is the git blob hash of content.
func blobSHA(content string) string {
	return plumbing.ComputeHash(plumbing.BlobObject, []byte(content)).String()
}

// gitopsFake is the gitops repo of a store test: remote, which the store
// clones, reads and pushes to. Each push is counted. The first conflicts
// pushes lose a race: before each, the next content of next is committed at
// path ("" removes it; the last one repeats), then onConflict runs when set.
type gitopsFake struct {
	t          *testing.T
	remote     *storetest.Remote
	path       string
	next       []string
	conflicts  int
	onConflict func(*storetest.Remote)

	mu     sync.Mutex
	pushes int
}

// newGitopsFake returns a gitopsFake whose remote holds files.
func newGitopsFake(t *testing.T, files map[string]string) *gitopsFake {
	t.Helper()
	return &gitopsFake{t: t, remote: newRemote(t, files)}
}

// newFileFake returns a gitopsFake around the file at path: the remote holds
// versions[0] there (no file when versions is empty or versions[0] is ""),
// plus extra, and each of the first conflicts pushes commits the next of
// versions first.
func newFileFake(t *testing.T, path string, versions []string, conflicts int, extra map[string]string) *gitopsFake {
	t.Helper()
	files := maps.Clone(extra)
	if files == nil {
		files = map[string]string{}
	}
	if len(versions) > 0 && versions[0] != "" {
		files[path] = versions[0]
	}
	f := newGitopsFake(t, files)
	f.path, f.conflicts = path, conflicts
	if len(versions) > 1 {
		f.next = versions[1:]
	}
	return f
}

// beforePush counts a push and, for the first f.conflicts, commits the next
// version first.
func (f *gitopsFake) beforePush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes++
	if f.conflicts == 0 {
		return nil
	}
	f.conflicts--
	if len(f.next) > 0 {
		f.remote.Set(f.path, f.next[0])
		if len(f.next) > 1 {
			f.next = f.next[1:]
		}
	} else {
		f.remote.Change(nil)
	}
	if f.onConflict != nil {
		f.onConflict(f.remote)
	}
	return nil
}

// pushCount returns the pushes so far.
func (f *gitopsFake) pushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pushes
}

// written returns the content of f.path on the remote, "" when it has none.
func (f *gitopsFake) written() string {
	return f.remote.Files()[f.path]
}

// open returns a store over f.
func (f *gitopsFake) open(t *testing.T, opts ...store.Option) *store.GitHubStore {
	t.Helper()
	f.remote.OnPush(f.beforePush)
	return openStore(t, f.remote.URL(), opts...)
}

// openStore returns a store that clones remoteURL into its own temporary
// directory, removed at the end of the test, and does not wait between
// Apply's attempts.
func openStore(t *testing.T, remoteURL string, opts ...store.Option) *store.GitHubStore {
	t.Helper()
	st, err := store.Open(
		context.Background(), "acme", "gitops", store.CloneConfig{URL: remoteURL}, storetest.Identity, opts...,
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	store.SetSleep(st, func(context.Context, time.Duration) error { return nil })
	t.Cleanup(func() {
		if closeErr := st.Close(); closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
	})
	return st
}

// readStore returns a store over a remote holding files.
func readStore(t *testing.T, files map[string]string, opts ...store.Option) (*store.GitHubStore, *storetest.Remote) {
	t.Helper()
	remote := newRemote(t, files)
	return openStore(t, remote.URL(), opts...), remote
}

// apply runs change on c as one operation of kind.
func apply(
	c *store.GitHubStore, kind string, change func(*store.Tree) (store.Summary, error),
) (store.Result, error) {
	return c.Apply(context.Background(), store.Op{Kind: kind, Change: change})
}

// deploy pins service at sha in the preview slug (Tree.Deploy) as one deploy
// operation, and returns the last attempt's result.
func deploy(
	c *store.GitHubStore,
	slug string,
	catalog *store.ServiceCatalog,
	service, sha string,
	in preview.DeployInputs,
	skipUnchanged bool,
) (*store.WriteResult, store.Result, error) {
	var res *store.WriteResult
	out, err := apply(c, store.OpDeploy, func(tree *store.Tree) (store.Summary, error) {
		var deployErr error
		res, deployErr = tree.Deploy(slug, catalog, service, sha, in, skipUnchanged)
		return store.Summary{Subject: "deploy " + service + " to " + slug}, deployErr
	})
	return res, out, err
}
