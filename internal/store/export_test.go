package store

import (
	"context"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/badilhq/badil/internal/preview"
)

// SetNow makes c's freshness checks read the time from now.
func SetNow(c *GitHubStore, now func() time.Time) {
	c.clone.checkMu.Lock()
	defer c.clone.checkMu.Unlock()
	c.clone.now = now
}

// CloneDir returns the directory of c's clone.
func CloneDir(c *GitHubStore) string {
	return c.clone.dir
}

// MaxPacks is the pack count past which a fetch repacks the clone.
const MaxPacks = maxPacks

// SetSleep makes c's Apply wait between attempts with sleep.
func SetSleep(c *GitHubStore, sleep func(ctx context.Context, d time.Duration) error) {
	c.sleep = sleep
}

// Backoff is the wait before attempt n+1 of Apply.
func Backoff(n int) time.Duration {
	return backoff(n)
}

// MaxAttempts is how many times Apply pushes before it gives up on conflicts.
const MaxAttempts = maxAttempts

// PutPreview puts vals at previews/<slug>/values.yaml in t.
func PutPreview(t *Tree, slug string, vals preview.Values) error {
	return t.putPreview(slug, vals)
}

// CommitURL is the web URL of c's gitops commit hash.
func CommitURL(c *GitHubStore, hash string) string {
	return c.commitURL(plumbing.NewHash(hash))
}

// GitopsRepoURL is the gitopsRepoURL c writes into the preview files.
func GitopsRepoURL(c *GitHubStore) string {
	return c.gitopsRepoURL()
}
