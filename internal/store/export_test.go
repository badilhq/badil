package store

import "time"

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
