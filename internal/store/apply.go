package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/badilhq/badil/internal/lifecycle"
)

// OpDeploy is lifecycle.OpDeploy.
const OpDeploy = lifecycle.OpDeploy

// OpTeardown is lifecycle.OpTeardown.
const OpTeardown = lifecycle.OpTeardown

// OpSweep is lifecycle.OpSweep.
const OpSweep = lifecycle.OpSweep

// OpSync is lifecycle.OpSync.
const OpSync = lifecycle.OpSync

// maxAttempts is how many times Apply runs an operation and pushes it before
// it gives up on conflicts.
const maxAttempts = 5

// Backoff before attempts 2 to maxAttempts: firstWait, doubled each time,
// with up to ±waitJitter random jitter, capped at maxWait.
const (
	firstWait  = 100 * time.Millisecond
	maxWait    = 2 * time.Second
	waitJitter = 0.2
)

// CommitIdentity is the author and committer of the commits Apply pushes.
type CommitIdentity struct {
	Name, Email string
}

// Summary is lifecycle.Summary.
type Summary = lifecycle.Summary

// Op is lifecycle.Op.
type Op = lifecycle.Op

// Result is lifecycle.Result.
type Result = lifecycle.Result

// Apply runs op on the gitops repo as one commit. Operations are serialised
// in the process. Each attempt fetches the default branch, runs op.Change on
// the tree of the fetched head and, unless it changed nothing, commits the
// change on that head as the store's identity, unsigned, and pushes it
// without force. A push that fails while the remote head moved since the
// fetch is a conflict: it is counted, and the next attempt starts again from
// a new fetch, after a backoff, up to maxAttempts attempts. Any other push
// failure is returned, as ErrUnauthorized when the remote refused the
// credentials. Its duration, retries included, is observed as op.Kind. Once
// a push landed, Apply calls the store's WithApplied callback with its
// result.
//
// An Apply waiting for the one before it gives up when ctx is done, with
// ctx's error and nothing run, observed as a failed op.Kind.
func (c *GitHubStore) Apply(ctx context.Context, op Op) (Result, error) {
	if err := c.acquire(ctx); err != nil {
		c.observe(op.Kind, time.Now(), err)
		return Result{}, err
	}
	defer func() { <-c.applySlot }()
	start := time.Now()
	res, err := c.apply(ctx, op)
	c.observe(op.Kind, start, err)
	if err == nil && !res.NoOp && c.applied != nil {
		c.applied(res)
	}
	return res, err
}

// acquire takes the store's Apply slot, waiting for the Apply that holds
// it, or returns ctx's error when ctx is done first.
func (c *GitHubStore) acquire(ctx context.Context) error {
	select {
	case c.applySlot <- struct{}{}:
		return nil
	default:
	}
	select {
	case c.applySlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for gitops write: %w", ctx.Err())
	}
}

// apply is Apply, unserialised and untimed.
func (c *GitHubStore) apply(ctx context.Context, op Op) (Result, error) {
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			if err := c.sleep(ctx, backoff(attempt-1)); err != nil {
				return Result{}, err
			}
		}
		res, parent, err := c.attempt(ctx, op)
		var failed *pushError
		if !errors.As(err, &failed) {
			return res, err
		}
		pushErr := failed.err
		if errors.Is(pushErr, ErrUnauthorized) {
			return Result{}, pushErr
		}
		if err = c.clone.fetch(ctx); err != nil {
			return Result{}, err
		}
		head, err := c.clone.lockedRemoteHead()
		if err != nil {
			return Result{}, err
		}
		if head == parent {
			return Result{}, pushErr
		}
		c.metrics.PushConflict(op.Kind)
		if attempt == maxAttempts {
			return Result{}, fmt.Errorf("push gitops repo: %d attempts conflicted: %w", attempt, pushErr)
		}
	}
}

// pushError is the error of an attempt whose push failed, err.
type pushError struct {
	err error
}

func (p *pushError) Error() string { return p.err.Error() }
func (p *pushError) Unwrap() error { return p.err }

