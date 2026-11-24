package store_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

// checkTTL is how long a successful remote check is trusted.
const checkTTL = 5 * time.Second

// fakeClock is a clock the test moves forward.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// clockedStore returns a store over a remote holding files, timed in a fresh
// *Metrics, whose freshness checks read the returned clock.
func clockedStore(
	t *testing.T, files map[string]string,
) (*store.GitHubStore, *storetest.Remote, *fakeClock, *metrics.Metrics) {
	t.Helper()
	m := metrics.New()
	st, remote := readStore(t, files, store.WithMetrics(m))
	clock := &fakeClock{t: time.Now()}
	store.SetNow(st, clock.now)
	return st, remote, clock, m
}

// opCounts returns how many gitops operations m timed, keyed by
// "<op>/<result>".
func opCounts(t *testing.T, m *metrics.Metrics) map[string]uint64 {
	t.Helper()
	families, err := m.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	counts := map[string]uint64{}
	for _, mf := range families {
		if mf.GetName() != "badil_gitops_operation_duration_seconds" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range metric.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			counts[labels["op"]+"/"+labels["result"]] = metric.GetHistogram().GetSampleCount()
		}
	}
	return counts
}

// opCount returns how many gitops operations op ended with result.
func opCount(t *testing.T, m *metrics.Metrics, op, result string) uint64 {
	t.Helper()
	return opCounts(t, m)[op+"/"+result]
}

// assertChecks fails t unless m counts lsRemote successful ls-remotes and
// fetches successful fetches.
func assertChecks(t *testing.T, m *metrics.Metrics, lsRemote, fetches uint64) {
	t.Helper()
	gotLs, gotFetch := opCount(t, m, "ls_remote", "ok"), opCount(t, m, "fetch", "ok")
	if gotLs != lsRemote || gotFetch != fetches {
		t.Errorf("ls-remotes, fetches = %d, %d; want %d, %d", gotLs, gotFetch, lsRemote, fetches)
	}
}

// previewFile is a preview file whose only service is name.
func previewFile(name string) string {
	return "services:\n  - name: " + name + "\n"
}

// serviceName reads the preview a through st and returns its first service's
// name.
func serviceName(t *testing.T, st *store.GitHubStore) string {
	t.Helper()
	vals, _, err := st.GetPreview(context.Background(), "a")
	if err != nil {
		t.Fatalf("GetPreview: %v", err)
	}
	return vals.Services[0].Name
}

