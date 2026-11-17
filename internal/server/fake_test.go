package server

import (
	"context"
	"sync"
	"time"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/preview"
)

// fakeGitops captures calls and returns canned responses. Each field controls
// one of the methods on gitopsAPI. When badilYAML is set, the catalog is
// parsed from it instead of being taken from catalog. When started is non-nil,
// FetchServiceCatalog sends "catalog" and TeardownPreviewWithRetry sends
// "teardown" on entry; the test buffers the channel.
type fakeGitops struct {
	catalog     []string
	badilYAML   string
	catalogErr  error
	writeResult *gitops.WriteResult
	writeErr    error
	fanOutErr   error

	// teardownResult nil means TeardownPreviewWithRetry returns an empty
	// result. A non-nil teardownGate blocks the call until it is closed.
	teardownResult *gitops.TeardownResult
	teardownErr    error
	teardownGate   chan struct{}
	teardownPanic  bool

	// pr is what GetPullRequest returns, unless prErr is set.
	pr      *gitops.PullRequest
	prErr   error
	prCalls int

	// files is what ListPullRequestFiles returns, unless filesErr is set.
	files      []gitops.ChangedFile
	filesErr   error
	filesCalls int
	// unpinResult is what UnpinServicesWithRetry returns (an empty result
	// when nil), unless unpinErr is set; unpins holds the services of each
	// call.
	unpinResult *gitops.TeardownResult
	unpinErr    error
	unpins      [][]string

	// previews is what GetPreview reads, unless previewErr is set; an unknown
	// slug is ErrNotFound.
	previews   map[string]*preview.PreviewValues
	previewErr error
	// listed is what ListPreviews returns, unless listErr is set.
	listed    []string
	listErr   error
	listCalls int

	started chan string

	// catalogSHA is the blob SHA FetchServiceCatalogSHA returns with the
	// catalog; catalogDeadline is the deadline of its last call.
	catalogSHA      string
	catalogDeadline time.Time
	// syncResult is what SyncPreviewWithRetry returns for a slug (an empty
	// result when unset), unless syncErr has an error for it. A non-nil
	// syncGate blocks the call until it is closed. syncMu guards syncs, the
	// slugs synced so far.
	syncResult map[string]*gitops.SyncResult
	syncErr    map[string]error
	syncGate   chan struct{}
	syncMu     sync.Mutex
	syncs      []string

	capturedInputs   preview.DeployInputs
	capturedCatalog  []string
	capturedURLs     preview.URLConfig
	capturedService  string
	capturedSlug     string
	fanOutBodyCalled bool
	capturedSummary  string
	// capturedSkipUnchanged holds the skipUnchanged of each write, in order.
	capturedSkipUnchanged []bool

	teardownSlug   string
	teardownOwner  string
	teardownRepo   string
	teardownNumber int
	upserts        []struct {
		owner, repo string
		number      int
		body        string
	}
}

func (f *fakeGitops) FetchServiceCatalog(_ context.Context) (*gitops.ServiceCatalog, error) {
	if f.started != nil {
		f.started <- "catalog"
	}
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	if f.badilYAML != "" {
		return gitops.ParseCatalog([]byte(f.badilYAML))
	}
	return &gitops.ServiceCatalog{Catalog: preview.Catalog{Services: f.catalog}}, nil
}

func (f *fakeGitops) WritePreviewWithRetry(
	_ context.Context,
	slug string,
	catalog *gitops.ServiceCatalog,
	serviceName, _ string,
	inputs preview.DeployInputs,
	_, _ string,
	skipUnchanged bool,
) (*gitops.WriteResult, error) {
	f.capturedSkipUnchanged = append(f.capturedSkipUnchanged, skipUnchanged)
	f.capturedSlug = slug
	f.capturedCatalog = catalog.Services
	f.capturedURLs = catalog.URLs
	f.capturedService = serviceName
	f.capturedInputs = inputs
	if f.writeResult == nil {
		return nil, f.writeErr
	}
	result := *f.writeResult
	result.Values = preview.ApplyHosts(f.writeResult.Values, slug, catalog.URLs)
	return &result, f.writeErr
}

