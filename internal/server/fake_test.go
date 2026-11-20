package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

// testIdentity is the commit identity of the fake's store.
var testIdentity = store.CommitIdentity{ //nolint:gochecknoglobals // test fixture
	Name:  "badil-test[bot]",
	Email: "1+badil-test[bot]@users.noreply.github.com",
}

// fakeGitops is the store and the forge of a server test. Its catalog is
// parsed from badilYAML when set, or lists catalog, each service at
// https://github.com/acme/<name>.git. Apply runs on a real store over a
// remote holding that badil.yaml, the files of previews and of synced (each
// projected on the catalog, so that a sync leaves it alone, unless it is in
// stale) and raw; the remote is made by open. Apply fails with applyErr when
// set. When started is non-nil, FetchServiceCatalog sends "catalog" and Apply
// sends "apply" on entry; the test buffers the channel. A non-nil applyGate
// blocks Apply until it is closed; applyPanic makes it panic.
//
// After each pushed commit, the fake records from its subject and body the
// last service deployed (capturedSlug, capturedService and capturedInputs,
// read back from the pushed file), the last preview a PR left (teardownSlug,
// teardownOwner, teardownRepo, teardownNumber) and the previews synced.
type fakeGitops struct {
	t          *testing.T
	catalog    []string
	badilYAML  string
	catalogErr error
	applyErr   error
	fanOutErr  error
	applyGate  chan struct{}
	applyPanic bool

	// pr is what GetPullRequest returns, unless prErr is set.
	pr      *forge.PullRequest
	prErr   error
	prCalls int

	previews map[string]*preview.PreviewValues
	stale    map[string]bool
	raw      map[string]string

	started chan string

	// catalogSHA is the blob SHA FetchServiceCatalogSHA returns with the
	// catalog; catalogDeadline is the deadline of its last call.
	catalogSHA      string
	catalogDeadline time.Time
	// syncMu guards syncs, invalidations and catalogInvalidations.
	syncMu sync.Mutex
	syncs  []string
	// invalidations counts the Invalidate calls; catalogInvalidations is
	// invalidations when FetchServiceCatalogSHA was last called.
	invalidations        int
	catalogInvalidations int

	remote *storetest.Remote
	st     *store.GitHubStore

	capturedInputs   preview.DeployInputs
	capturedURLs     preview.URLConfig
	capturedService  string
	capturedSlug     string
	fanOutBodyCalled bool
	capturedSummary  string

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

// badil returns the fake's badil.yaml.
func (f *fakeGitops) badil() string {
	if f.badilYAML != "" {
		return f.badilYAML
	}
	var b strings.Builder
	b.WriteString("services:\n")
	for _, name := range f.catalog {
		b.WriteString("  " + name + ":\n    repo: https://github.com/acme/" + name + ".git\n")
	}
	if len(f.catalog) == 0 {
		b.WriteString("  backend-1:\n    repo: https://github.com/acme/backend-1.git\n")
	}
	return b.String()
}

// open makes the fake's remote and store, once, for t.
func (f *fakeGitops) open(t *testing.T) {
	t.Helper()
	if f.st != nil {
		return
	}
	f.t = t
	files := map[string]string{"badil.yaml": f.badil()}
	catalog, _ := store.ParseCatalog([]byte(f.badil()))
	for slug, vals := range f.previews {
		v := *vals
		if catalog != nil && !f.stale[slug] {
			v = projectOn(slug, v, catalog)
		}
		out, err := yaml.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", slug, err)
		}
		files["previews/"+slug+"/values.yaml"] = string(out)
	}
	for path, content := range f.raw {
		files[path] = content
	}
	f.remote = storetest.NewRemote(t, files)
	st, err := store.Open(context.Background(), "acme", "gitops", store.CloneConfig{URL: f.remote.URL()}, testIdentity)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.st = st
}

// projectOn returns vals projected on catalog, as a sync writes it.
func projectOn(slug string, vals preview.PreviewValues, catalog *store.ServiceCatalog) preview.PreviewValues {
	cat := catalog.Catalog
	cat.GitopsRepoURL = "https://github.com/acme/gitops.git"
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	defer slog.SetDefault(prev)
	return preview.ApplyHosts(preview.ApplyCatalog(vals, cat).Values, slug, catalog.URLs)
}

