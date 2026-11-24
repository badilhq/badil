package status_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/badilhq/badil/internal/status"
	"github.com/badilhq/badil/internal/status/kube"
)

const namespace = "badil"

// t0 is the time of the tests' first event.
var t0 = time.Date(2026, 11, 19, 21, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // test fixture

// lister lists slugs, after calling during when set.
type lister struct {
	mu     sync.Mutex
	slugs  []string
	err    error
	during func()
}

func (l *lister) ListPreviewFiles(context.Context) ([]string, error) {
	l.mu.Lock()
	during, slugs, err := l.during, slices.Clone(l.slugs), l.err
	l.mu.Unlock()
	if during != nil {
		during()
	}
	return slugs, err
}

func (l *lister) set(slugs ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.slugs = slugs
}

// entry is a status entry at t0 plus d.
func entry(revision, s string, d time.Duration) status.Entry {
	return status.Entry{Revision: revision, Status: s, At: t0.Add(d)}
}

// statusConfigMap is the status ConfigMap of slug holding services.
func statusConfigMap(t *testing.T, slug string, services map[string]status.Entry) *corev1.ConfigMap {
	t.Helper()
	data := map[string]string{}
	for name, e := range services {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data[name] = string(raw)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: kube.ConfigMapName(slug), Namespace: namespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "badil", "badil.dev/preview": slug},
		},
		Data: data,
	}
}

// startStore starts a store persisted in client, flushing every interval,
// over l, and closes it at the end of the test.
func startStore(t *testing.T, client *fake.Clientset, interval time.Duration, l status.Lister) *status.Store {
	t.Helper()
	s := kube.New(client, namespace)
	status.SetInterval(s, interval)
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.Start(context.Background(), l)
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

// configMapData returns the decoded data of slug's ConfigMap, and false when
// it does not exist.
func configMapData(t *testing.T, client *fake.Clientset, slug string) (map[string]status.Entry, bool) {
	t.Helper()
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(
		context.Background(), kube.ConfigMapName(slug), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if cm.Labels["app.kubernetes.io/managed-by"] != "badil" || cm.Labels["badil.dev/preview"] != slug {
		t.Errorf("configmap %s labels = %v", cm.Name, cm.Labels)
	}
	out := map[string]status.Entry{}
	for name, raw := range cm.Data {
		var e status.Entry
		if err = json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatalf("decode %s/%s: %v", cm.Name, name, err)
		}
		out[name] = e
	}
	return out, true
}

// eventually fails t unless cond holds within timeout.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", timeout, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func equalEntries(a, b map[string]status.Entry) bool {
	return maps.EqualFunc(a, b, func(v, w status.Entry) bool {
		return v.Revision == w.Revision && v.Status == w.Status && v.At.Equal(w.At)
	})
}

func TestRecord_StaleAndFirstReady(t *testing.T) {
	s := status.NewMemory()
	steps := []struct {
		e              status.Entry
		recorded, more bool
	}{
		{entry("aaa", status.Progressing, 0), true, false},
		{entry("aaa", status.Ready, time.Minute), true, true},
		// The same time again is not older: recorded.
		{entry("aaa", status.Ready, time.Minute), true, false},
		{entry("aaa", status.Degraded, 30*time.Second), false, false},
		{entry("aaa", status.Degraded, 2*time.Minute), true, false},
		{entry("aaa", status.Ready, 3*time.Minute), true, false},
		{entry("bbb", status.Ready, 4*time.Minute), true, true},
	}
	for i, step := range steps {
		recorded, first := s.Record("feature-x", "backend-1", step.e)
		if recorded != step.recorded || first != step.more {
			t.Errorf("step %d: Record = %v, %v; want %v, %v", i, recorded, first, step.recorded, step.more)
		}
	}
	got := s.Preview("feature-x")
	if want := map[string]status.Entry{
		"backend-1": entry("bbb", status.Ready, 4*time.Minute),
	}; !equalEntries(
		got,
		want,
	) {
		t.Errorf("Preview = %+v, want %+v", got, want)
	}
	if other := s.Preview("other"); len(other) != 0 {
		t.Errorf("Preview(other) = %+v, want none", other)
	}
}

// TestPersist_WrittenWithinFlushInterval checks, with the real interval,
// that a recorded event reaches the preview's ConfigMap within 5 s, and that
// writes are coalesced: two events make one create.
func TestPersist_WrittenWithinFlushInterval(t *testing.T) {
	t.Parallel()
	if status.FlushInterval != 5*time.Second {
		t.Fatalf("FlushInterval = %s, want 5s", status.FlushInterval)
	}
	client := fake.NewClientset()
	s := startStore(t, client, status.FlushInterval, &lister{slugs: []string{"feature-x"}})
	s.Record("feature-x", "backend-1", entry("aaa", status.Progressing, 0))
	s.Record("feature-x", "postgres", entry("16.4.2", status.Ready, time.Second))
	want := map[string]status.Entry{
		"backend-1": entry("aaa", status.Progressing, 0),
		"postgres":  entry("16.4.2", status.Ready, time.Second),
	}
	eventually(t, status.FlushInterval+500*time.Millisecond, "configmap written", func() bool {
		got, ok := configMapData(t, client, "feature-x")
		return ok && equalEntries(got, want)
	})
	creates := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == "create" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("creates = %d, want 1", creates)
	}
}

