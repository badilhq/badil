package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v71/github"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// trustingValidator accepts every delivery and parses it with go-github, so
// the push tests reach the handler without signing.
type trustingValidator struct{}

func (trustingValidator) ValidateWebhook(r *http.Request) ([]byte, error) { return io.ReadAll(r.Body) }

func (trustingValidator) ParseEvent(eventType string, payload []byte) (any, error) {
	return gh.ParseWebHook(eventType, payload)
}

// newPushServer returns a server over fg with the trusting validator, the
// installation 123 allowed, and metrics recorded in a fresh *Metrics.
func newPushServer(t *testing.T, fg *fakeGitops) (*Server, *metrics.Metrics, *LogCapture) {
	t.Helper()
	capture := &LogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })
	m := metrics.New()
	return NewServer(trustingValidator{}, fg, "t", 123, WithMetrics(m)), m, capture
}

// pushPayload is a push of ref to repo, whose default branch is main.
func pushPayload(t *testing.T, repo, ref string, deleted bool) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"ref":          ref,
		"after":        "0000000000000000000000000000000000000002",
		"deleted":      deleted,
		"repository":   map[string]any{"full_name": repo, "default_branch": "main"},
		"installation": map[string]any{"id": 123},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// push delivers a push to s and returns the answered status, without
// waiting for the background sync.
func push(t *testing.T, s *Server, body []byte) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Github-Event", "push")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// pushAndWait is push, then waits for the background sync.
func pushAndWait(t *testing.T, s *Server, body []byte) int {
	t.Helper()
	code := push(t, s, body)
	if err := s.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	return code
}

// previewFiles returns an empty preview file for each slug.
func previewFiles(slugs ...string) map[string]*preview.PreviewValues {
	files := map[string]*preview.PreviewValues{}
	for _, slug := range slugs {
		files[slug] = &preview.PreviewValues{}
	}
	return files
}

// syncFake is a fakeGitops listing a and b, with a badil.yaml blob SHA.
func syncFake() *fakeGitops {
	return &fakeGitops{listed: []string{"a", "b"}, previews: previewFiles("a", "b"), catalogSHA: "sha-1"}
}

func TestPush_DefaultBranchSyncsEveryPreview(t *testing.T) {
	for _, repo := range []string{"acme/gitops", "ACME/GitOps"} {
		t.Run(repo, func(t *testing.T) {
			fg := syncFake()
			s, m, _ := newPushServer(t, fg)
			if code := pushAndWait(t, s, pushPayload(t, repo, "refs/heads/main", false)); code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if got := fg.synced(); !slices.Equal(got, []string{"a", "b"}) {
				t.Errorf("synced = %v, want every preview", got)
			}
			assertCounter(t, m, "badil_webhook_deliveries_total", `{event="push",status="200"} 1`)
		})
	}
}

func TestPush_OtherPushesIgnored(t *testing.T) {
	cases := []struct {
		name, repo, ref string
		deleted         bool
		reason          string
	}{
		{name: "other repo", repo: "acme/backend-1", ref: "refs/heads/main", reason: "not the gitops repo"},
		{name: "other branch", repo: "acme/gitops", ref: "refs/heads/feature", reason: "not the default branch"},
		{name: "tag", repo: "acme/gitops", ref: "refs/tags/main", reason: "not the default branch"},
		{name: "deletion", repo: "acme/gitops", ref: "refs/heads/main", deleted: true, reason: "branch deleted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := syncFake()
			fg.started = make(chan string, 4)
			s, m, capture := newPushServer(t, fg)
			if code := pushAndWait(t, s, pushPayload(t, tc.repo, tc.ref, tc.deleted)); code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if got := drain(fg.started); len(got) != 0 || len(fg.synced()) != 0 {
				t.Errorf("gitops calls = %v, syncs = %v, want none", got, fg.synced())
			}
			assertCounter(t, m, "badil_webhook_deliveries_total", `{event="push",status="200"} 1`)
			if _, attrs := teardownRecord(t, capture, "webhook push"); attrs["reason"].String() != tc.reason {
				t.Errorf("skip reason = %q, want %q", attrs["reason"], tc.reason)
			}
		})
	}
}

func TestPush_WaitBackgroundWaitsForSync(t *testing.T) {
	fg := syncFake()
	fg.syncGate = make(chan struct{})
	s, _, _ := newPushServer(t, fg)
	if code := push(t, s, pushPayload(t, "acme/gitops", "refs/heads/main", false)); code != http.StatusOK {
		t.Fatalf("status = %d, want 200 before the sync finished", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), quiet)
	defer cancel()
	if err := s.WaitBackground(ctx); err == nil {
		t.Fatal("WaitBackground returned while the sync was running")
	}
	close(fg.syncGate)
	if err := s.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	if got := fg.synced(); len(got) != 2 {
		t.Errorf("synced = %v, want every preview", got)
	}
}

func TestPush_BoundedBySyncTimeout(t *testing.T) {
	fg := syncFake()
	s, _, _ := newPushServer(t, fg)
	pushAndWait(t, s, pushPayload(t, "acme/gitops", "refs/heads/main", false))
	if left := time.Until(fg.catalogDeadline); left < syncTimeout-time.Minute || left > syncTimeout {
		t.Errorf("sync deadline in %v, want the %v syncTimeout bound", left, syncTimeout)
	}
}

// assertCounter fails t unless the counter family name of m is exactly
// samples, one "{labels} value" (or " value") line each.
func assertCounter(t *testing.T, m *metrics.Metrics, name string, samples ...string) {
	t.Helper()
	help := map[string]string{
		"badil_webhook_deliveries_total": "GitHub webhook deliveries, by verified event and HTTP status.",
		"badil_previews_synced_total":    "Previews rewritten to match badil.yaml, by a sweep, a push or /v1/sync.",
	}
	var expected strings.Builder
	expected.WriteString("# HELP " + name + " " + help[name] + "\n# TYPE " + name + " counter\n")
	for _, sample := range samples {
		expected.WriteString(name + sample + "\n")
	}
	if err := testutil.GatherAndCompare(m.Gatherer(), strings.NewReader(expected.String()), name); err != nil {
		t.Error(err)
	}
}