func (f *fakeGitops) FanOutPRComments(_ context.Context, _ preview.PreviewValues, body string) error {
	f.fanOutBodyCalled = true
	f.capturedSummary = body
	return f.fanOutErr
}

func (f *fakeGitops) TeardownPreviewWithRetry(
	_ context.Context,
	slug, owner, repo string,
	prNumber int,
) (*gitops.TeardownResult, error) {
	if f.started != nil {
		f.started <- "teardown"
	}
	f.teardownSlug, f.teardownOwner, f.teardownRepo, f.teardownNumber = slug, owner, repo, prNumber
	if f.teardownGate != nil {
		<-f.teardownGate
	}
	if f.teardownPanic {
		panic("fake teardown panic")
	}
	if f.teardownErr != nil {
		return nil, f.teardownErr
	}
	if f.teardownResult == nil {
		return &gitops.TeardownResult{}, nil
	}
	if len(f.teardownResult.Removed) == 0 || f.teardownResult.Deleted {
		return f.teardownResult, nil
	}
	// Like the client, an update carries the catalog it was projected on.
	res := *f.teardownResult
	if f.badilYAML != "" {
		res.Catalog, _ = gitops.ParseCatalog([]byte(f.badilYAML))
	} else {
		res.Catalog = &gitops.ServiceCatalog{Catalog: preview.Catalog{Services: f.catalog}}
	}
	return &res, nil
}

func (f *fakeGitops) UpsertPRComment(_ context.Context, owner, repo string, prNumber int, body string) error {
	f.upserts = append(f.upserts, struct {
		owner, repo string
		number      int
		body        string
	}{owner, repo, prNumber, body})
	return nil
}

func (f *fakeGitops) GetPullRequest(_ context.Context, _, _ string, _ int) (*gitops.PullRequest, error) {
	f.prCalls++
	if f.prErr != nil {
		return nil, f.prErr
	}
	return f.pr, nil
}

func (f *fakeGitops) ListPullRequestFiles(context.Context, string, string, int) ([]gitops.ChangedFile, error) {
	f.filesCalls++
	if f.filesErr != nil {
		return nil, f.filesErr
	}
	return f.files, nil
}

func (f *fakeGitops) UnpinServicesWithRetry(
	_ context.Context,
	_, _, _ string,
	_ int,
	services []string,
) (*gitops.TeardownResult, error) {
	f.unpins = append(f.unpins, services)
	if f.unpinErr != nil {
		return nil, f.unpinErr
	}
	if f.unpinResult == nil {
		return &gitops.TeardownResult{}, nil
	}
	return f.unpinResult, nil
}

func (f *fakeGitops) GetPreview(_ context.Context, slug string) (*preview.PreviewValues, string, error) {
	if f.previewErr != nil {
		return nil, "", f.previewErr
	}
	vals, ok := f.previews[slug]
	if !ok {
		return nil, "", gitops.ErrNotFound
	}
	return vals, "blob-" + slug, nil
}

func (f *fakeGitops) ListPreviews(context.Context) ([]string, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listed, nil
}

func (f *fakeGitops) FetchServiceCatalogSHA(ctx context.Context) (*gitops.ServiceCatalog, string, error) {
	f.catalogDeadline, _ = ctx.Deadline()
	catalog, err := f.FetchServiceCatalog(ctx)
	return catalog, f.catalogSHA, err
}

func (f *fakeGitops) SyncPreviewWithRetry(
	_ context.Context,
	slug string,
	_ *gitops.ServiceCatalog,
) (*gitops.SyncResult, error) {
	if f.syncGate != nil {
		<-f.syncGate
	}
	f.syncMu.Lock()
	defer f.syncMu.Unlock()
	if _, ok := f.previews[slug]; !ok {
		return &gitops.SyncResult{NotFound: true}, nil
	}
	f.syncs = append(f.syncs, slug)
	if err := f.syncErr[slug]; err != nil {
		return nil, err
	}
	if res, ok := f.syncResult[slug]; ok {
		return res, nil
	}
	return &gitops.SyncResult{}, nil
}

func (f *fakeGitops) FullName() string { return "acme/gitops" }

// synced returns the slugs synced so far.
func (f *fakeGitops) synced() []string {
	f.syncMu.Lock()
	defer f.syncMu.Unlock()
	return append([]string(nil), f.syncs...)
}