// TestPersist_UpdateConflictReplacesFromMemory checks that a conflicting
// update reads the ConfigMap again and replaces its data with memory.
func TestPersist_UpdateConflictReplacesFromMemory(t *testing.T) {
	client := fake.NewClientset()
	conflicts := 1
	var mu sync.Mutex
	client.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		if conflicts == 0 {
			return false, nil, nil
		}
		conflicts--
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Resource: "configmaps"},
			"x",
			errors.New("changed"),
		)
	})
	s := startStore(t, client, 20*time.Millisecond, &lister{slugs: []string{"feature-x"}})
	// Another writer made the ConfigMap after the store started.
	if _, err := client.CoreV1().ConfigMaps(namespace).Create(context.Background(),
		statusConfigMap(t, "feature-x", map[string]status.Entry{"stale-service": entry("old", status.Degraded, 0)}),
		metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s.Record("feature-x", "backend-1", entry("aaa", status.Ready, 0))
	want := map[string]status.Entry{"backend-1": entry("aaa", status.Ready, 0)}
	eventually(t, 2*time.Second, "configmap replaced", func() bool {
		got, _ := configMapData(t, client, "feature-x")
		return equalEntries(got, want)
	})
	mu.Lock()
	defer mu.Unlock()
	if conflicts != 0 {
		t.Error("no update conflicted")
	}
}

// TestPersist_FailedWriteRetried checks that a failed write is logged and
// made again at the next flush.
func TestPersist_FailedWriteRetried(t *testing.T) {
	client := fake.NewClientset()
	failures := 2
	var mu sync.Mutex
	client.PrependReactor("create", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		if failures == 0 {
			return false, nil, nil
		}
		failures--
		return true, nil, apierrors.NewServiceUnavailable("down")
	})
	logs := captureLogs(t)
	s := startStore(t, client, 20*time.Millisecond, &lister{slugs: []string{"feature-x"}})
	s.Record("feature-x", "backend-1", entry("aaa", status.Ready, 0))
	eventually(t, 2*time.Second, "configmap written after failures", func() bool {
		_, ok := configMapData(t, client, "feature-x")
		return ok
	})
	if !strings.Contains(logs.String(), "writing status configmap failed") {
		t.Errorf("logs = %q, want the failed write", logs.String())
	}
}

// TestLoad_RebuildsFromLabelledConfigMaps checks that Load reads back the
// ConfigMaps badil manages that carry the preview label, and nothing else.
func TestLoad_RebuildsFromLabelledConfigMaps(t *testing.T) {
	other := statusConfigMap(t, "other", map[string]status.Entry{"x": entry("r", status.Ready, 0)})
	other.Labels = map[string]string{"badil.dev/preview": "other"}
	noPreview := statusConfigMap(t, "no-preview", map[string]status.Entry{"x": entry("r", status.Ready, 0)})
	delete(noPreview.Labels, "badil.dev/preview")
	elsewhere := statusConfigMap(t, "elsewhere", map[string]status.Entry{"x": entry("r", status.Ready, 0)})
	elsewhere.Namespace = "default"
	want := map[string]status.Entry{
		"backend-1": entry("aaa", status.Ready, 0),
		"postgres":  entry("16.4.2", status.Degraded, time.Second),
	}
	client := fake.NewClientset(statusConfigMap(t, "feature-x", want), other, noPreview, elsewhere)

	s := kube.New(client, namespace)
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Preview("feature-x"); !equalEntries(got, want) {
		t.Errorf("Preview(feature-x) = %+v, want %+v", got, want)
	}
	for _, slug := range []string{"other", "no-preview", "elsewhere"} {
		if got := s.Preview(slug); len(got) != 0 {
			t.Errorf("Preview(%s) = %+v, want none", slug, got)
		}
	}
	// A ready read back is not recorded in this process: the next ready at
	// that revision is the first.
	if recorded, first := s.Record(
		"feature-x",
		"backend-1",
		entry("aaa", status.Ready, time.Minute),
	); !recorded ||
		!first {
		t.Errorf("Record after Load = %v, %v; want recorded, first ready", recorded, first)
	}
}

func TestLoad_ListFailureIsAnError(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("rbac"))
	})
	if err := kube.New(client, namespace).Load(context.Background()); err == nil {
		t.Fatal("Load: want an error")
	}
}

