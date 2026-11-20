package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// fakeGitops captures calls and returns canned responses. Each field controls
// one of the methods on storeAPI or forge.Forge. When badilYAML is set, the catalog is
// parsed from it instead of being taken from catalog. When started is non-nil,
// FetchServiceCatalog sends "catalog" and TeardownPreviewWithRetry sends
// "teardown" on entry; the test buffers the channel.
type fakeGitops struct {
	catalog     []string
	badilYAML   string
	catalogErr  error
	writeResult *store.WriteResult
	writeErr    error
	fanOutErr   error

	// teardownResult nil means TeardownPreviewWithRetry returns an empty
	// result. A non-nil teardownGate blocks the call until it is closed.
	teardownResult *store.TeardownResult
	teardownErr    error
	teardownGate   chan struct{}
	teardownPanic  bool

	// pr is what GetPullRequest returns, unless prErr is set.
	pr      *forge.PullRequest
	prErr   error
	prCalls int

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
	syncResult map[string]*store.SyncResult
	syncErr    map[string]error
	syncGate   chan struct{}
	syncMu     sync.Mutex
	syncs      []string
	// invalidations counts the Invalidate calls; catalogInvalidations is
	// invalidations when FetchServiceCatalogSHA was last called. syncMu
	// guards both.
	invalidations        int
	catalogInvalidations int

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

func (f *fakeGitops) FetchServiceCatalog(_ context.Context) (*store.ServiceCatalog, error) {
	if f.started != nil {
		f.started <- "catalog"
	}
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	if f.badilYAML != "" {
		return store.ParseCatalog([]byte(f.badilYAML))
	}
	return &store.ServiceCatalog{Catalog: preview.Catalog{Services: f.catalog}}, nil
}

func (f *fakeGitops) WritePreviewWithRetry(
	_ context.Context,
	slug string,
	catalog *store.ServiceCatalog,
	serviceName, _ string,
	inputs preview.DeployInputs,
	_, _ string,
	skipUnchanged bool,
) (*store.WriteResult, error) {
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
) (*store.TeardownResult, error) {
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
		return &store.TeardownResult{}, nil
	}
	if len(f.teardownResult.Removed) == 0 || f.teardownResult.Deleted {
		return f.teardownResult, nil
	}
	// Like the store, an update carries the catalog it was projected on.
	res := *f.teardownResult
	if f.badilYAML != "" {
		res.Catalog, _ = store.ParseCatalog([]byte(f.badilYAML))
	} else {
		res.Catalog = &store.ServiceCatalog{Catalog: preview.Catalog{Services: f.catalog}}
	}
	return &res, nil
}

func (f *fakeGitops) UpsertComment(_ context.Context, owner, repo string, prNumber int, body string) error {
	f.upserts = append(f.upserts, struct {
		owner, repo string
		number      int
		body        string
	}{owner, repo, prNumber, body})
	return nil
}

func (f *fakeGitops) GetPullRequest(_ context.Context, _, _ string, _ int) (*forge.PullRequest, error) {
	f.prCalls++
	if f.prErr != nil {
		return nil, f.prErr
	}
	return f.pr, nil
}

func (f *fakeGitops) ListChangedFiles(context.Context, string, string, int) ([]forge.ChangedFile, error) {
	return nil, nil
}

func (f *fakeGitops) ParseWebhook(*http.Request) (forge.Event, error) {
	return forge.Event{}, errors.New("unexpected ParseWebhook")
}

func (f *fakeGitops) UnpinServicesWithRetry(
	context.Context, string, string, string, int, []string,
) (*store.TeardownResult, error) {
	return &store.TeardownResult{}, nil
}

func (f *fakeGitops) GetPreview(_ context.Context, slug string) (*preview.PreviewValues, string, error) {
	if f.previewErr != nil {
		return nil, "", f.previewErr
	}
	vals, ok := f.previews[slug]
	if !ok {
		return nil, "", store.ErrNotFound
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

func (f *fakeGitops) FetchServiceCatalogSHA(ctx context.Context) (*store.ServiceCatalog, string, error) {
	f.syncMu.Lock()
	f.catalogInvalidations = f.invalidations
	f.syncMu.Unlock()
	f.catalogDeadline, _ = ctx.Deadline()
	catalog, err := f.FetchServiceCatalog(ctx)
	return catalog, f.catalogSHA, err
}

func (f *fakeGitops) SyncPreviewWithRetry(
	_ context.Context,
	slug string,
	_ *store.ServiceCatalog,
) (*store.SyncResult, error) {
	if f.syncGate != nil {
		<-f.syncGate
	}
	f.syncMu.Lock()
	defer f.syncMu.Unlock()
	if _, ok := f.previews[slug]; !ok {
		return &store.SyncResult{NotFound: true}, nil
	}
	f.syncs = append(f.syncs, slug)
	if err := f.syncErr[slug]; err != nil {
		return nil, err
	}
	if res, ok := f.syncResult[slug]; ok {
		return res, nil
	}
	return &store.SyncResult{}, nil
}

func (f *fakeGitops) FullName() string { return "acme/gitops" }

func (f *fakeGitops) Invalidate() {
	f.syncMu.Lock()
	defer f.syncMu.Unlock()
	f.invalidations++
}

// invalidated returns the Invalidate calls so far, and how many came before
// the last FetchServiceCatalogSHA.
func (f *fakeGitops) invalidated() (int, int) {
	f.syncMu.Lock()
	defer f.syncMu.Unlock()
	return f.invalidations, f.catalogInvalidations
}

// withFanOut plugs f's FanOutPRComments in as the server's fan-out.
func withFanOut(f *fakeGitops) Option {
	return func(s *Server) {
		s.fanOut = func(ctx context.Context, _ forge.Forge, vals preview.PreviewValues, body string) error {
			return f.FanOutPRComments(ctx, vals, body)
		}
	}
}

// synced returns the slugs synced so far.
func (f *fakeGitops) synced() []string {
	f.syncMu.Lock()
	defer f.syncMu.Unlock()
	return append([]string(nil), f.syncs...)
}