// attempt is one attempt of apply: fetch, Change, commit and push. It
// returns the result and the parent commit. A failed push is a
// *pushError.
func (c *GitHubStore) attempt(ctx context.Context, op Op) (Result, plumbing.Hash, error) {
	if err := c.clone.fetch(ctx); err != nil {
		return Result{}, plumbing.ZeroHash, err
	}
	parent, tree, summary, err := c.change(op)
	if err != nil {
		return Result{}, parent, err
	}
	if !tree.staged() {
		return c.noop(parent)
	}
	commit, err := c.clone.commit(parent, tree, c.message(summary, tree), c.identity)
	if err != nil {
		return Result{}, parent, err
	}
	if commit.IsZero() {
		return c.noop(parent)
	}
	if err = c.clone.push(ctx, commit); err != nil {
		return Result{}, parent, &pushError{err: err}
	}
	if err = c.clone.advance(parent, commit); err != nil {
		return Result{}, parent, err
	}
	return Result{
		Commit: commit.String(), Subject: summary.Subject, URL: c.commitURL(commit), Deleted: tree.deleted(),
	}, parent, nil
}

// noop returns the result of an attempt that changed nothing on parent.
func (c *GitHubStore) noop(parent plumbing.Hash) (Result, plumbing.Hash, error) {
	subject, err := c.clone.subject(parent)
	if err != nil {
		return Result{}, parent, err
	}
	return Result{NoOp: true, Commit: parent.String(), Subject: subject, URL: c.commitURL(parent)}, parent, nil
}

// change runs op.Change on the tree of the remote-tracking ref, holding the
// clone's read lock, and returns that ref's commit, the tree and the summary.
func (c *GitHubStore) change(op Op) (plumbing.Hash, *Tree, Summary, error) {
	c.clone.mu.RLock()
	defer c.clone.mu.RUnlock()
	parent, err := c.clone.remoteHead()
	if err != nil {
		return parent, nil, Summary{}, err
	}
	root, err := c.clone.tree()
	if err != nil {
		return parent, nil, Summary{}, err
	}
	tree := newTree(root, c.gitopsRepoURL())
	summary, err := op.Change(tree)
	return parent, tree, summary, err
}

// message returns the commit message of summary: its subject and, after a
// blank line, tree's body lines.
func (c *GitHubStore) message(summary Summary, tree *Tree) string {
	msg := summary.Subject + "\n"
	if lines := tree.body(); len(lines) > 0 {
		msg += "\n" + strings.Join(lines, "\n") + "\n"
	}
	return msg
}

// commitURL returns the web URL of the gitops repo's commit h.
func (c *GitHubStore) commitURL(h plumbing.Hash) string {
	return c.webBase + "/commit/" + h.String()
}

// backoff returns the wait before attempt n+1: firstWait doubled n-1 times,
// with up to ±waitJitter jitter, capped at maxWait.
func backoff(n int) time.Duration {
	d := firstWait << (n - 1)
	jitter := 1 + waitJitter*(2*rand.Float64()-1) //nolint:gosec // jitter needs no crypto randomness
	return min(time.Duration(float64(d)*jitter), maxWait)
}

// sleepCtx waits d, or returns ctx's error when ctx is done first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lockedRemoteHead is remoteHead under the read lock.
func (c *gitClone) lockedRemoteHead() (plumbing.Hash, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.remoteHead()
}

// subject returns the subject line of the commit h, read under the read
// lock.
func (c *gitClone) subject(h plumbing.Hash) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	commit, err := c.repo.CommitObject(h)
	if err != nil {
		return "", fmt.Errorf("read commit %s: %w", h, err)
	}
	subject, _, _ := strings.Cut(commit.Message, "\n")
	return subject, nil
}

// commit stores, holding the write lock, a commit of tree's files on parent
// with message, authored and committed by id. It returns the zero hash when
// the files leave parent's tree as it was.
func (c *gitClone) commit(parent plumbing.Hash, tree *Tree, message string, id CommitIdentity) (plumbing.Hash, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	treeHash, err := c.writeTree(tree.root, tree.files)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if treeHash.IsZero() {
		// The change emptied the repo: commit the empty tree.
		obj := c.repo.Storer.NewEncodedObject()
		if err = (&object.Tree{}).Encode(obj); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("encode tree: %w", err)
		}
		if treeHash, err = c.repo.Storer.SetEncodedObject(obj); err != nil {
			return plumbing.ZeroHash, err
		}
	}
	if treeHash == tree.root.Hash {
		return plumbing.ZeroHash, nil
	}
	sig := object.Signature{Name: id.Name, Email: id.Email, When: time.Now()}
	commit := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      message,
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{parent},
	}
	obj := c.repo.Storer.NewEncodedObject()
	if err = commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode commit: %w", err)
	}
	return c.repo.Storer.SetEncodedObject(obj)
}

