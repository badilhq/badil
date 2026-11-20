package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/store"
)

// TestMain serves file:// remotes in-process, so the tests need no git
// binary.
func TestMain(m *testing.M) {
	client.InstallProtocol("file", server.NewClient(server.DefaultLoader))
	os.Exit(m.Run())
}

// remoteBranch is the default branch of the test remotes: not main, so the
// store has to find it from the clone's HEAD.
const remoteBranch = "trunk"

// remoteRepo is a bare repository the store clones, whose branch remoteBranch
// holds files. Each change is one commit.
type remoteRepo struct {
	t     *testing.T
	dir   string
	repo  *git.Repository
	mu    sync.Mutex
	files map[string]string
}

// newRemote returns a bare repository in a temporary directory whose one
// commit holds files (path to content).
func newRemote(t *testing.T, files map[string]string) *remoteRepo {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName(remoteBranch)},
		Bare:        true,
	})
	if err != nil {
		t.Fatalf("init remote: %v", err)
	}
	r := &remoteRepo{t: t, dir: dir, repo: repo, files: maps.Clone(files)}
	if r.files == nil {
		r.files = map[string]string{}
	}
	r.commit()
	return r
}

// url returns the remote's file:// URL.
func (r *remoteRepo) url() string {
	return "file://" + r.dir
}

// set commits content at path; "" removes path.
func (r *remoteRepo) set(path, content string) {
	r.change(map[string]string{path: content})
}

// change commits files over the remote's files; a "" content removes its path.
func (r *remoteRepo) change(files map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for path, content := range files {
		if content == "" {
			delete(r.files, path)
			continue
		}
		r.files[path] = content
	}
	r.commit()
}

// commit writes r.files as a commit on remoteBranch, with the previous one as
// parent. The caller holds r.mu, except in newRemote.
func (r *remoteRepo) commit() {
	r.t.Helper()
	st := r.repo.Storer
	tree, err := writeTree(st, r.files)
	if err != nil {
		r.t.Fatalf("write tree: %v", err)
	}
	branch := plumbing.NewBranchReferenceName(remoteBranch)
	var parents []plumbing.Hash
	if ref, refErr := st.Reference(branch); refErr == nil {
		parents = append(parents, ref.Hash())
	}
	sig := object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}
	c := &object.Commit{
		Author: sig, Committer: sig, Message: "test", TreeHash: tree, ParentHashes: parents,
	}
	obj := st.NewEncodedObject()
	if err = c.Encode(obj); err != nil {
		r.t.Fatalf("encode commit: %v", err)
	}
	h, err := st.SetEncodedObject(obj)
	if err != nil {
		r.t.Fatalf("store commit: %v", err)
	}
	// The branch moves by a rename, as on a git server: a concurrent fetch
	// reads the old or the new commit, never a half-written ref file.
	tmp, err := os.CreateTemp(r.dir, "ref-")
	if err != nil {
		r.t.Fatalf("set %s: %v", branch, err)
	}
	_, err = tmp.WriteString(h.String() + "\n")
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(r.dir, filepath.FromSlash(branch.String())))
	}
	if err != nil {
		r.t.Fatalf("set %s: %v", branch, err)
	}
}

// writeTree stores files (path to content) as a tree and returns its hash.
func writeTree(st storer.EncodedObjectStorer, files map[string]string) (plumbing.Hash, error) {
	var entries []object.TreeEntry
	dirs := map[string]map[string]string{}
	for path, content := range files {
		if dir, rest, ok := strings.Cut(path, "/"); ok {
			if dirs[dir] == nil {
				dirs[dir] = map[string]string{}
			}
			dirs[dir][rest] = content
			continue
		}
		blob := st.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		w, err := blob.Writer()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if _, err = io.WriteString(w, content); err != nil {
			return plumbing.ZeroHash, err
		}
		if err = w.Close(); err != nil {
			return plumbing.ZeroHash, err
		}
		h, err := st.SetEncodedObject(blob)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: path, Mode: filemode.Regular, Hash: h})
	}
	for dir, sub := range dirs {
		h, err := writeTree(st, sub)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: dir, Mode: filemode.Dir, Hash: h})
	}
	sort.Sort(object.TreeEntrySorter(entries))
	obj := st.NewEncodedObject()
	if err := (&object.Tree{Entries: entries}).Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return st.SetEncodedObject(obj)
}

// blobSHA is the git blob hash of content, the contents API sha of a file.
func blobSHA(content string) string {
	return plumbing.ComputeHash(plumbing.BlobObject, []byte(content)).String()
}