func TestOpen_TempDirPerStore(t *testing.T) {
	remote := newRemote(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	open := func() *store.GitHubStore {
		st, err := store.Open(
			context.Background(),
			store.CloneConfig{URL: remote.URL()},
			storetest.Identity,
		)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
	a, b := open(), open()
	dirA, dirB := store.CloneDir(a), store.CloneDir(b)
	// The store's own directory is under the process temp directory, not
	// the test's.
	tmp := filepath.Clean(os.TempDir()) //nolint:usetesting // the directory os.MkdirTemp uses
	for _, dir := range []string{dirA, dirB} {
		if filepath.Dir(dir) != tmp || !strings.HasPrefix(filepath.Base(dir), "badil-gitops-") {
			t.Errorf("clone dir = %s, want a badil-gitops-* directory under %s", dir, tmp)
		}
	}
	if dirA == dirB {
		t.Fatalf("both stores clone into %s", dirA)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dirA); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s after Close = %v, want it removed", dirA, err)
	}
	if _, err := os.Stat(dirB); err != nil {
		t.Errorf("stat %s = %v, want the other store's clone kept", dirB, err)
	}
	if _, err := b.ListPreviews(context.Background()); err != nil {
		t.Errorf("ListPreviews on the other store: %v", err)
	}
}

func TestOpen_CloneDirClonedAgain(t *testing.T) {
	remote := newRemote(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	dir := filepath.Join(t.TempDir(), "gitops")
	open := func() *store.GitHubStore {
		st, err := store.Open(
			context.Background(),
			store.CloneConfig{URL: remote.URL(), Dir: dir},
			storetest.Identity,
		)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return st
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "stale")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := open()
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the directory removed before the clone", stale, err)
	}
	if got := serviceName(t, first); got != "first" {
		t.Errorf("service = %q, want first", got)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stat %s after Close = %v, want the given directory kept", dir, err)
	}

	remote.Set("previews/a/values.yaml", previewFile("second"))
	if got := serviceName(t, open()); got != "second" {
		t.Errorf("service after a new start = %q, want second, cloned again", got)
	}
}

func TestFreshness_TTL(t *testing.T) {
	st, remote, clock, m := clockedStore(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	// The clone is fresh until the TTL; the first check past it finds the
	// head unchanged and fetches nothing.
	serviceName(t, st)
	assertChecks(t, m, 0, 0)
	clock.advance(checkTTL)
	if got := serviceName(t, st); got != "first" {
		t.Fatalf("service = %q, want first", got)
	}
	assertChecks(t, m, 1, 0)

	remote.Set("previews/a/values.yaml", previewFile("second"))
	clock.advance(checkTTL - time.Millisecond)
	if got := serviceName(t, st); got != "first" {
		t.Errorf("service within the TTL = %q, want first, with no check", got)
	}
	assertChecks(t, m, 1, 0)

	clock.advance(time.Millisecond)
	if got := serviceName(t, st); got != "second" {
		t.Errorf("service past the TTL = %q, want second", got)
	}
	assertChecks(t, m, 2, 1)
	serviceName(t, st)
	assertChecks(t, m, 2, 1)
}

func TestFreshness_ConcurrentReadsShareOneCheck(t *testing.T) {
	st, remote, clock, m := clockedStore(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	remote.Set("previews/a/values.yaml", previewFile("second"))
	clock.advance(checkTTL)

	const readers = 16
	start := make(chan struct{})
	names := make([]string, readers)
	errs := make([]error, readers)
	var wg sync.WaitGroup
	for i := range readers {
		wg.Go(func() {
			<-start
			vals, _, err := st.GetPreview(context.Background(), "a")
			errs[i] = err
			if err == nil {
				names[i] = vals.Services[0].Name
			}
		})
	}
	close(start)
	wg.Wait()
	for i := range readers {
		if errs[i] != nil || names[i] != "second" {
			t.Errorf("reader %d = %q, %v; want second", i, names[i], errs[i])
		}
	}
	assertChecks(t, m, 1, 1)
}

// TestFreshness_ReadsDuringFetches reads while the remote moves and the
// store is told to check it, for the race detector.
func TestFreshness_ReadsDuringFetches(t *testing.T) {
	st, remote := readStore(t, map[string]string{"previews/a/values.yaml": previewFile("v0")})
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 1; i <= 20; i++ {
			remote.Set("previews/a/values.yaml", previewFile(fmt.Sprintf("v%d", i)))
			st.Invalidate()
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if _, err := st.ListPreviews(ctx); err != nil {
					t.Errorf("ListPreviews: %v", err)
				}
				if _, _, err := st.GetPreview(ctx, "a"); err != nil {
					t.Errorf("GetPreview: %v", err)
				}
			}
		})
	}
	wg.Wait()
	st.Invalidate()
	if got := serviceName(t, st); got != "v20" {
		t.Errorf("service = %q, want v20, the last commit", got)
	}
}