func (f *fakeGitops) FetchServiceCatalog(_ context.Context) (*store.ServiceCatalog, error) {
	if f.started != nil {
		f.started <- "catalog"
	}
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	catalog, err := store.ParseCatalog([]byte(f.badil()))
	if err == nil {
		f.capturedURLs = catalog.URLs
	}
	return catalog, err
}

// prRef matches the owner/repo#number a commit subject names.
var prRef = regexp.MustCompile(`([^\s/]+)/([^\s/#]+)#(\d+)`)

func (f *fakeGitops) Apply(ctx context.Context, op store.Op) (store.Result, error) {
	if f.started != nil {
		f.started <- "apply"
	}
	if f.applyGate != nil {
		<-f.applyGate
	}
	if f.applyPanic {
		panic("fake apply panic")
	}
	if f.applyErr != nil {
		return store.Result{}, f.applyErr
	}
	res, err := f.st.Apply(ctx, op)
	if err != nil || res.NoOp {
		return res, err
	}
	f.record(f.remote.Commits()[0])
	return res, nil
}

// record records what the pushed commit c did.
func (f *fakeGitops) record(c storetest.Commit) {
	for _, line := range c.Body() {
		slug, change, _ := strings.Cut(line, ": ")
		switch {
		case strings.HasPrefix(change, "pin "):
			service, _, _ := strings.Cut(strings.TrimPrefix(change, "pin "), "@")
			f.capturedSlug, f.capturedService = slug, service
			f.captureInputs(c, slug, service)
		case strings.HasPrefix(change, "unpin "), change == "delete":
			f.teardownSlug = slug
			if m := prRef.FindStringSubmatch(c.Subject()); m != nil {
				f.teardownOwner, f.teardownRepo = m[1], m[2]
				f.teardownNumber, _ = strconv.Atoi(m[3])
			}
		case change == "sync":
			f.syncMu.Lock()
			f.syncs = append(f.syncs, slug)
			f.syncMu.Unlock()
		}
	}
}

// captureInputs sets capturedInputs from the entry of service in the preview
// slug of c.
func (f *fakeGitops) captureInputs(c storetest.Commit, slug, service string) {
	var vals preview.PreviewValues
	if err := yaml.Unmarshal([]byte(c.Tree["previews/"+slug+"/values.yaml"]), &vals); err != nil {
		f.t.Errorf("parse %s: %v", slug, err)
		return
	}
	for _, svc := range vals.Services {
		if svc.Name != service || svc.Metadata == nil {
			continue
		}
		f.capturedInputs = preview.DeployInputs{
			ServiceName: service, CommitSha: svc.CommitSha, HeadRef: svc.Metadata.VCSRefName,
			PRAuthor: svc.Metadata.PRAuthor, PRURL: svc.Metadata.PRURL, PRNumber: svc.Metadata.PRNumber,
			Timestamp: svc.Metadata.UpdatedAt, WorkflowRunURL: svc.Metadata.PipelineRunURL,
		}
	}
}

func (f *fakeGitops) FanOutPRComments(_ context.Context, _ preview.PreviewValues, body string) error {
	f.fanOutBodyCalled = true
	f.capturedSummary = body
	return f.fanOutErr
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

func (f *fakeGitops) FetchServiceCatalogSHA(ctx context.Context) (*store.ServiceCatalog, string, error) {
	f.syncMu.Lock()
	f.catalogInvalidations = f.invalidations
	f.syncMu.Unlock()
	f.catalogDeadline, _ = ctx.Deadline()
	catalog, err := f.FetchServiceCatalog(ctx)
	return catalog, f.catalogSHA, err
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

// synced returns the previews pushed commits synced so far.
func (f *fakeGitops) synced() []string {
	f.syncMu.Lock()
	defer f.syncMu.Unlock()
	return append([]string(nil), f.syncs...)
}

// headURL returns the GitHub URL of the remote's head commit.
func (f *fakeGitops) headURL() string {
	return "https://github.com/acme/gitops/commit/" + f.remote.Head()
}

// pinnedBy42 is a preview entry of backend-1 pinned by acme/backend-1#42.
func pinnedBy42() preview.ServiceEntry {
	return preview.ServiceEntry{
		Name: "backend-1", CommitSha: "aaaaaaaaaa",
		Metadata: &preview.Metadata{PRURL: "https://github.com/acme/backend-1/pull/42", PRNumber: "42"},
	}
}