// writeRequest is one recorded contents API write (PUT or DELETE).
type writeRequest struct {
	method  string
	path    string
	message string
	sha     string
	content string
}

// fakeCommitURL is the html_url of every commit gitopsFake answers.
const fakeCommitURL = "https://github.com/acme/gitops/commit/c0ffee"

// gitopsFake is the gitops repo of a store test: remote, which the store
// clones and reads, and the contents API of acme/gitops, which it writes
// through. The writes (PUT and DELETE) are answered in order with statuses,
// then with 200. A 200 commits the write to remote. Each 409 first commits the
// next content of next at path ("" removes it; the last one repeats), as a
// write that won the race would have, then runs onConflict when set: the
// re-read after the 409 sees both.
type gitopsFake struct {
	t          *testing.T
	remote     *remoteRepo
	path       string
	next       []string
	statuses   []int
	onConflict func(*remoteRepo)

	mu     sync.Mutex
	writes []writeRequest
}

// newGitopsFake returns a gitopsFake whose remote holds files.
func newGitopsFake(t *testing.T, files map[string]string) *gitopsFake {
	t.Helper()
	return &gitopsFake{t: t, remote: newRemote(t, files)}
}

// newFileFake returns a gitopsFake around the file at path: the remote holds
// versions[0] there (no file when versions is empty or versions[0] is ""),
// plus extra, and each 409 of statuses commits the next of versions.
func newFileFake(
	t *testing.T, path string, versions []string, statuses []int, extra map[string]string,
) *gitopsFake {
	t.Helper()
	files := maps.Clone(extra)
	if files == nil {
		files = map[string]string{}
	}
	if len(versions) > 0 && versions[0] != "" {
		files[path] = versions[0]
	}
	f := newGitopsFake(t, files)
	f.path, f.statuses = path, statuses
	if len(versions) > 1 {
		f.next = versions[1:]
	}
	return f
}

func (f *gitopsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutPrefix(r.URL.Path, "/repos/acme/gitops/contents/")
	if !ok || (r.Method != http.MethodPut && r.Method != http.MethodDelete) {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
		return
	}
	var body struct {
		Message string `json:"message"`
		SHA     string `json:"sha"`
		Content []byte `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("decode %s body: %v", r.Method, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, writeRequest{
		method: r.Method, path: path, message: body.Message, sha: body.SHA, content: string(body.Content),
	})
	status := http.StatusOK
	if len(f.statuses) > 0 {
		status, f.statuses = f.statuses[0], f.statuses[1:]
	}
	switch {
	case status == http.StatusOK && r.Method == http.MethodPut:
		f.remote.set(path, string(body.Content))
	case status == http.StatusOK:
		f.remote.set(path, "")
	case status == http.StatusConflict:
		if len(f.next) > 0 {
			f.remote.set(f.path, f.next[0])
			if len(f.next) > 1 {
				f.next = f.next[1:]
			}
		}
		if f.onConflict != nil {
			f.onConflict(f.remote)
		}
	}
	w.WriteHeader(status)
	if status != http.StatusOK {
		_, _ = io.WriteString(w, `{"message":"nope"}`)
		return
	}
	_, _ = fmt.Fprintf(w, `{"commit":{"html_url":%q}}`, fakeCommitURL)
}

// requests returns the writes received so far.
func (f *gitopsFake) requests() []writeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

// open returns a store over f: it clones f.remote and writes through f.
func (f *gitopsFake) open(t *testing.T, opts ...store.Option) *store.GitHubStore {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return openStore(t, srv, f.remote.url(), opts...)
}

// openStore returns a store that clones remoteURL into its own temporary
// directory, removed at the end of the test, and writes through the contents
// API served by srv. The trailing slash on BaseURL is required by go-github's
// URL resolution.
func openStore(t *testing.T, srv *httptest.Server, remoteURL string, opts ...store.Option) *store.GitHubStore {
	t.Helper()
	c := gh.NewClient(nil)
	if srv != nil {
		u, err := url.Parse(srv.URL + "/")
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}
		c.BaseURL, c.UploadURL = u, u
	}
	st, err := store.Open(context.Background(), c, "acme", "gitops", store.CloneConfig{URL: remoteURL}, opts...)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := st.Close(); closeErr != nil {
			t.Errorf("close store: %v", closeErr)
		}
	})
	return st
}

// readStore returns a store over a remote holding files, with no contents
// API.
func readStore(t *testing.T, files map[string]string, opts ...store.Option) (*store.GitHubStore, *remoteRepo) {
	t.Helper()
	remote := newRemote(t, files)
	return openStore(t, nil, remote.url(), opts...), remote
}
