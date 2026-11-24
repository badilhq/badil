package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/badilhq/badil/internal/lifecycle"
)

// remoteCheckTTL is how long a successful ls-remote of the branch head is
// trusted before the next read checks the remote again.
const remoteCheckTTL = 5 * time.Second

// maxPacks is the pack count past which a fetch consolidates the clone into
// one pack, the default of git's gc.autoPackLimit: every fetch adds a pack,
// and every object lookup scans the index of each pack.
const maxPacks = 50

// remoteName is the name of the gitops remote in the clone.
const remoteName = "origin"

// CredentialProvider returns the user and password of HTTPS basic auth to the
// gitops remote. It is called before every clone, ls-remote, fetch and push.
type CredentialProvider func(ctx context.Context) (user, password string, err error)

// CloneConfig is where and how the store clones the gitops repo.
type CloneConfig struct {
	// URL is the gitops repo's remote URL.
	URL string
	// Dir is the clone directory, removed and cloned again on start. When it
	// is "", the store clones into its own os.MkdirTemp directory, which
	// Close removes.
	Dir string
	// Credentials authenticates the git calls; nil makes them without auth.
	Credentials CredentialProvider
}

// checkCall is one ls-remote (and fetch) in flight, shared by the reads that
// wait for it. gen is the forced-check generation it started at.
type checkCall struct {
	done chan struct{}
	gen  uint64
	err  error
}

// gitClone is a full clone of the gitops repo's default branch. Reads come
// from the tree of the fetched commit, refs/remotes/origin/<branch>, after
// ensureFresh.
type gitClone struct {
	repo    *git.Repository
	url     string
	dir     string
	tempDir bool
	branch  string
	creds   CredentialProvider
	observe func(op string, start time.Time, err error)
	// headMoved is called after a fetch moved the remote-tracking ref, nil
	// for none.
	headMoved func()
	now       func() time.Time

	// mu is held for reading by reads of the tree and for writing by a fetch.
	mu sync.RWMutex

	// checkMu guards the fields below it.
	checkMu sync.Mutex
	// gen counts the forced checks; checkedGen is the gen the last successful
	// check started at, and checkedAt when it started.
	gen        uint64
	checkedGen uint64
	checkedAt  time.Time
	inflight   *checkCall
}

// openClone clones cfg.URL as cfg says, timing the clone with observe. Each
// later fetch that moves the remote-tracking ref calls headMoved, when not
// nil.
func openClone(
	ctx context.Context, cfg CloneConfig, observe func(string, time.Time, error), headMoved func(),
) (_ *gitClone, err error) {
	c := &gitClone{
		url: cfg.URL, dir: cfg.Dir, creds: cfg.Credentials, observe: observe, headMoved: headMoved, now: time.Now,
	}
	if c.dir == "" {
		if c.dir, err = os.MkdirTemp("", "badil-gitops-"); err != nil {
			return nil, fmt.Errorf("gitops clone dir: %w", err)
		}
		c.tempDir = true
	} else if err = os.RemoveAll(c.dir); err != nil {
		return nil, fmt.Errorf("gitops clone dir: %w", err)
	}
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()

	start := time.Now()
	err = c.clone(ctx)
	c.observe("clone", start, err)
	if err != nil {
		return nil, fmt.Errorf("clone gitops repo: %w", err)
	}
	// The clone counts as a check made when it started.
	c.checkedAt = start
	return c, nil
}

// clone makes the full clone of c.url's default branch into c.dir and keeps
// the branch its HEAD names, for the process lifetime.
func (c *gitClone) clone(ctx context.Context) error {
	auth, err := c.auth(ctx)
	if err != nil {
		return err
	}
	repo, err := git.PlainCloneContext(ctx, c.dir, true, &git.CloneOptions{
		URL:          c.url,
		Auth:         auth,
		RemoteName:   remoteName,
		SingleBranch: true,
		Tags:         git.NoTags,
	})
	if err != nil {
		return classifyGitError(err)
	}
	head, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return fmt.Errorf("read HEAD: %w", err)
	}
	if head.Type() != plumbing.SymbolicReference || !head.Target().IsBranch() {
		return fmt.Errorf("HEAD is not a branch: %s", head)
	}
	commit, err := repo.Reference(head.Target(), true)
	if err != nil {
		return fmt.Errorf("read %s: %w", head.Target(), err)
	}
	c.repo = repo
	c.branch = head.Target().Short()
	// A single-branch clone of HEAD tracks no branch: the remote-tracking ref
	// reads and fetches use starts at the cloned commit.
	return repo.Storer.SetReference(plumbing.NewHashReference(c.trackingRef(), commit.Hash()))
}

// Close removes the clone directory when openClone created it with
// os.MkdirTemp. A directory given in CloneConfig.Dir is kept.
func (c *gitClone) Close() error {
	if !c.tempDir {
		return nil
	}
	return os.RemoveAll(c.dir)
}

