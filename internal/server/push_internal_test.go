package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// errUnexpected is returned by the pushForge methods the push tests do not
// call.
var errUnexpected = errors.New("unexpected call")

// pushForge is a forge.Forge whose ParseWebhook returns event, which the test
// sets before each delivery, so the push tests reach the handler without
// signing.
type pushForge struct {
	event forge.Event
}

func (f *pushForge) ParseWebhook(*http.Request) (forge.Event, error) { return f.event, nil }

func (*pushForge) GetPullRequest(context.Context, string, string, int) (*forge.PullRequest, error) {
	return nil, errUnexpected
}

func (*pushForge) ListChangedFiles(context.Context, string, string, int) ([]forge.ChangedFile, error) {
	return nil, errUnexpected
}

func (*pushForge) ListOpenPullRequests(context.Context, string, string) ([]forge.PullRequest, error) {
	return nil, errUnexpected
}

func (*pushForge) UpsertComment(context.Context, string, string, int, string) error {
	return errUnexpected
}

// newPushServer returns a server over fg with a pushForge, and metrics
// recorded in a fresh *Metrics.
func newPushServer(t *testing.T, fg *fakeGitops) (*Server, *metrics.Metrics, *LogCapture) {
	t.Helper()
	capture := &LogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })
	m := metrics.New()
	fg.open(t)
	return NewServer(&pushForge{}, fg, "t", WithMetrics(m), withFanOut(fg)), m, capture
}

// pushPayload is a push of ref to repo, whose default branch is main.
func pushPayload(repo, ref string, deleted bool) forge.Event {
	return forge.Event{
		Name: "push",
		Kind: forge.KindPush,
		Push: forge.PushEvent{
			FullName:      repo,
			Ref:           ref,
			DefaultBranch: "main",
			After:         "0000000000000000000000000000000000000002",
			Deleted:       deleted,
		},
	}
}

// push delivers the push ev to s and returns the answered status, without
// waiting for the background sync.
func push(t *testing.T, s *Server, ev forge.Event) int {
	t.Helper()
	pf, ok := s.forge.(*pushForge)
	if !ok {
		t.Fatalf("forge = %T, want *pushForge", s.forge)
	}
	pf.event = ev
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// pushAndWait is push, then waits for the background sync.
func pushAndWait(t *testing.T, s *Server, ev forge.Event) int {
	t.Helper()
	code := push(t, s, ev)
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

// syncFake is a fakeGitops with the previews a and b, both matching
// badil.yaml, and a badil.yaml blob SHA.
func syncFake() *fakeGitops {
	return &fakeGitops{previews: previewFiles("a", "b"), catalogSHA: "sha-1"}
}

func TestPush_DefaultBranchSyncsEveryPreview(t *testing.T) {
	for _, repo := range []string{"acme/gitops", "ACME/GitOps"} {
		t.Run(repo, func(t *testing.T) {
			fg := syncFake()
			fg.stale = map[string]bool{"a": true, "b": true}
			s, m, _ := newPushServer(t, fg)
			if code := pushAndWait(t, s, pushPayload(repo, "refs/heads/main", false)); code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if got := fg.synced(); !slices.Equal(got, []string{"a", "b"}) {
				t.Errorf("synced = %v, want every preview", got)
			}
			// The sync reads badil.yaml after the store was told to check the
			// remote.
			if total, beforeCatalog := fg.invalidated(); total != 1 || beforeCatalog != 1 {
				t.Errorf("invalidations = %d, %d before the badil.yaml read; want 1, 1", total, beforeCatalog)
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
			if code := pushAndWait(t, s, pushPayload(tc.repo, tc.ref, tc.deleted)); code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if got := drain(fg.started); len(got) != 0 || len(fg.synced()) != 0 {
				t.Errorf("gitops calls = %v, syncs = %v, want none", got, fg.synced())
			}
			if total, _ := fg.invalidated(); total != 0 {
				t.Errorf("invalidations = %d, want none", total)
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
	fg.stale = map[string]bool{"a": true, "b": true}
	fg.applyGate = make(chan struct{})
	s, _, _ := newPushServer(t, fg)
	if code := push(t, s, pushPayload("acme/gitops", "refs/heads/main", false)); code != http.StatusOK {
		t.Fatalf("status = %d, want 200 before the sync finished", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), quiet)
	defer cancel()
	if err := s.WaitBackground(ctx); err == nil {
		t.Fatal("WaitBackground returned while the sync was running")
	}
	close(fg.applyGate)
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
	pushAndWait(t, s, pushPayload("acme/gitops", "refs/heads/main", false))
	if left := time.Until(fg.catalogDeadline); left < syncTimeout-time.Minute || left > syncTimeout {
		t.Errorf("sync deadline in %v, want the %v syncTimeout bound", left, syncTimeout)
	}
}

// assertCounter fails t unless the counter family name of m is exactly
// samples, one "{labels} value" (or " value") line each; none means no
// sample.
func assertCounter(t *testing.T, m *metrics.Metrics, name string, samples ...string) {
	t.Helper()
	help := map[string]string{
		"badil_webhook_deliveries_total": "GitHub webhook deliveries, by verified event and HTTP status.",
		"badil_previews_synced_total":    "Previews rewritten to match badil.yaml, by a sweep, a push or /v1/sync.",
		"badil_status_events_total": "Renderer status events, by reported status and by whether they were " +
			"recorded, ignored or failed.",
	}
	var expected strings.Builder
	if len(samples) > 0 {
		expected.WriteString("# HELP " + name + " " + help[name] + "\n# TYPE " + name + " counter\n")
	}
	for _, sample := range samples {
		expected.WriteString(name + sample + "\n")
	}
	if err := testutil.GatherAndCompare(m.Gatherer(), strings.NewReader(expected.String()), name); err != nil {
		t.Error(err)
	}
}
