// Package storetest provides bare gitops repositories for tests of the store
// and of its callers: remotes a store clones, fetches from and pushes to,
// in-process, with no git binary.
package storetest

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
)

// Branch is the default branch of the remotes: not main, so a store has to
// find it from its clone's HEAD.
const Branch = "trunk"

// The file transport is installed once per process, for every remote.
var (
	installOnce sync.Once  //nolint:gochecknoglobals // the transport is process-wide
	hooksMu     sync.Mutex //nolint:gochecknoglobals // guards hooks
	// hooks holds, by remote directory, what runs before each push to it.
	hooks = map[string]func() error{} //nolint:gochecknoglobals // read by the process-wide transport
)

// hookTransport serves file:// remotes in-process and runs a remote's push
// hook before each push session to it.
type hookTransport struct {
	transport.Transport
}

func (h hookTransport) NewReceivePackSession(
	ep *transport.Endpoint, auth transport.AuthMethod,
) (transport.ReceivePackSession, error) {
	hooksMu.Lock()
	hook := hooks[ep.Path]
	hooksMu.Unlock()
	if hook != nil {
		if err := hook(); err != nil {
			return nil, err
		}
	}
	return h.Transport.NewReceivePackSession(ep, auth)
}

// install serves file:// remotes in-process, once per process.
func install() {
	installOnce.Do(func() {
		client.InstallProtocol("file", hookTransport{server.NewClient(server.DefaultLoader)})
	})
}

// Remote is a bare repository whose branch Branch holds files.
type Remote struct {
	t   testing.TB
	dir string
	mu  sync.Mutex
}

// NewRemote returns a bare repository in a temporary directory whose one
// commit holds files (path to content).
func NewRemote(t testing.TB, files map[string]string) *Remote {
	t.Helper()
	install()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName(Branch)},
		Bare:        true,
	})
	if err != nil {
		t.Fatalf("init remote: %v", err)
	}
	r := &Remote{t: t, dir: dir}
	t.Cleanup(func() { r.OnPush(nil) })
	r.commit(repo, maps.Clone(files), nil)
	return r
}

// URL returns the remote's file:// URL.
func (r *Remote) URL() string {
	return "file://" + r.dir
}

// Dir returns the remote's directory.
func (r *Remote) Dir() string {
	return r.dir
}

// OnPush runs hook before each push to the remote, before the remote
// advertises its branch: a commit hook makes is one the push races with, an
// error it returns fails the push. A nil hook removes it.
func (r *Remote) OnPush(hook func() error) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	if hook == nil {
		delete(hooks, r.dir)
		return
	}
	hooks[r.dir] = hook
}

// Set commits content at path; "" removes path.
func (r *Remote) Set(path, content string) {
	r.Change(map[string]string{path: content})
}

// Change commits files over the branch's files; a "" content removes its
// path.
func (r *Remote) Change(files map[string]string) {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	repo := r.open()
	head := headHash(r.t, repo)
	current := filesAt(r.t, repo, head)
	for path, content := range files {
		if content == "" {
			delete(current, path)
			continue
		}
		current[path] = content
	}
	r.commit(repo, current, []plumbing.Hash{head})
}

// Files returns the files of the branch head (path to content).
func (r *Remote) Files() map[string]string {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	repo := r.open()
	return filesAt(r.t, repo, headHash(r.t, repo))
}

// Head returns the commit of the branch head.
func (r *Remote) Head() string {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return headHash(r.t, r.open()).String()
}

// Commit is a commit of the remote.
type Commit struct {
	Hash      string
	Message   string
	Author    object.Signature
	Committer object.Signature
	Parents   []string
	Signed    bool
	// Tree is the commit's files (path to content).
	Tree map[string]string
}

// Subject returns the first line of the commit's message.
func (c Commit) Subject() string {
	subject, _, _ := strings.Cut(c.Message, "\n")
	return subject
}

// Body returns the lines of the commit's message after its subject and the
// blank line that follows it.
func (c Commit) Body() []string {
	_, rest, _ := strings.Cut(strings.TrimRight(c.Message, "\n"), "\n")
	rest = strings.TrimPrefix(rest, "\n")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "\n")
}

// Commits returns the commits of the branch, from the head back along first
// parents, the head first.
func (r *Remote) Commits() []Commit {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	repo := r.open()
	var out []Commit
	h := headHash(r.t, repo)
	for !h.IsZero() {
		c, err := repo.CommitObject(h)
		if err != nil {
			r.t.Fatalf("read commit %s: %v", h, err)
		}
		parents := make([]string, 0, len(c.ParentHashes))
		for _, p := range c.ParentHashes {
			parents = append(parents, p.String())
		}
		out = append(out, Commit{
			Hash: h.String(), Message: c.Message, Author: c.Author, Committer: c.Committer,
			Parents: parents, Signed: c.PGPSignature != "", Tree: filesAt(r.t, repo, h),
		})
		h = plumbing.ZeroHash
		if len(c.ParentHashes) > 0 {
			h = c.ParentHashes[0]
		}
	}
	return out
}