// auth returns the basic auth of c.creds, nil without a provider. A provider
// error is lifecycle.ErrUnauthorized and keeps its cause in the chain, so a
// refresh cut short by its caller's context is still seen as cancelled by
// ensureFresh.
func (c *gitClone) auth(ctx context.Context) (transport.AuthMethod, error) {
	if c.creds == nil {
		return nil, nil //nolint:nilnil // a nil AuthMethod is go-git's anonymous access, passed as Auth by every caller
	}
	user, password, err := c.creds(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", lifecycle.ErrUnauthorized, err)
	}
	return &githttp.BasicAuth{Username: user, Password: password}, nil
}

// classifyGitError returns err as lifecycle.ErrUnauthorized when the remote
// refused the credentials; other errors pass through unchanged.
func classifyGitError(err error) error {
	if errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed) {
		return fmt.Errorf("%w: %w", lifecycle.ErrUnauthorized, err)
	}
	return err
}

// trackingRef is the remote-tracking ref of the branch, the commit reads use.
func (c *gitClone) trackingRef() plumbing.ReferenceName {
	return plumbing.NewRemoteReferenceName(remoteName, c.branch)
}

// remoteHead returns the commit of the remote-tracking ref.
func (c *gitClone) remoteHead() (plumbing.Hash, error) {
	ref, err := c.repo.Reference(c.trackingRef(), true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("read %s: %w", c.trackingRef(), err)
	}
	return ref.Hash(), nil
}

// invalidate makes the next read check the remote, whatever the TTL.
func (c *gitClone) invalidate() {
	c.checkMu.Lock()
	c.gen++
	c.checkMu.Unlock()
}

// ensureFresh checks the remote branch head unless a check that started at
// the current forced-check generation succeeded under remoteCheckTTL ago, and
// fetches when the head moved. Concurrent callers share one check in flight;
// one that started before a forced check is waited for, then another starts.
func (c *gitClone) ensureFresh(ctx context.Context) error {
	for {
		c.checkMu.Lock()
		if c.checkedGen == c.gen && c.now().Sub(c.checkedAt) < remoteCheckTTL {
			c.checkMu.Unlock()
			return nil
		}
		if call := c.inflight; call != nil {
			current := call.gen == c.gen
			c.checkMu.Unlock()
			select {
			case <-call.done:
			case <-ctx.Done():
				return ctx.Err()
			}
			if !current {
				continue
			}
			// A check cancelled with its starter's context is retried by a
			// caller whose own context is still live.
			if call.err != nil && ctx.Err() == nil &&
				(errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded)) {
				continue
			}
			return call.err
		}
		call := &checkCall{done: make(chan struct{}), gen: c.gen}
		c.inflight = call
		c.checkMu.Unlock()

		startedAt := c.now()
		call.err = c.check(ctx)

		c.checkMu.Lock()
		c.inflight = nil
		if call.err == nil {
			c.checkedGen, c.checkedAt = call.gen, startedAt
		}
		c.checkMu.Unlock()
		close(call.done)
		return call.err
	}
}

// check runs ls-remote for the branch and fetches it when its head differs
// from the remote-tracking ref.
func (c *gitClone) check(ctx context.Context) error {
	remote, err := c.lsRemote(ctx)
	if err != nil {
		return err
	}
	c.mu.RLock()
	local, err := c.remoteHead()
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	if local == remote {
		return nil
	}
	return c.fetch(ctx)
}

// lsRemote returns the commit of refs/heads/<branch> on the remote, timed as
// the ls_remote operation.
func (c *gitClone) lsRemote(ctx context.Context) (_ plumbing.Hash, err error) {
	start := time.Now()
	defer func() { c.observe("ls_remote", start, err) }()
	auth, err := c.auth(ctx)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	remote, err := c.repo.Remote(remoteName)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("ls-remote gitops repo: %w", classifyGitError(err))
	}
	branch := plumbing.NewBranchReferenceName(c.branch)
	for _, ref := range refs {
		if ref.Name() == branch {
			return ref.Hash(), nil
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("ls-remote gitops repo: %s not found", branch)
}

// fetch updates the remote-tracking ref of the branch, holding the write lock,
// timed as the fetch operation. When the ref moved, it calls headMoved once
// the lock is released.
func (c *gitClone) fetch(ctx context.Context) (err error) {
	start := time.Now()
	defer func() { c.observe("fetch", start, err) }()
	auth, err := c.auth(ctx)
	if err != nil {
		return err
	}
	moved, err := c.lockedFetch(ctx, auth)
	if err != nil {
		return err
	}
	if moved && c.headMoved != nil {
		c.headMoved()
	}
	return nil
}

// lockedFetch fetches the branch into the remote-tracking ref, holding the
// write lock, and reports whether the ref moved.
func (c *gitClone) lockedFetch(ctx context.Context, auth transport.AuthMethod) (bool, error) {
	refspec := config.RefSpec(fmt.Sprintf("+%s:%s", plumbing.NewBranchReferenceName(c.branch), c.trackingRef()))
	c.mu.Lock()
	defer c.mu.Unlock()
	before, err := c.remoteHead()
	if err != nil {
		return false, err
	}
	err = c.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{refspec},
		Auth:       auth,
		Tags:       git.NoTags,
	})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("fetch gitops repo: %w", classifyGitError(err))
	}
	if repackErr := c.repack(); repackErr != nil {
		slog.WarnContext(ctx, "repack gitops clone", slog.String("err", repackErr.Error()))
	}
	after, err := c.remoteHead()
	if err != nil {
		return false, err
	}
	return after != before, nil
}

