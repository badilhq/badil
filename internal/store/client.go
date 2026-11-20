package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	gh "github.com/google/go-github/v71/github"
	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// MaxWriteRetries is the cap on 409-conflict retries for WritePreviewWithRetry.
const MaxWriteRetries = 3

// Sentinel errors. Callers should test with errors.Is.
var (
	ErrNotFound     = errors.New("gitops: not found")
	ErrUnauthorized = errors.New("gitops: unauthorized")
)

// GitHubStore is the gitops repo read from a git clone and written through the
// GitHub contents API, with a go-github client scoped to its owner/repo.
type GitHubStore struct {
	gh      *gh.Client
	owner   string
	repo    string
	metrics *metrics.Metrics
	clone   *gitClone
}

// Option configures a GitHubStore built by Open.
type Option func(*GitHubStore)

// WithMetrics times WritePreviewWithRetry, TeardownPreviewWithRetry (and
// UnpinServicesWithRetry, as the teardown operation), SyncPreviewWithRetry
// and the clone's git clone, ls-remote and fetch in m, and counts the
// previews WritePreviewWithRetry creates.
func WithMetrics(m *metrics.Metrics) Option {
	return func(c *GitHubStore) {
		c.metrics = m
	}
}

// Open clones the gitops repo as clone says and binds the gh client to
// owner/repo, which it writes to. owner is the GitHub organization or user;
// repo is the bare repository name. The clone is timed as the clone
// operation; a failed clone is an error, ErrUnauthorized when the remote or
// the credential provider refused it.
func Open(
	ctx context.Context, client *gh.Client, owner, repo string, clone CloneConfig, opts ...Option,
) (*GitHubStore, error) {
	c := &GitHubStore{gh: client, owner: owner, repo: repo}
	for _, opt := range opts {
		opt(c)
	}
	cl, err := openClone(ctx, clone, c.observe)
	if err != nil {
		return nil, err
	}
	c.clone = cl
	return c, nil
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

// classifyHTTPError translates a go-github response into our sentinel errors
// for status codes the caller cares about. Other errors pass through unchanged.
func classifyHTTPError(resp *gh.Response, err error) error {
	if resp == nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	return err
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

// FullName returns the gitops repo as owner/repo.
func (c *GitHubStore) FullName() string {
	return c.owner + "/" + c.repo
}

// previewsDir is the gitops repo directory holding one subdirectory per preview.
const previewsDir = "previews"

// ListPreviews returns the sorted names of the directories under previews/,
// all of them. A missing previews/ directory yields an empty slice.
func (c *GitHubStore) ListPreviews(ctx context.Context) ([]string, error) {
	return c.clone.listDirs(ctx, previewsDir)
}

// previewPath returns the gitops repo path for a preview slug's values file.
func previewPath(slug string) string {
	return fmt.Sprintf("previews/%s/values.yaml", slug)
}

// GetPreview reads previews/<slug>/values.yaml from the gitops clone and
// returns the parsed values plus the file's blob SHA, the contents API SHA
// subsequent updates need. Returns ErrNotFound when the file does not exist.
func (c *GitHubStore) GetPreview(ctx context.Context, slug string) (*preview.PreviewValues, string, error) {
	raw, sha, err := c.clone.readFile(ctx, previewPath(slug))
	if err != nil {
		return nil, "", err
	}
	var vals preview.PreviewValues
	if err = yaml.Unmarshal(raw, &vals); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", previewPath(slug), err)
	}
	return &vals, sha, nil
}

// marshalPreview encodes vals with the same indentation rules used for the
// golden fixture (2-space indent).
func marshalPreview(vals preview.PreviewValues) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(vals); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WritePreview persists vals at previews/<slug>/values.yaml through the
// contents API. When sha is empty the file is created; otherwise an update is
// performed against that SHA. Returns the html_url of the resulting commit.
// The next read checks the gitops remote.
func (c *GitHubStore) WritePreview(
	ctx context.Context,
	slug string,
	vals preview.PreviewValues,
	sha, message string,
) (string, error) {
	body, err := marshalPreview(vals)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	opts := &gh.RepositoryContentFileOptions{
		Message: gh.Ptr(message),
		Content: body,
	}
	if sha != "" {
		opts.SHA = gh.Ptr(sha)
	}
	// Create and update are the same contents PUT; sha is only set on update.
	out, resp, writeErr := c.gh.Repositories.UpdateFile(ctx, c.owner, c.repo, previewPath(slug), opts)
	// Whatever its outcome, the write may have moved the branch: the next
	// read, a 409 re-read included, checks the remote.
	c.clone.invalidate()
	if writeErr != nil {
		return "", classifyHTTPError(resp, writeErr)
	}
	if out == nil {
		return "", fmt.Errorf("write %s: empty response", previewPath(slug))
	}
	return out.Commit.GetHTMLURL(), nil
}

// statusCode pulls the HTTP status code out of a go-github error. Returns 0
// when the error is not an HTTP error.
func statusCode(err error) int {
	var ghErr *gh.ErrorResponse
	if errors.As(err, &ghErr) && ghErr.Response != nil {
		return ghErr.Response.StatusCode
	}
	return 0
}

// WriteResult is the outcome of a successful WritePreviewWithRetry call.
// CommitMessage is whichever of {createMessage, updateMessage} was actually
// used for the gitops commit, so callers can label PR summaries consistently
// with what GitHub shows when the user clicks through to the commit.
//
// Unchanged is true when skipUnchanged skipped the write: Values then holds
// the fetched file, and CommitURL and CommitMessage are empty. Created is true
// when the write created the file (the first attempt on a missing file); a
// write after a 409 re-fetch is an update. Excluded is the projection's
// (preview.Projection.Excluded).
type WriteResult struct {
	Values        preview.PreviewValues
	Excluded      []string
	CommitURL     string
	CommitMessage string
	Unchanged     bool
	Created       bool
}

// WritePreviewWithRetry composes Get → (Build|Update)PreviewValues → Write and
// retries up to MaxWriteRetries times on 409 conflicts (someone else updated
// the file between our read and write). On every retry the latest file is
// re-fetched and the deploy is re-applied via preview.UpdatePreviewValues, so
// the merge is always against the freshest baseline.
//
// On a fresh create the full catalog is materialized via BuildPreviewValues so
// the resulting values.yaml lists every service (the triggering one pinned to
// commitSha, the rest tracking their branch).
//
// After every build or merge, the file is projected on catalog
// (applyCatalog). The written file follows the current catalog.
//
// With skipUnchanged, when the file exists and the values to write differ
// from it only in serviceName's updated-at (preview.EqualExceptUpdatedAt),
// nothing is written: on the first read and after every 409 re-fetch. The
// result is then Unchanged.
//
// Returns a WriteResult capturing the final PreviewValues that was written,
// the gitops commit URL, and the commit message used — callers (e.g. the
// deploy handler) can render PR summaries without re-fetching.
//
// Its duration, retries included, is observed as the write operation, and a
// write that Created the file increments the previews gauge.
func (c *GitHubStore) WritePreviewWithRetry(
	ctx context.Context,
	slug string,
	catalog *ServiceCatalog,
	serviceName, commitSha string,
	inputs preview.DeployInputs,
	createMessage, updateMessage string,
	skipUnchanged bool,
) (*WriteResult, error) {
	start := time.Now()
	res, err := c.writePreviewWithRetry(
		ctx, slug, catalog, serviceName, commitSha, inputs, createMessage, updateMessage, skipUnchanged,
	)
	c.observe("write", start, err)
	if err == nil && res.Created {
		c.metrics.PreviewCreated()
	}
	return res, err
}

// RepoURL returns the git URL of the GitHub repo owner/repo:
// https://github.com/<owner>/<repo>.git.
func RepoURL(owner, repo string) string {
	return "https://github.com/" + owner + "/" + repo + ".git"
}

// gitopsRepoURL returns the git URL of the gitops repo (GITOPS_REPO).
func (c *GitHubStore) gitopsRepoURL() string {
	return RepoURL(c.owner, c.repo)
}

// applyCatalog returns vals projected on catalog (preview.ApplyCatalog) with
// its hosts (preview.ApplyHosts), and logs a dependency left out because a
// kept pinned entry has its name.
func (c *GitHubStore) applyCatalog(
	ctx context.Context, vals preview.PreviewValues, slug string, catalog *ServiceCatalog,
) preview.Projection {
	cat := catalog.Catalog
	cat.GitopsRepoURL = c.gitopsRepoURL()
	projected := preview.ApplyCatalog(vals, cat)
	for _, name := range projected.Shadowed {
		slog.WarnContext(ctx, "dependency left out: a pinned service has its name",
			slog.String("slug", slug),
			slog.String("name", name),
		)
	}
	projected.Values = preview.ApplyHosts(projected.Values, slug, catalog.URLs)
	return projected
}

// writePreviewWithRetry is WritePreviewWithRetry, untimed.
func (c *GitHubStore) writePreviewWithRetry(
	ctx context.Context,
	slug string,
	catalog *ServiceCatalog,
	serviceName, commitSha string,
	inputs preview.DeployInputs,
	createMessage, updateMessage string,
	skipUnchanged bool,
) (*WriteResult, error) {
	existing, sha, err := c.GetPreview(ctx, slug)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	var proj preview.Projection
	var message string
	if exists {
		updated := preview.UpdatePreviewValues(*existing, serviceName, commitSha, inputs)
		proj = c.applyCatalog(ctx, updated, slug, catalog)
		if skipUnchanged && preview.EqualExceptUpdatedAt(*existing, proj.Values, serviceName) {
			return &WriteResult{Values: *existing, Excluded: proj.Excluded, Unchanged: true}, nil
		}
		message = updateMessage
	} else {
		proj = c.applyCatalog(
			ctx, preview.BuildPreviewValues(serviceName, commitSha, catalog.Services, inputs), slug, catalog,
		)
		message = createMessage
	}

	creating := !exists
	for attempt := 1; attempt <= MaxWriteRetries; attempt++ {
		commitURL, writeErr := c.WritePreview(ctx, slug, proj.Values, sha, message)
		if writeErr == nil {
			return &WriteResult{
				Values: proj.Values, Excluded: proj.Excluded, CommitURL: commitURL, CommitMessage: message,
				Created: creating,
			}, nil
		}
		if statusCode(writeErr) != http.StatusConflict {
			return nil, writeErr
		}
		// 409 — re-fetch, re-merge, retry.
		latest, latestSHA, getErr := c.GetPreview(ctx, slug)
		if getErr != nil {
			return nil, fmt.Errorf("conflict re-fetch: %w", getErr)
		}
		sha = latestSHA
		updated := preview.UpdatePreviewValues(*latest, serviceName, commitSha, inputs)
		proj = c.applyCatalog(ctx, updated, slug, catalog)
		if skipUnchanged && preview.EqualExceptUpdatedAt(*latest, proj.Values, serviceName) {
			return &WriteResult{Values: *latest, Excluded: proj.Excluded, Unchanged: true}, nil
		}
		message = updateMessage
		creating = false
	}
	return nil, fmt.Errorf("write %s: %d attempts exhausted", previewPath(slug), MaxWriteRetries)
}

// TeardownResult is the outcome of TeardownPreviewWithRetry and
// UnpinServicesWithRetry. Removed lists the services unpinned from the PR; it
// is empty when there was nothing to tear down. Values, Excluded
// (preview.Projection.Excluded) and Catalog, the badil.yaml the file was
// projected on, are set only when Deleted is false.
type TeardownResult struct {
	Values        preview.PreviewValues
	Excluded      []string
	Catalog       *ServiceCatalog
	Removed       []string
	Deleted       bool
	CommitURL     string
	CommitMessage string
}

// TeardownPreviewWithRetry unpins every service of previews/<slug>/values.yaml
// deployed by the PR owner/repo#prNumber. The file is deleted when no pin
// remains. Otherwise it is updated, projected on badil.yaml after the unpin
// (applyCatalog), so that an unpinned entry gets its branch-tracking imageTag
// in the same commit: badil.yaml is read once, before the first update, and a
// read or parse error fails the teardown with nothing written. A deletion
// does not read badil.yaml. On a 409 conflict the file is re-fetched, the
// unpin re-applied and update vs delete decided again, up to MaxWriteRetries
// attempts. A missing file (on read, re-fetch or delete) or a PR that pinned
// nothing is a no-op: an empty TeardownResult and a nil error.
//
// Its duration, retries included, is observed as the teardown operation.
func (c *GitHubStore) TeardownPreviewWithRetry(
	ctx context.Context,
	slug, owner, repo string,
	prNumber int,
) (*TeardownResult, error) {
	start := time.Now()
	unpin := func(vals preview.PreviewValues) (preview.PreviewValues, []string, bool) {
		return preview.UnpinPR(vals, owner, repo, prNumber)
	}
	res, err := c.unpinWithRetry(ctx, "teardown", slug, unpin, false)
	c.observe("teardown", start, err)
	return res, err
}

// UnpinServicesWithRetry unpins, in previews/<slug>/values.yaml, the entries
// deployed by the PR owner/repo#prNumber whose service is in services
// (preview.UnpinPRServices); the PR's other entries stay pinned. It is
// TeardownPreviewWithRetry except that the file is never deleted: it is
// updated, projected on badil.yaml, even when no pin remains, so Deleted is
// always false.
//
// Its duration, retries included, is observed as the teardown operation.
func (c *GitHubStore) UnpinServicesWithRetry(
	ctx context.Context,
	slug, owner, repo string,
	prNumber int,
	services []string,
) (*TeardownResult, error) {
	start := time.Now()
	unpin := func(vals preview.PreviewValues) (preview.PreviewValues, []string, bool) {
		return preview.UnpinPRServices(vals, owner, repo, prNumber, services)
	}
	res, err := c.unpinWithRetry(ctx, "unpin", slug, unpin, true)
	c.observe("teardown", start, err)
	return res, err
}

// unpinWithRetry applies unpin to previews/<slug>/values.yaml and writes the
// result, as TeardownPreviewWithRetry describes. The file is deleted when no
// pin remains, unless keepFile is set. op prefixes its errors.
func (c *GitHubStore) unpinWithRetry(
	ctx context.Context,
	op, slug string,
	unpin func(preview.PreviewValues) (preview.PreviewValues, []string, bool),
	keepFile bool,
) (*TeardownResult, error) {
	existing, sha, err := c.GetPreview(ctx, slug)
	if errors.Is(err, ErrNotFound) {
		return &TeardownResult{}, nil
	}
	if err != nil {
		return nil, err
	}

	var catalog *ServiceCatalog
	for attempt := 1; attempt <= MaxWriteRetries; attempt++ {
		vals, removed, pinned := unpin(*existing)
		if len(removed) == 0 {
			return &TeardownResult{}, nil
		}
		update := pinned || keepFile
		if update && catalog == nil {
			if catalog, err = c.FetchServiceCatalog(ctx); err != nil {
				return nil, fmt.Errorf("%s %s: %w", op, previewPath(slug), err)
			}
		}
		result, writeErr := c.writeUnpinned(ctx, slug, sha, vals, removed, update, catalog)
		if writeErr == nil {
			return result, nil
		}
		if statusCode(writeErr) != http.StatusConflict {
			return nil, writeErr
		}
		// 409 — re-fetch, re-apply the unpin, decide again.
		existing, sha, err = c.GetPreview(ctx, slug)
		if errors.Is(err, ErrNotFound) {
			return &TeardownResult{}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("conflict re-fetch: %w", err)
		}
	}
	return nil, fmt.Errorf("%s %s: %d attempts exhausted", op, previewPath(slug), MaxWriteRetries)
}

// writeUnpinned writes vals, the file at blob sha once removed were unpinned:
// when update is set, it is updated, projected on catalog (applyCatalog);
// otherwise it is deleted, and an empty result is returned when it is
// already gone.
func (c *GitHubStore) writeUnpinned(
	ctx context.Context,
	slug, sha string,
	vals preview.PreviewValues,
	removed []string,
	update bool,
	catalog *ServiceCatalog,
) (*TeardownResult, error) {
	result := &TeardownResult{Removed: removed, Deleted: !update}
	var err error
	if !update {
		result.CommitMessage = fmt.Sprintf("chore(preview): delete %s preview", slug)
		result.CommitURL, err = c.deletePreview(ctx, slug, sha, result.CommitMessage)
		if errors.Is(err, ErrNotFound) {
			return &TeardownResult{}, nil
		}
		return result, err
	}
	proj := c.applyCatalog(ctx, vals, slug, catalog)
	result.Values, result.Excluded, result.Catalog = proj.Values, proj.Excluded, catalog
	result.CommitMessage = fmt.Sprintf("chore(preview): remove %s from %s", strings.Join(removed, ", "), slug)
	result.CommitURL, err = c.WritePreview(ctx, slug, proj.Values, sha, result.CommitMessage)
	return result, err
}

// SyncResult is the outcome of SyncPreviewWithRetry. Changed is true when
// the file was rewritten; Values, Excluded (preview.Projection.Excluded),
// CommitURL and CommitMessage then describe that write. Notify is true when
// the written file's ordered list of service or dependency names, or any
// entry's host, differs from the file it replaced. NotFound is true when the
// file does not exist.
type SyncResult struct {
	Values        preview.PreviewValues
	Excluded      []string
	CommitURL     string
	CommitMessage string
	Changed       bool
	Notify        bool
	NotFound      bool
}

// SyncPreviewWithRetry rewrites previews/<slug>/values.yaml as its projection
// on catalog (applyCatalog) when that differs from the file (preview.Equal),
// with the message "chore(preview): sync <slug> with badil.yaml". On a 409
// conflict the file is re-fetched and the projection recomputed, up to
// MaxWriteRetries attempts. A file that already matches is not written. A
// missing file (on read or re-fetch) is a no-op: a SyncResult with only
// NotFound set and a nil error.
//
// Its duration, retries included, is observed as the sync operation.
func (c *GitHubStore) SyncPreviewWithRetry(
	ctx context.Context,
	slug string,
	catalog *ServiceCatalog,
) (*SyncResult, error) {
	start := time.Now()
	res, err := c.syncPreviewWithRetry(ctx, slug, catalog)
	c.observe("sync", start, err)
	return res, err
}

// syncPreviewWithRetry is SyncPreviewWithRetry, untimed.
func (c *GitHubStore) syncPreviewWithRetry(
	ctx context.Context,
	slug string,
	catalog *ServiceCatalog,
) (*SyncResult, error) {
	existing, sha, err := c.GetPreview(ctx, slug)
	if errors.Is(err, ErrNotFound) {
		return &SyncResult{NotFound: true}, nil
	}
	if err != nil {
		return nil, err
	}

	message := fmt.Sprintf("chore(preview): sync %s with badil.yaml", slug)
	for attempt := 1; attempt <= MaxWriteRetries; attempt++ {
		proj := c.applyCatalog(ctx, *existing, slug, catalog)
		if preview.Equal(*existing, proj.Values) {
			return &SyncResult{}, nil
		}
		commitURL, writeErr := c.WritePreview(ctx, slug, proj.Values, sha, message)
		if writeErr == nil {
			return &SyncResult{
				Values:        proj.Values,
				Excluded:      proj.Excluded,
				CommitURL:     commitURL,
				CommitMessage: message,
				Changed:       true,
				Notify:        summaryChanged(*existing, proj.Values),
			}, nil
		}
		if statusCode(writeErr) != http.StatusConflict {
			return nil, writeErr
		}
		// 409 — re-fetch and recompute the projection.
		existing, sha, err = c.GetPreview(ctx, slug)
		if errors.Is(err, ErrNotFound) {
			return &SyncResult{NotFound: true}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("conflict re-fetch: %w", err)
		}
	}
	return nil, fmt.Errorf("sync %s: %d attempts exhausted", previewPath(slug), MaxWriteRetries)
}

// summaryChanged reports whether the ordered list of service names, the
// ordered list of dependency names or any entry's host differs between a and
// b: the parts of the PR comment a sync can change.
func summaryChanged(a, b preview.PreviewValues) bool {
	return !slices.EqualFunc(a.Services, b.Services, func(x, y preview.ServiceEntry) bool {
		return x.Name == y.Name && x.Host == y.Host
	}) || !slices.EqualFunc(a.Dependencies, b.Dependencies, func(x, y preview.DependencyEntry) bool {
		return x.Name == y.Name && x.Host == y.Host
	})
}

// DeleteResult is the outcome of DeletePreviewWithRetry. Deleted is false
// when nothing was deleted; Values then is empty. Values holds the content
// that was deleted.
type DeleteResult struct {
	Deleted   bool
	Values    preview.PreviewValues
	CommitURL string
}

// DeletePreviewWithRetry deletes previews/<slug>/values.yaml with message,
// provided stillDelete holds on the file read just before the delete. On a
// 409 conflict the file is re-fetched and stillDelete re-checked, up to
// MaxWriteRetries attempts. A stillDelete returning false, or a missing file
// (on read, re-fetch or delete), deletes nothing: an empty DeleteResult and a
// nil error.
func (c *GitHubStore) DeletePreviewWithRetry(
	ctx context.Context,
	slug, message string,
	stillDelete func(preview.PreviewValues) bool,
) (*DeleteResult, error) {
	existing, sha, err := c.GetPreview(ctx, slug)
	if errors.Is(err, ErrNotFound) {
		return &DeleteResult{}, nil
	}
	if err != nil {
		return nil, err
	}

	if !stillDelete(*existing) {
		return &DeleteResult{}, nil
	}
	for attempt := 1; attempt <= MaxWriteRetries; attempt++ {
		commitURL, delErr := c.deletePreview(ctx, slug, sha, message)
		if delErr == nil {
			return &DeleteResult{Deleted: true, Values: *existing, CommitURL: commitURL}, nil
		}
		if errors.Is(delErr, ErrNotFound) {
			return &DeleteResult{}, nil
		}
		if statusCode(delErr) != http.StatusConflict {
			return nil, delErr
		}
		// 409 — re-fetch and re-check before trying again.
		existing, sha, err = c.GetPreview(ctx, slug)
		if errors.Is(err, ErrNotFound) {
			return &DeleteResult{}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("conflict re-fetch: %w", err)
		}
		if !stillDelete(*existing) {
			return &DeleteResult{}, nil
		}
	}
	return nil, fmt.Errorf("delete %s: %d attempts exhausted", previewPath(slug), MaxWriteRetries)
}

// deletePreview removes previews/<slug>/values.yaml at blob sha through the
// contents API and returns the html_url of the resulting commit. The next read
// checks the gitops remote.
func (c *GitHubStore) deletePreview(ctx context.Context, slug, sha, message string) (string, error) {
	opts := &gh.RepositoryContentFileOptions{
		Message: gh.Ptr(message),
		SHA:     gh.Ptr(sha),
	}
	out, resp, err := c.gh.Repositories.DeleteFile(ctx, c.owner, c.repo, previewPath(slug), opts)
	// As in WritePreview, the next read checks the remote.
	c.clone.invalidate()
	if err != nil {
		return "", classifyHTTPError(resp, err)
	}
	if out == nil {
		return "", fmt.Errorf("delete %s: empty response", previewPath(slug))
	}
	return out.Commit.GetHTMLURL(), nil
}
