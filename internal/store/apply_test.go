package store_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

// applyFiles are the remote files of the Apply tests.
var applyFiles = map[string]string{ //nolint:gochecknoglobals // test fixture
	"badil.yaml":                   "services: {}\n",
	"previews/a/values.yaml":       previewFile("a"),
	"previews/b/values.yaml":       previewFile("b"),
	"previews/b/README.md":         "kept\n",
	"previews/zz-last/values.yaml": previewFile("zz"),
}

// rename returns a Change that renames the only service of the preview slug
// to name, recorded as a sync, with subject.
func rename(slug, name, subject string) func(*store.Tree) (store.Summary, error) {
	return func(tree *store.Tree) (store.Summary, error) {
		vals, err := tree.GetPreview(slug)
		if err != nil {
			return store.Summary{}, err
		}
		vals.Services[0].Name = name
		return store.Summary{Subject: subject}, store.PutPreview(tree, slug, *vals)
	}
}

// TestApply_OneCommitOnFetchedHead checks that Apply commits on the remote
// head it fetched, even within the read TTL, exactly the files the Change
// put and deleted, with the subject, the body lines by slug and the store's
// identity as author and committer, unsigned.
func TestApply_OneCommitOnFetchedHead(t *testing.T) {
	f := newGitopsFake(t, applyFiles)
	c := f.open(t)
	// The remote moves after the clone: Apply fetches it first.
	f.remote.Set("previews/b/values.yaml", previewFile("b2"))
	parent := f.remote.Head()

	runs := 0
	out, err := apply(c, store.OpSweep, func(tree *store.Tree) (store.Summary, error) {
		runs++
		vals, getErr := tree.GetPreview("b")
		if getErr != nil || vals.Services[0].Name != "b2" {
			t.Errorf("GetPreview(b) = %+v, %v; want the fetched b2", vals, getErr)
		}
		tree.DeletePreview("zz-last")
		if putErr := store.PutPreview(tree, "new", preview.PreviewValues{
			Services: []preview.ServiceEntry{{Name: "new"}},
		}); putErr != nil {
			return store.Summary{}, putErr
		}
		_, syncErr := tree.Sync("a", testCatalog("a"))
		return store.Summary{Subject: "chore(preview): sweep: delete 1, unpin 0, sync 1"}, syncErr
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	commits := f.remote.Commits()
	head := commits[0]
	if runs != 1 || f.pushCount() != 1 || out.NoOp || out.Commit != head.Hash ||
		out.URL != "https://github.com/acme/gitops/commit/"+head.Hash ||
		out.Subject != "chore(preview): sweep: delete 1, unpin 0, sync 1" {
		t.Fatalf("runs, pushes = %d, %d; result %+v; want one run and push of the head %s",
			runs, f.pushCount(), out, head.Hash)
	}
	if !slices.Equal(head.Parents, []string{parent}) {
		t.Errorf("parents = %v, want the fetched head %s", head.Parents, parent)
	}
	if head.Subject() != out.Subject || !slices.Equal(head.Body(), []string{"a: sync", "zz-last: delete"}) {
		t.Errorf("message = %q, want the subject then the body lines by slug", head.Message)
	}
	want := maps.Clone(commits[1].Tree)
	delete(want, "previews/zz-last/values.yaml")
	want["previews/new/values.yaml"] = head.Tree["previews/new/values.yaml"]
	want["previews/a/values.yaml"] = head.Tree["previews/a/values.yaml"]
	if !maps.Equal(head.Tree, want) || !strings.Contains(want["previews/new/values.yaml"], "name: new") ||
		!strings.Contains(want["previews/a/values.yaml"], "apiVersion: badil.dev/v1beta1") {
		t.Errorf("tree = %v, want the parent's with exactly the changes", head.Tree)
	}
	for _, sig := range []struct {
		name, email string
	}{{head.Author.Name, head.Author.Email}, {head.Committer.Name, head.Committer.Email}} {
		if sig.name != storetest.Identity.Name || sig.email != storetest.Identity.Email {
			t.Errorf("signature = %s <%s>, want %s <%s>",
				sig.name, sig.email, storetest.Identity.Name, storetest.Identity.Email)
		}
	}
	if head.Signed {
		t.Error("commit is signed, want unsigned")
	}
	got, err := c.ListPreviews(context.Background())
	if want := []string{"a", "b", "new"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("ListPreviews = %v, %v; want %v", got, err, want)
	}
}

// TestApply_ConflictRerunsOnNewTree loses the first push to a concurrent
// commit and checks the Change runs again on the tree that commit made, the
// push is not forced and the conflict is counted with the operation kind.
func TestApply_ConflictRerunsOnNewTree(t *testing.T) {
	f := newGitopsFake(t, applyFiles)
	f.path, f.next, f.conflicts = "previews/b/values.yaml", []string{previewFile("concurrent")}, 1
	m := metrics.New()
	c := f.open(t, store.WithMetrics(m))

	var seen []string
	out, err := apply(c, store.OpDeploy, func(tree *store.Tree) (store.Summary, error) {
		vals, getErr := tree.GetPreview("b")
		if getErr != nil {
			return store.Summary{}, getErr
		}
		seen = append(seen, vals.Services[0].Name)
		return rename("a", "renamed", "rename a")(tree)
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := []string{"b", "concurrent"}; !slices.Equal(seen, want) {
		t.Errorf("Change saw b as %v, want %v", seen, want)
	}
	commits := f.remote.Commits()
	if f.pushCount() != 2 || out.Commit != commits[0].Hash ||
		!strings.Contains(commits[0].Tree["previews/b/values.yaml"], "concurrent") ||
		!slices.Equal(commits[0].Parents, []string{commits[1].Hash}) {
		t.Errorf("pushes = %d, head %s; want 2, the head on the concurrent commit kept", f.pushCount(), commits[0].Hash)
	}
	assertConflicts(t, m, map[string]float64{store.OpDeploy: 1})
}

// assertConflicts fails t unless m counts want push conflicts by op.
func assertConflicts(t *testing.T, m *metrics.Metrics, want map[string]float64) {
	t.Helper()
	families, err := m.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	got := map[string]float64{}
	for _, mf := range families {
		if mf.GetName() != "badil_gitops_push_conflicts_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			labels := metric.GetLabel()
			if len(labels) != 1 || labels[0].GetName() != "op" {
				t.Errorf("labels = %v, want only op", labels)
				continue
			}
			got[labels[0].GetValue()] = metric.GetCounter().GetValue()
		}
	}
	if !maps.Equal(got, want) {
		t.Errorf("push conflicts = %v, want %v", got, want)
	}
}

// TestApply_PushFailureNotRetried fails the push while the remote head stays
// where it was fetched: the error is returned after one attempt.
func TestApply_PushFailureNotRetried(t *testing.T) {
	f := newGitopsFake(t, applyFiles)
	m := metrics.New()
	c := f.open(t, store.WithMetrics(m))
	head := f.remote.Head()
	pushes := 0
	f.remote.OnPush(func() error {
		pushes++
		return errors.New("remote hook declined")
	})

	runs := 0
	_, err := apply(c, store.OpSync, func(tree *store.Tree) (store.Summary, error) {
		runs++
		return rename("a", "x", "rename a")(tree)
	})
	if err == nil || !strings.Contains(err.Error(), "remote hook declined") {
		t.Fatalf("Apply = %v, want the push error", err)
	}
	if runs != 1 || pushes != 1 || f.remote.Head() != head {
		t.Errorf("runs, pushes = %d, %d, head moved %v; want one attempt, nothing pushed",
			runs, pushes, f.remote.Head() != head)
	}
	assertConflicts(t, m, map[string]float64{})
	if got := opCount(t, m, store.OpSync, "error"); got != 1 {
		t.Errorf("failed sync operations = %d, want 1", got)
	}
}

// TestApply_ConflictsExhausted loses every push and checks Apply gives up
// after MaxAttempts, waiting the backoff between them.
func TestApply_ConflictsExhausted(t *testing.T) {
	f := newGitopsFake(t, applyFiles)
	f.conflicts = store.MaxAttempts + 1
	m := metrics.New()
	c := f.open(t, store.WithMetrics(m))
	var waits []time.Duration
	store.SetSleep(c, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	})

	runs := 0
	_, err := apply(c, store.OpTeardown, func(tree *store.Tree) (store.Summary, error) {
		runs++
		return rename("a", "x", "rename a")(tree)
	})
	if err == nil || !strings.Contains(err.Error(), "5 attempts conflicted") {
		t.Fatalf("Apply = %v, want the attempts named", err)
	}
	if runs != store.MaxAttempts || f.pushCount() != store.MaxAttempts {
		t.Errorf("runs, pushes = %d, %d; want %d each", runs, f.pushCount(), store.MaxAttempts)
	}
	assertConflicts(t, m, map[string]float64{store.OpTeardown: store.MaxAttempts})
	assertWaits(t, waits)
}

// assertWaits checks waits are the backoff before attempts 2 to 5: 100, 200,
// 400 and 800 ms, each within ±20 %.
func assertWaits(t *testing.T, waits []time.Duration) {
	t.Helper()
	base := []int{100, 200, 400, 800}
	if len(waits) != len(base) {
		t.Fatalf("waits = %v, want %d", waits, len(base))
	}
	for i, w := range waits {
		d := time.Duration(base[i]) * time.Millisecond
		if w < d*8/10 || w > d*12/10 {
			t.Errorf("wait %d = %v, want %v ±20 %%", i+1, w, d)
		}
	}
}

func TestBackoff(t *testing.T) {
	for range 100 {
		var waits []time.Duration
		for n := 1; n < store.MaxAttempts; n++ {
			waits = append(waits, store.Backoff(n))
		}
		assertWaits(t, waits)
		if d := store.Backoff(10); d != 2*time.Second {
			t.Fatalf("Backoff(10) = %v, want the 2s cap", d)
		}
	}
}

// TestApply_NoOp checks a Change that stages nothing, or only what is
// already there, commits nothing and returns the remote head with its
// subject.
func TestApply_NoOp(t *testing.T) {
	cases := []struct {
		name   string
		change func(*store.Tree) (store.Summary, error)
	}{
		{name: "nothing staged", change: func(*store.Tree) (store.Summary, error) {
			return store.Summary{Subject: "nothing"}, nil
		}},
		{name: "same content", change: rename("a", "a", "same")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitopsFake(t, map[string]string{"previews/a/values.yaml": "services:\n  - name: a\n"})
			c := f.open(t)
			// The file as the store writes it.
			if _, err := apply(c, store.OpDeploy, rename("a", "a", "normalise")); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			head := f.remote.Head()
			f.pushes = 0
			out, err := apply(c, store.OpDeploy, tc.change)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !out.NoOp || out.Commit != head || out.Subject != "normalise" ||
				f.pushCount() != 0 || f.remote.Head() != head {
				t.Errorf("result = %+v, pushes = %d; want a no-op on %s", out, f.pushCount(), head)
			}
		})
	}
}

func TestApply_ChangeErrorCommitsNothing(t *testing.T) {
	f := newGitopsFake(t, applyFiles)
	c := f.open(t)
	head := f.remote.Head()
	boom := errors.New("boom")
	_, err := apply(c, store.OpDeploy, func(tree *store.Tree) (store.Summary, error) {
		_, _ = rename("a", "x", "rename")(tree)
		return store.Summary{}, boom
	})
	if !errors.Is(err, boom) || f.pushCount() != 0 || f.remote.Head() != head {
		t.Errorf("Apply = %v, pushes = %d; want boom with nothing pushed", err, f.pushCount())
	}
}

// TestApply_PushUnauthorized pushes to an HTTP remote that serves fetches
// and refuses pushes with 403.
func TestApply_PushUnauthorized(t *testing.T) {
	remote := newRemote(t, applyFiles)
	var pushes atomic.Int32
	handler := remote.HTTPHandler(http.StatusForbidden)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("service") == transport.ReceivePackServiceName {
			pushes.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	m := metrics.New()
	c := openStore(t, srv.URL+"/acme/gitops.git", store.WithMetrics(m))

	runs := 0
	_, err := apply(c, store.OpDeploy, func(tree *store.Tree) (store.Summary, error) {
		runs++
		return rename("a", "x", "rename a")(tree)
	})
	if !errors.Is(err, store.ErrUnauthorized) ||
		!strings.Contains(err.Error(), transport.ErrAuthorizationFailed.Error()) {
		t.Fatalf("Apply = %v, want ErrUnauthorized with the 403's error", err)
	}
	if runs != 1 || pushes.Load() != 1 {
		t.Errorf("runs, pushes = %d, %d; want one attempt", runs, pushes.Load())
	}
	assertConflicts(t, m, map[string]float64{})
}

func TestOpen_RejectsEmptyIdentity(t *testing.T) {
	remote := newRemote(t, applyFiles)
	for _, id := range []store.CommitIdentity{
		{Email: storetest.Identity.Email},
		{Name: storetest.Identity.Name},
		{},
	} {
		st, err := store.Open(
			context.Background(),
			store.CloneConfig{URL: remote.URL()},
			id,
			storetest.GitHubForge.ParsePRURL,
		)
		if err == nil {
			_ = st.Close()
			t.Errorf("Open with identity %+v = nil error, want one", id)
		}
	}
}

func TestOpen_RejectsNilPRURLParser(t *testing.T) {
	remote := newRemote(t, applyFiles)
	st, err := store.Open(context.Background(), store.CloneConfig{URL: remote.URL()}, storetest.Identity, nil)
	if err == nil {
		_ = st.Close()
		t.Fatal("Open with a nil pull request URL parser = nil error, want one")
	}
}

// TestApply_Serialised runs operations concurrently and checks each one's
// commit lands: none conflicts with another of the same store.
func TestApply_Serialised(t *testing.T) {
	f := newGitopsFake(t, applyFiles)
	m := metrics.New()
	c := f.open(t, store.WithMetrics(m))
	slugs := []string{"a", "b", "zz-last"}
	errs := make(chan error, len(slugs))
	for _, slug := range slugs {
		go func() {
			_, err := apply(c, store.OpSync, rename(slug, "renamed-"+slug, "rename "+slug))
			errs <- err
		}()
	}
	for range slugs {
		if err := <-errs; err != nil {
			t.Errorf("Apply: %v", err)
		}
	}
	files := f.remote.Files()
	for _, slug := range slugs {
		if !strings.Contains(files["previews/"+slug+"/values.yaml"], "renamed-"+slug) {
			t.Errorf("preview %s = %q, want renamed", slug, files["previews/"+slug+"/values.yaml"])
		}
	}
	if n := len(f.remote.Commits()); n != len(slugs)+1 {
		t.Errorf("commits = %d, want %d", n, len(slugs)+1)
	}
	assertConflicts(t, m, map[string]float64{})
	if got := opCount(t, m, store.OpSync, "ok"); got != uint64(len(slugs)) {
		t.Errorf("sync operations = %d, want %d", got, len(slugs))
	}
}

// TestApply_WaitBoundedByContext checks that an Apply waiting for the one
// before it gives up when its ctx is done, runs nothing and is observed as a
// failed operation, and that the store accepts the next Apply once the first
// one ends.
func TestApply_WaitBoundedByContext(t *testing.T) {
	f := newGitopsFake(t, applyFiles)
	m := metrics.New()
	c := f.open(t, store.WithMetrics(m))
	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := apply(c, store.OpSweep, func(tree *store.Tree) (store.Summary, error) {
			close(started)
			<-release
			return rename("a", "renamed-a", "rename a")(tree)
		})
		first <- err
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ran := false
	_, err := c.Apply(ctx, store.Op{Kind: store.OpDeploy, Change: func(*store.Tree) (store.Summary, error) {
		ran = true
		return store.Summary{}, nil
	}})
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("waiting Apply = %v, ran %v; want context.DeadlineExceeded without running", err, ran)
	}
	if got := opCount(t, m, store.OpDeploy, "error"); got != 1 {
		t.Errorf("failed deploy operations = %d, want 1", got)
	}

	close(release)
	if err = <-first; err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if _, err = apply(c, store.OpSync, rename("b", "renamed-b", "rename b")); err != nil {
		t.Fatalf("Apply after the first: %v", err)
	}
}