// open opens the repository afresh, so that it sees the packs pushes added.
func (r *Remote) open() *git.Repository {
	r.t.Helper()
	repo, err := git.PlainOpen(r.dir)
	if err != nil {
		r.t.Fatalf("open remote: %v", err)
	}
	return repo
}

// headHash returns the commit of the branch head of repo.
func headHash(t testing.TB, repo *git.Repository) plumbing.Hash {
	t.Helper()
	ref, err := repo.Storer.Reference(plumbing.NewBranchReferenceName(Branch))
	if err != nil {
		t.Fatalf("read %s: %v", Branch, err)
	}
	return ref.Hash()
}

// filesAt returns the files of commit h of repo.
func filesAt(t testing.TB, repo *git.Repository, h plumbing.Hash) map[string]string {
	t.Helper()
	c, err := repo.CommitObject(h)
	if err != nil {
		t.Fatalf("read commit %s: %v", h, err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatalf("read tree of %s: %v", h, err)
	}
	files := map[string]string{}
	err = tree.Files().ForEach(func(f *object.File) error {
		content, contentErr := f.Contents()
		files[f.Name] = content
		return contentErr
	})
	if err != nil {
		t.Fatalf("read files of %s: %v", h, err)
	}
	return files
}

// commit writes files as a commit of repo on Branch with parents. The caller
// holds r.mu, except in NewRemote.
func (r *Remote) commit(repo *git.Repository, files map[string]string, parents []plumbing.Hash) {
	r.t.Helper()
	st := repo.Storer
	tree, err := writeTree(st, files)
	if err != nil {
		r.t.Fatalf("write tree: %v", err)
	}
	sig := object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}
	c := &object.Commit{
		Author: sig, Committer: sig, Message: "test\n", TreeHash: tree, ParentHashes: parents,
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
	branch := plumbing.NewBranchReferenceName(Branch)
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

// HTTPHandler serves the remote over git's smart HTTP protocol for fetches
// (git-upload-pack) under any path, and answers every push (git-receive-pack)
// with pushStatus.
func (r *Remote) HTTPHandler(pushStatus int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/info/refs") &&
			req.URL.Query().Get("service") == transport.UploadPackServiceName:
			r.advertise(w, req)
		case strings.HasSuffix(req.URL.Path, "/"+transport.UploadPackServiceName) && req.Method == http.MethodPost:
			r.uploadPack(w, req)
		case strings.HasSuffix(req.URL.Path, "/info/refs") ||
			strings.HasSuffix(req.URL.Path, "/"+transport.ReceivePackServiceName):
			w.WriteHeader(pushStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// uploadSession opens an upload-pack session on the remote.
func (r *Remote) uploadSession() (transport.UploadPackSession, error) {
	ep, err := transport.NewEndpoint(r.dir)
	if err != nil {
		return nil, err
	}
	return server.DefaultServer.NewUploadPackSession(ep, nil)
}

// advertise answers GET info/refs?service=git-upload-pack.
func (r *Remote) advertise(w http.ResponseWriter, req *http.Request) {
	sess, err := r.uploadSession()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ar, err := sess.AdvertisedReferencesContext(req.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ar.Prefix = [][]byte{[]byte("# service=" + transport.UploadPackServiceName), pktline.Flush}
	var buf bytes.Buffer
	if err = ar.Encode(&buf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-"+transport.UploadPackServiceName+"-advertisement")
	_, _ = w.Write(buf.Bytes())
}

// uploadPack answers POST git-upload-pack: the wants, the haves, then done.
func (r *Remote) uploadPack(w http.ResponseWriter, req *http.Request) {
	upReq := packp.NewUploadPackRequest()
	if err := upReq.UploadRequest.Decode(req.Body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s := pktline.NewScanner(req.Body)
	for s.Scan() {
		line := strings.TrimSpace(string(s.Bytes()))
		if line == "done" {
			break
		}
		if have, ok := strings.CutPrefix(line, "have "); ok {
			upReq.Haves = append(upReq.Haves, plumbing.NewHash(have))
		}
	}
	sess, err := r.uploadSession()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := sess.UploadPack(req.Context(), upReq)
	if err != nil && !errors.Is(err, transport.ErrEmptyUploadPackRequest) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-"+transport.UploadPackServiceName+"-result")
	if resp != nil {
		_ = resp.Encode(w)
	}
}