// repack consolidates the clone's packs into one when there are more than
// maxPacks. The caller holds c.mu for writing. A failure leaves every object
// readable: the new pack is complete before any old one is deleted.
func (c *gitClone) repack() error {
	// PlainCloneContext stores the clone in a *filesystem.Storage.
	fs, ok := c.repo.Storer.(*filesystem.Storage)
	if !ok {
		return fmt.Errorf("gitops clone storage is %T, not *filesystem.Storage", c.repo.Storer)
	}
	packs, err := fs.ObjectPacks()
	if err != nil || len(packs) <= maxPacks {
		return err
	}
	err = c.repo.RepackObjects(&git.RepackConfig{})
	// RepackObjects deletes the old packs but leaves their indexes in the
	// object storage's memory: drop them so lookups read the packs on disk.
	fs.Reindex()
	return err
}

// tree returns the tree of the remote-tracking ref's commit. The caller holds
// c.mu for reading.
func (c *gitClone) tree() (*object.Tree, error) {
	head, err := c.remoteHead()
	if err != nil {
		return nil, err
	}
	commit, err := c.repo.CommitObject(head)
	if err != nil {
		return nil, fmt.Errorf("read commit %s: %w", head, err)
	}
	return commit.Tree()
}

// readFile returns the content and blob SHA of path in the fetched commit, or
// lifecycle.ErrNotFound when it has no such file.
func (c *gitClone) readFile(ctx context.Context, path string) ([]byte, string, error) {
	if err := c.ensureFresh(ctx); err != nil {
		return nil, "", err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	tree, err := c.tree()
	if err != nil {
		return nil, "", err
	}
	content, hash, err := readTreeFile(tree, path)
	if err != nil {
		return nil, "", err
	}
	return content, hash.String(), nil
}

// readTreeFile returns the content and blob hash of path in root, or
// lifecycle.ErrNotFound when it has no such file.
func readTreeFile(root *object.Tree, path string) ([]byte, plumbing.Hash, error) {
	f, err := root.File(path)
	if errors.Is(err, object.ErrFileNotFound) {
		return nil, plumbing.ZeroHash, fmt.Errorf("%w: %s", lifecycle.ErrNotFound, path)
	}
	if err != nil {
		return nil, plumbing.ZeroHash, fmt.Errorf("read %s: %w", path, err)
	}
	content, err := f.Contents()
	if err != nil {
		return nil, plumbing.ZeroHash, fmt.Errorf("read %s: %w", path, err)
	}
	return []byte(content), f.Hash, nil
}

// listDirs returns the sorted names of the directories directly under dir in
// the fetched commit; none when dir does not exist.
func (c *gitClone) listDirs(ctx context.Context, dir string) ([]string, error) {
	if err := c.ensureFresh(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	return dirNames(tree, dir)
}

// dirNames returns the sorted names of the directories directly under dir in
// root; none when dir does not exist.
func dirNames(root *object.Tree, dir string) ([]string, error) {
	sub, err := root.Tree(dir)
	if errors.Is(err, object.ErrDirectoryNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	names := make([]string, 0, len(sub.Entries))
	for _, e := range sub.Entries {
		if e.Mode == filemode.Dir {
			names = append(names, e.Name)
		}
	}
	slices.Sort(names)
	return names, nil
}

// listPreviewFiles returns the sorted slugs of the directories under
// previews/ that hold a values.yaml in the fetched commit.
func (c *gitClone) listPreviewFiles(ctx context.Context) ([]string, error) {
	if err := c.ensureFresh(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	dirs, err := dirNames(tree, previewsDir)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(dirs))
	for _, slug := range dirs {
		_, findErr := tree.FindEntry(previewPath(slug))
		if errors.Is(findErr, object.ErrEntryNotFound) || errors.Is(findErr, object.ErrDirectoryNotFound) {
			continue
		}
		if findErr != nil {
			return nil, fmt.Errorf("read %s: %w", previewPath(slug), findErr)
		}
		slugs = append(slugs, slug)
	}
	return slugs, nil
}
