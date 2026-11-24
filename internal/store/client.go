package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// Sentinel errors. Callers should test with errors.Is.
var (
	ErrNotFound     = errors.New("gitops: not found")
	ErrUnauthorized = errors.New("gitops: unauthorized")
)

// GitHubStore is the gitops repo, read from a git clone and written by
// pushing commits to it (Apply).
type GitHubStore struct {
	// repoURL is the URL the derived names come from.
	repoURL string
	// fullName is repoURL's repository path, owner/repo on GitHub.
	fullName string
	// webBase is repoURL's scheme, host and fullName, the base of commit links.
	webBase  string
	identity CommitIdentity
	// parsePR is the forge's pull request URL rule, given to the trees of
	// Apply.
	parsePR preview.PRURLParser
	metrics *metrics.Metrics
	clone   *gitClone
	// applySlot serialises Apply: it holds one token while an Apply runs.
	applySlot chan struct{}
	// sleep waits between Apply's attempts.
	sleep func(ctx context.Context, d time.Duration) error
	// applied is called with the result of every Apply whose push landed,
	// nil when unset.
	applied func(Result)
	// headMoved is called after every fetch that moved the remote-tracking
	// ref, nil when unset.
	headMoved func()
}

// Option configures a GitHubStore built by Open.
type Option func(*GitHubStore)

// WithMetrics times Apply, as its operation kind, and the clone's git clone,
// ls-remote and fetch in m, and counts Apply's push conflicts.
func WithMetrics(m *metrics.Metrics) Option {
	return func(c *GitHubStore) {
		c.metrics = m
	}
}

// WithRepoURL makes the full name, the commit links and the preview files'
// gitopsRepoURL derive from u instead of CloneConfig.URL: for a store that
// clones a mirror of the repo at u.
func WithRepoURL(u string) Option {
	return func(c *GitHubStore) {
		c.repoURL = u
	}
}

// WithApplied calls f with the result of every Apply whose push landed,
// whichever caller ran it; not after a no-op or a failed Apply. f runs on
// Apply's goroutine before Apply returns: it must not block.
func WithApplied(f func(Result)) Option {
	return func(c *GitHubStore) {
		c.applied = f
	}
}

// WithHeadMoved calls f after every fetch of the clone that moved the
// remote-tracking ref of the default branch, Apply's fetches included. f runs
// on the fetching goroutine, after the clone's locks are released: it must
// not block.
func WithHeadMoved(f func()) Option {
	return func(c *GitHubStore) {
		c.headMoved = f
	}
}

// Open clones the gitops repo at clone.URL, which is required, as clone
// says. Names derive from clone.URL unless WithRepoURL is set; a URL without
// a repository path of two segments is an error. Apply commits
// as identity, whose Name and Email must be set. The clone is timed as the
// clone operation; a failed clone is an error, ErrUnauthorized when the
// remote or the credential provider refused it. parsePR, the forge's pull
// request URL rule (forge.Forge.ParsePRURL), is required: Tree.Teardown and
// Tree.UnpinServices match pr-urls with it.
func Open(
	ctx context.Context, clone CloneConfig, identity CommitIdentity, parsePR preview.PRURLParser, opts ...Option,
) (*GitHubStore, error) {
	if clone.URL == "" {
		return nil, errors.New("gitops repo URL is required")
	}
	if identity.Name == "" || identity.Email == "" {
		return nil, errors.New("gitops commit identity: name and email are required")
	}
	if parsePR == nil {
		return nil, errors.New("pull request URL parser is required")
	}
	c := &GitHubStore{identity: identity, parsePR: parsePR, applySlot: make(chan struct{}, 1), sleep: sleepCtx}
	for _, opt := range opts {
		opt(c)
	}
	if c.repoURL == "" {
		c.repoURL = clone.URL
	}
	var err error
	if c.fullName, c.webBase, err = repoNames(c.repoURL); err != nil {
		return nil, err
	}
	cl, err := openClone(ctx, clone, c.observe, c.headMoved)
	if err != nil {
		return nil, err
	}
	c.clone = cl
	return c, nil
}

// RepoFullName returns the full name of a repo from its URL path: the path
// without its leading "/", a trailing "/" and then a trailing ".git". The
// second result is false when that leaves fewer than two segments or an empty
// one.
func RepoFullName(path string) (string, bool) {
	fullName := strings.TrimPrefix(path, "/")
	fullName = strings.TrimSuffix(strings.TrimSuffix(fullName, "/"), ".git")
	segments := strings.Split(fullName, "/")
	return fullName, len(segments) >= 2 && !slices.Contains(segments, "")
}