func TestFreshness_InvalidateChecksAtOnce(t *testing.T) {
	st, remote, _, m := clockedStore(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	serviceName(t, st)
	remote.Set("previews/a/values.yaml", previewFile("second"))
	st.Invalidate()
	if got := serviceName(t, st); got != "second" {
		t.Errorf("service after Invalidate = %q, want second", got)
	}
	assertChecks(t, m, 1, 1)
}

// TestFreshness_ApplyReadsBackAtOnce pushes a commit, then reads the pushed
// file back within the TTL, with no remote check but Apply's own fetch.
func TestFreshness_ApplyReadsBackAtOnce(t *testing.T) {
	f := newGitopsFake(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	m := metrics.New()
	st := f.open(t, store.WithMetrics(m))
	clock := &fakeClock{t: time.Now()}
	store.SetNow(st, clock.now)

	_, err := apply(st, lifecycle.OpDeploy, func(tree lifecycle.Tree) (lifecycle.Summary, error) {
		vals, getErr := tree.GetPreview("a")
		if getErr != nil {
			return lifecycle.Summary{}, getErr
		}
		vals.Services[0].Name = "second"
		return lifecycle.Summary{Subject: "rename"}, tree.PutPreview("a", *vals)
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, sha, err := st.GetPreview(context.Background(), "a")
	if err != nil {
		t.Fatalf("GetPreview: %v", err)
	}
	written := f.remote.Files()["previews/a/values.yaml"]
	if got.Services[0].Name != "second" || sha != blobSHA(written) {
		t.Errorf("read back %+v at %s, want the pushed file at %s", got, sha, blobSHA(written))
	}
	assertChecks(t, m, 0, 1)
}

func TestFreshness_LsRemoteErrorFailsRead(t *testing.T) {
	st, remote, clock, m := clockedStore(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	serviceName(t, st)
	if err := os.RemoveAll(remote.Dir()); err != nil {
		t.Fatal(err)
	}
	clock.advance(checkTTL)
	for range 2 {
		vals, sha, err := st.GetPreview(context.Background(), "a")
		if err == nil || vals != nil || sha != "" {
			t.Errorf("GetPreview = %+v, %q, %v; want an error and no stale content", vals, sha, err)
		}
	}
	if _, err := st.ListPreviews(context.Background()); err == nil {
		t.Error("ListPreviews: want an error")
	}
	if got := opCount(t, m, "ls_remote", "error"); got != 3 {
		t.Errorf("failed ls-remotes = %d, want 3: a failed check is not trusted", got)
	}
}

func TestOpen_RemoteRefusesCredentials(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			t.Cleanup(srv.Close)
			creds := func(context.Context) (string, string, error) { return "x-access-token", "revoked", nil }
			_, err := store.Open(
				context.Background(),
				store.CloneConfig{URL: srv.URL + "/acme/gitops.git", Credentials: creds},
				storetest.Identity,
			)
			if !errors.Is(err, lifecycle.ErrUnauthorized) {
				t.Errorf("Open = %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestOpen_RepoURLErrors(t *testing.T) {
	remote := newRemote(t, nil)
	cases := []struct {
		name    string
		clone   store.CloneConfig
		opts    []store.Option
		wantErr string
	}{
		{name: "no clone URL", wantErr: "gitops repo URL is required"},
		{
			name:    "one segment",
			clone:   store.CloneConfig{URL: remote.URL()},
			opts:    []store.Option{store.WithRepoURL("https://github.com/gitops.git")},
			wantErr: `gitops repo URL: repository path "/gitops.git" needs at least two segments`,
		},
		{
			name:    "no path",
			clone:   store.CloneConfig{URL: "https://github.com"},
			wantErr: `gitops repo URL: repository path "" needs at least two segments`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(
				context.Background(),
				tc.clone,
				storetest.Identity,
				tc.opts...)
			if err == nil {
				_ = st.Close()
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("Open = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// authRemote serves remote over HTTP, refusing with 401 every request whose
// basic-auth pair is not user and password, answering every push it lets
// through with 403, and records each request's pair.
type authRemote struct {
	srv   *httptest.Server
	mu    sync.Mutex
	pairs []string
}

func newAuthRemote(t *testing.T, remote *storetest.Remote, user, password string) *authRemote {
	t.Helper()
	a := &authRemote{}
	handler := remote.HTTPHandler(http.StatusForbidden)
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		a.mu.Lock()
		a.pairs = append(a.pairs, r.URL.Query().Get("service")+" "+u+":"+p)
		a.mu.Unlock()
		if u != user || p != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="gitops"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

// requests returns the recorded requests, each "<service> <user>:<password>",
// the service empty but for info/refs.
func (a *authRemote) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.pairs)
}

// assertAuth checks every request carried the basic-auth pair want, and that
// they include the clone, the ls-remotes and fetches, and a push.
func (a *authRemote) assertAuth(t *testing.T, want string) {
	t.Helper()
	services := map[string]int{}
	for _, req := range a.requests() {
		service, pair, _ := strings.Cut(req, " ")
		services[service]++
		if pair != want {
			t.Errorf("request %q, want basic auth %s", req, want)
		}
	}
	// The clone, two ls-remotes, a fetch and Apply's fetch advertise
	// git-upload-pack; the push advertises git-receive-pack.
	if services["git-upload-pack"] < 4 || services["git-receive-pack"] != 1 {
		t.Errorf("info/refs requests by service = %v, want the clone, ls-remotes, fetch and a push", services)
	}
}

// TestCredentials_BasicAuth checks that the clone, every ls-remote and fetch,
// and a push send a static provider's username and token.
func TestCredentials_BasicAuth(t *testing.T) {
	for _, user := range []string{"x-access-token", "deploy-user"} {
		t.Run(user, func(t *testing.T) {
			const token = "glpat-static-token"
			remote := newRemote(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
			a := newAuthRemote(t, remote, user, token)
			creds := func(context.Context) (string, string, error) { return user, token, nil }
			st, err := store.Open(
				context.Background(),
				store.CloneConfig{
					URL:         a.srv.URL + "/acme/gitops.git",
					Credentials: creds,
				},
				storetest.Identity,
			)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			store.SetSleep(st, func(context.Context, time.Duration) error { return nil })

			st.Invalidate()
			serviceName(t, st)
			remote.Set("previews/a/values.yaml", previewFile("second"))
			st.Invalidate()
			if got := serviceName(t, st); got != "second" {
				t.Fatalf("service = %q, want the fetched second", got)
			}
			_, err = st.Apply(context.Background(), lifecycle.Op{
				Kind: lifecycle.OpDeploy,
				Change: func(tree lifecycle.Tree) (lifecycle.Summary, error) {
					tree.DeletePreview("a")
					return lifecycle.Summary{Subject: "chore(preview): delete a"}, nil
				},
			})
			if !errors.Is(err, lifecycle.ErrUnauthorized) {
				t.Fatalf("Apply = %v, want the push refused with 403", err)
			}

			a.assertAuth(t, user+":"+token)
		})
	}
}

// TestCredentials_WrongToken checks that a clone refused for a wrong token
// fails with ErrUnauthorized, without the token in the error.
func TestCredentials_WrongToken(t *testing.T) {
	const token = "glpat-wrong-token"
	remote := newRemote(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	a := newAuthRemote(t, remote, "x-access-token", "glpat-right-token")
	creds := func(context.Context) (string, string, error) { return "x-access-token", token, nil }
	st, err := store.Open(
		context.Background(),
		store.CloneConfig{
			URL:         a.srv.URL + "/acme/gitops.git",
			Credentials: creds,
		},
		storetest.Identity,
	)
	if err == nil {
		_ = st.Close()
	}
	if !errors.Is(err, lifecycle.ErrUnauthorized) || strings.Contains(err.Error(), token) {
		t.Fatalf("Open = %v, want ErrUnauthorized without the token", err)
	}
	if reqs := a.requests(); len(reqs) == 0 || reqs[0] != "git-upload-pack x-access-token:"+token {
		t.Errorf("requests = %v, want the clone with the wrong token", reqs)
	}
}

func TestOpen_CredentialProviderError(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	creds := func(context.Context) (string, string, error) { return "", "", errors.New("token mint failed") }
	_, err := store.Open(
		context.Background(),
		store.CloneConfig{URL: srv.URL + "/acme/gitops.git", Credentials: creds},
		storetest.Identity,
	)
	if !errors.Is(err, lifecycle.ErrUnauthorized) || !strings.Contains(err.Error(), "token mint failed") {
		t.Errorf("Open = %v, want ErrUnauthorized with the provider's error", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("remote requests = %d, want none after a provider error", n)
	}
}

// TestCredentials_BeforeEachGitCall checks that the provider is called
// before the clone, each ls-remote and each fetch, and that its error fails a
// read with ErrUnauthorized.
func TestCredentials_BeforeEachGitCall(t *testing.T) {
	remote := newRemote(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	var calls atomic.Int32
	var failing atomic.Bool
	creds := func(context.Context) (string, string, error) {
		calls.Add(1)
		if failing.Load() {
			return "", "", errors.New("token mint failed")
		}
		return "x-access-token", "token", nil
	}
	st, err := store.Open(
		context.Background(),
		store.CloneConfig{URL: remote.URL(), Credentials: creds},
		storetest.Identity,
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if n := calls.Load(); n != 1 {
		t.Errorf("provider calls after the clone = %d, want 1", n)
	}

	st.Invalidate()
	serviceName(t, st)
	if n := calls.Load(); n != 2 {
		t.Errorf("provider calls after an ls-remote = %d, want 2", n)
	}
	remote.Set("previews/a/values.yaml", previewFile("second"))
	st.Invalidate()
	serviceName(t, st)
	if n := calls.Load(); n != 4 {
		t.Errorf("provider calls after an ls-remote and a fetch = %d, want 4", n)
	}

	failing.Store(true)
	st.Invalidate()
	if _, _, readErr := st.GetPreview(context.Background(), "a"); !errors.Is(readErr, lifecycle.ErrUnauthorized) {
		t.Errorf("GetPreview = %v, want ErrUnauthorized", readErr)
	}
}

// notifyCtx is a background context that closes waiting the first time its
// Done is called: when a read starts waiting on a check in flight.
type notifyCtx struct {
	context.Context

	once    sync.Once
	waiting chan struct{}
}

func (c *notifyCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// TestFreshness_CancelledRefreshRetriedByWaiter cancels the read that started
// a shared check while its provider refreshes the token, as ghinstallation
// wraps the context error, and checks a waiting read retries and succeeds.
func TestFreshness_CancelledRefreshRetriedByWaiter(t *testing.T) {
	remote := newRemote(t, map[string]string{"previews/a/values.yaml": previewFile("first")})
	var calls atomic.Int32
	refreshing := make(chan struct{})
	creds := func(ctx context.Context) (string, string, error) {
		if calls.Add(1) == 2 {
			close(refreshing)
			<-ctx.Done()
			return "", "", fmt.Errorf("could not refresh installation id 1's token: %w", ctx.Err())
		}
		return "x-access-token", "token", nil
	}
	st, err := store.Open(
		context.Background(),
		store.CloneConfig{URL: remote.URL(), Credentials: creds},
		storetest.Identity,
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	st.Invalidate()

	starterCtx, cancel := context.WithCancel(context.Background())
	starterErr := make(chan error, 1)
	go func() {
		_, _, readErr := st.GetPreview(starterCtx, "a")
		starterErr <- readErr
	}()
	<-refreshing

	waiterCtx := &notifyCtx{Context: context.Background(), waiting: make(chan struct{})}
	waiterErr := make(chan error, 1)
	go func() {
		_, _, readErr := st.GetPreview(waiterCtx, "a")
		waiterErr <- readErr
	}()
	<-waiterCtx.waiting
	cancel()

	if gotStarterErr := <-starterErr; !errors.Is(gotStarterErr, context.Canceled) {
		t.Errorf("starter GetPreview = %v, want context.Canceled", gotStarterErr)
	}
	if gotWaiterErr := <-waiterErr; gotWaiterErr != nil {
		t.Errorf("waiter GetPreview = %v, want the check retried", gotWaiterErr)
	}
}

// TestFetch_RepacksPastPackLimit fetches more times than the pack limit and
// checks the clone is repacked and every read still finds its objects.
func TestFetch_RepacksPastPackLimit(t *testing.T) {
	st, remote := readStore(t, map[string]string{"previews/a/values.yaml": previewFile("v0")})
	packDir := filepath.Join(store.CloneDir(st), "objects", "pack")
	for i := 1; i <= store.MaxPacks+10; i++ {
		want := fmt.Sprintf("v%d", i)
		remote.Set("previews/a/values.yaml", previewFile(want))
		st.Invalidate()
		if got := serviceName(t, st); got != want {
			t.Fatalf("service after fetch %d = %q, want %s", i, got, want)
		}
		packs, err := filepath.Glob(filepath.Join(packDir, "*.pack"))
		if err != nil {
			t.Fatal(err)
		}
		if len(packs) > store.MaxPacks {
			t.Fatalf("packs after fetch %d = %d, want at most %d", i, len(packs), store.MaxPacks)
		}
	}
	if _, err := st.ListPreviews(context.Background()); err != nil {
		t.Errorf("ListPreviews: %v", err)
	}
}

// TestGetPreview_SHAIsGitHashObject checks the SHA against git hash-object's
// definition: the SHA-1 of "blob <size>\x00" and the content.
func TestGetPreview_SHAIsGitHashObject(t *testing.T) {
	content := previewFile("backend-1")
	st, _ := readStore(t, map[string]string{"previews/a/values.yaml": content})
	_, sha, err := st.GetPreview(context.Background(), "a")
	if err != nil {
		t.Fatalf("GetPreview: %v", err)
	}
	sum := sha1.Sum(fmt.Appendf(nil, "blob %d\x00%s", len(content), content))
	if want := hex.EncodeToString(sum[:]); sha != want {
		t.Errorf("sha = %s, want git hash-object's %s", sha, want)
	}
}