// writeTree stores the tree base (nil for none) with changes applied and
// returns its hash, the zero hash when it is left empty. changes maps a path
// relative to base to its new content, a nil content deleting it.
func (c *gitClone) writeTree(base *object.Tree, changes map[string][]byte) (plumbing.Hash, error) {
	files, dirs := splitChanges(changes)
	current := map[string]object.TreeEntry{}
	names := map[string]bool{}
	if base != nil {
		for _, e := range base.Entries {
			current[e.Name] = e
			names[e.Name] = true
		}
	}
	for name := range files {
		names[name] = true
	}
	for name := range dirs {
		names[name] = true
	}
	var entries []object.TreeEntry
	for name := range names {
		e, ok := current[name]
		if !ok {
			e = object.TreeEntry{Name: name, Mode: filemode.Regular}
			if dirs[name] != nil {
				e.Mode = filemode.Dir
			}
		}
		content, changed := files[name]
		entry, keep, err := c.changeEntry(e, content, changed, dirs[name])
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if keep {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return plumbing.ZeroHash, nil
	}
	sort.Sort(object.TreeEntrySorter(entries))
	obj := c.repo.Storer.NewEncodedObject()
	if err := (&object.Tree{Entries: entries}).Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tree: %w", err)
	}
	return c.repo.Storer.SetEncodedObject(obj)
}

// splitChanges splits changes into the files directly in their tree and,
// by directory name, the changes under each directory.
func splitChanges(changes map[string][]byte) (map[string][]byte, map[string]map[string][]byte) {
	files := map[string][]byte{}
	dirs := map[string]map[string][]byte{}
	for path, content := range changes {
		dir, rest, ok := strings.Cut(path, "/")
		if !ok {
			files[path] = content
			continue
		}
		if dirs[dir] == nil {
			dirs[dir] = map[string][]byte{}
		}
		dirs[dir][rest] = content
	}
	return files, dirs
}

// changeEntry returns the tree entry e once changed: a directory by
// dirChanges, a file by content when changed (a nil content deletes it). It
// reports false when the entry is gone. A new entry has a zero hash.
func (c *gitClone) changeEntry(
	e object.TreeEntry, content []byte, changed bool, dirChanges map[string][]byte,
) (object.TreeEntry, bool, error) {
	switch {
	case dirChanges != nil && e.Mode == filemode.Dir:
		var sub *object.Tree
		if !e.Hash.IsZero() {
			var err error
			if sub, err = c.repo.TreeObject(e.Hash); err != nil {
				return e, false, fmt.Errorf("read tree %s: %w", e.Name, err)
			}
		}
		h, err := c.writeTree(sub, dirChanges)
		return object.TreeEntry{Name: e.Name, Mode: filemode.Dir, Hash: h}, !h.IsZero(), err
	case dirChanges != nil:
		return e, false, fmt.Errorf("write %s/: a file has its name", e.Name)
	case !changed:
		return e, true, nil
	case content == nil:
		return e, false, nil
	}
	h, err := c.writeBlob(content)
	return object.TreeEntry{Name: e.Name, Mode: filemode.Regular, Hash: h}, err == nil, err
}

// writeBlob stores content as a blob and returns its hash.
func (c *gitClone) writeBlob(content []byte) (plumbing.Hash, error) {
	obj := c.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err = w.Write(content); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err = w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return c.repo.Storer.SetEncodedObject(obj)
}

// push pushes commit to the remote branch without force, holding the read
// lock. The remote-tracking ref is left alone. A refusal of the credentials
// is ErrUnauthorized.
func (c *gitClone) push(ctx context.Context, commit plumbing.Hash) error {
	auth, err := c.auth(ctx)
	if err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	// A remote without fetch refspecs: the push updates no local ref.
	remote := git.NewRemote(c.repo.Storer, &config.RemoteConfig{Name: remoteName, URLs: []string{c.url}})
	refspec := config.RefSpec(commit.String() + ":" + plumbing.NewBranchReferenceName(c.branch).String())
	err = remote.PushContext(ctx, &git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{refspec},
		Auth:       auth,
	})
	if err != nil {
		return fmt.Errorf("push gitops repo: %w", classifyGitError(err))
	}
	return nil
}

// advance sets the remote-tracking ref to commit, pushed on parent, unless a
// fetch moved it from parent since.
func (c *gitClone) advance(parent, commit plumbing.Hash) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	head, err := c.remoteHead()
	if err != nil || head != parent {
		return err
	}
	return c.repo.Storer.SetReference(plumbing.NewHashReference(c.trackingRef(), commit))
}