// repoNames returns the full name of the repo at raw (RepoFullName) and its
// web base, raw's scheme and host followed by the full name.
func repoNames(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", errors.New("gitops repo URL: not a URL")
	}
	fullName, ok := RepoFullName(u.Path)
	if !ok {
		return "", "", fmt.Errorf("gitops repo URL: repository path %q needs at least two segments", u.Path)
	}
	return fullName, u.Scheme + "://" + u.Host + "/" + fullName, nil
}

// Close removes the clone directory when the store created it (an unset
// CloneConfig.Dir).
func (c *GitHubStore) Close() error {
	return c.clone.Close()
}

// Invalidate makes the next read check the gitops remote at once, whatever
// the time since the last check: after a push to the gitops repo.
func (c *GitHubStore) Invalidate() {
	c.clone.invalidate()
}

// observe records the duration since start of the gitops operation op, which
// ended with err.
func (c *GitHubStore) observe(op string, start time.Time, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	c.metrics.GitopsOp(op, result, time.Since(start))
}

// FetchServiceCatalog reads badil.yaml from the gitops clone and returns its
// validated service catalog (the keys of its services mapping, with each
// service's repo and branch) and `preview` block. A malformed file is an
// error.
func (c *GitHubStore) FetchServiceCatalog(ctx context.Context) (*ServiceCatalog, error) {
	catalog, _, err := c.FetchServiceCatalogSHA(ctx)
	return catalog, err
}

// FetchServiceCatalogSHA is FetchServiceCatalog, plus the blob SHA of the
// badil.yaml it read on the default branch. When the file was read but its
// content can't be parsed, the SHA is returned with the error; it is "" when
// the file could not be read.
func (c *GitHubStore) FetchServiceCatalogSHA(ctx context.Context) (*ServiceCatalog, string, error) {
	raw, sha, err := c.clone.readFile(ctx, configFile)
	if err != nil {
		return nil, "", err
	}
	catalog, err := ParseCatalog(raw)
	return catalog, sha, err
}

// FullName returns the gitops repo's repository path, owner/repo on GitHub.
func (c *GitHubStore) FullName() string {
	return c.fullName
}

// previewsDir is the gitops repo directory holding one subdirectory per preview.
const previewsDir = "previews"

// ListPreviews returns the sorted names of the directories under previews/,
// all of them. A missing previews/ directory yields an empty slice.
func (c *GitHubStore) ListPreviews(ctx context.Context) ([]string, error) {
	return c.clone.listDirs(ctx, previewsDir)
}

// ListPreviewFiles returns the sorted slugs of the previews whose
// previews/<slug>/values.yaml is in the gitops clone.
func (c *GitHubStore) ListPreviewFiles(ctx context.Context) ([]string, error) {
	return c.clone.listPreviewFiles(ctx)
}

// previewPath returns the gitops repo path for a preview slug's values file.
func previewPath(slug string) string {
	return fmt.Sprintf("previews/%s/values.yaml", slug)
}

// GetPreview reads previews/<slug>/values.yaml from the gitops clone and
// returns the parsed values plus the file's blob SHA. Returns ErrNotFound
// when the file does not exist.
func (c *GitHubStore) GetPreview(ctx context.Context, slug string) (*preview.Values, string, error) {
	raw, sha, err := c.clone.readFile(ctx, previewPath(slug))
	if err != nil {
		return nil, "", err
	}
	vals, err := parsePreview(slug, raw)
	if err != nil {
		return nil, "", err
	}
	return vals, sha, nil
}

// parsePreview parses raw, the content of previews/<slug>/values.yaml.
func parsePreview(slug string, raw []byte) (*preview.Values, error) {
	var vals preview.Values
	if err := yaml.Unmarshal(raw, &vals); err != nil {
		return nil, fmt.Errorf("parse %s: %w", previewPath(slug), err)
	}
	return &vals, nil
}

// gitopsRepoURL returns the git URL of the gitops repo written into the
// preview files.
func (c *GitHubStore) gitopsRepoURL() string {
	return c.repoURL
}

// previewIndent is the indentation of the preview files badil writes.
const previewIndent = 2

// marshalPreview encodes vals with the same indentation rules used for the
// golden fixture (2-space indent).
func marshalPreview(vals preview.Values) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(previewIndent)
	if err := enc.Encode(vals); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