// TestDelete_RemovesMemoryAndConfigMap checks that Delete forgets the
// preview at once and deletes its ConfigMap in the background.
func TestDelete_RemovesMemoryAndConfigMap(t *testing.T) {
	client := fake.NewClientset(
		statusConfigMap(t, "feature-x", map[string]status.Entry{"backend-1": entry("aaa", status.Ready, 0)}),
		statusConfigMap(t, "kept", map[string]status.Entry{"backend-1": entry("bbb", status.Ready, 0)}),
	)
	// A long interval: the delete does not wait for a flush.
	s := startStore(t, client, time.Hour, &lister{slugs: []string{"feature-x", "kept"}})
	s.Delete("feature-x")
	if got := s.Preview("feature-x"); len(got) != 0 {
		t.Errorf("Preview after Delete = %+v, want none", got)
	}
	eventually(t, 2*time.Second, "configmap deleted", func() bool {
		_, ok := configMapData(t, client, "feature-x")
		return !ok
	})
	if _, ok := configMapData(t, client, "kept"); !ok {
		t.Error("kept configmap deleted")
	}
}

// TestCollectGarbage removes the memory and the ConfigMaps of the previews
// the lister no longer lists, at start and on request, and keeps a preview
// recorded while the listing ran.
func TestCollectGarbage(t *testing.T) {
	client := fake.NewClientset(
		statusConfigMap(t, "gone-at-start", map[string]status.Entry{"backend-1": entry("aaa", status.Ready, 0)}),
		statusConfigMap(t, "kept", map[string]status.Entry{"backend-1": entry("bbb", status.Ready, 0)}),
		statusConfigMap(t, "deleted-later", map[string]status.Entry{"backend-1": entry("ccc", status.Ready, 0)}),
	)
	l := &lister{slugs: []string{"kept", "deleted-later"}}
	s := startStore(t, client, time.Hour, l)
	eventually(t, 2*time.Second, "start collection", func() bool {
		_, ok := configMapData(t, client, "gone-at-start")
		return !ok && len(s.Preview("gone-at-start")) == 0
	})

	// new-preview is recorded while the listing that misses it runs.
	listed := make(chan struct{})
	l.mu.Lock()
	l.during = func() {
		s.Record("new-preview", "backend-1", entry("ddd", status.Ready, 0))
		close(listed)
	}
	l.mu.Unlock()
	l.set("kept")
	s.CollectGarbage()
	<-listed
	eventually(t, 2*time.Second, "deleted-later collected", func() bool {
		_, ok := configMapData(t, client, "deleted-later")
		return !ok && len(s.Preview("deleted-later")) == 0
	})
	if len(s.Preview("kept")) != 1 {
		t.Error("kept collected")
	}
	if len(s.Preview("new-preview")) != 1 {
		t.Error("a preview recorded during the listing was collected")
	}
}

// TestClose_FlushesDirtyPreviews checks that Close writes the previews
// recorded since the last flush.
func TestClose_FlushesDirtyPreviews(t *testing.T) {
	client := fake.NewClientset()
	s := kube.New(client, namespace)
	status.SetInterval(s, time.Hour)
	s.Start(context.Background(), &lister{slugs: []string{"feature-x"}})
	s.Record("feature-x", "backend-1", entry("aaa", status.Ready, 0))
	s.Close(context.Background())
	got, ok := configMapData(t, client, "feature-x")
	if want := map[string]status.Entry{"backend-1": entry("aaa", status.Ready, 0)}; !ok || !equalEntries(got, want) {
		t.Errorf("configmap after Close = %+v (exists %v), want %+v", got, ok, want)
	}
}

// TestInCluster_MemoryOnlyOutsideACluster checks that without in-cluster
// configuration the store is memory-only, logs why once, and still records,
// deletes and collects.
func TestInCluster_MemoryOnlyOutsideACluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	t.Setenv("POD_NAMESPACE", "badil")
	logs := captureLogs(t)

	s := kube.InCluster(context.Background())
	if status.Persisted(s) {
		t.Fatal("Persisted = true outside a cluster")
	}
	if n := strings.Count(logs.String(), "status persistence disabled"); n != 1 {
		t.Errorf("logs = %q, want one status persistence disabled line", logs.String())
	}
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	l := &lister{slugs: []string{"feature-x", "gone"}}
	s.Start(context.Background(), l)
	defer s.Close(context.Background())
	s.Record("feature-x", "backend-1", entry("aaa", status.Ready, 0))
	s.Record("gone", "backend-1", entry("aaa", status.Ready, 0))
	s.Record("deleted", "backend-1", entry("aaa", status.Ready, 0))
	s.Delete("deleted")
	if len(s.Preview("deleted")) != 0 {
		t.Error("Delete kept the preview")
	}
	l.set("feature-x")
	s.CollectGarbage()
	eventually(t, 2*time.Second, "gone collected", func() bool { return len(s.Preview("gone")) == 0 })
	if len(s.Preview("feature-x")) != 1 {
		t.Error("feature-x collected")
	}
}

func TestValid(t *testing.T) {
	for _, s := range []string{"ready", "progressing", "degraded", "sync-failed"} {
		if !status.Valid(s) {
			t.Errorf("Valid(%q) = false", s)
		}
	}
	for _, s := range []string{"", "healthy", "Ready", "unknown"} {
		if status.Valid(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs sends the default logger to a buffer for the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}
