package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
)

const teardownSlug = "feature-add-pricing"

// teardownSubject is the commit subject of a teardown of acme/backend-1#42
// from teardownSlug.
const teardownSubject = "chore(preview): unpin acme/backend-1#42 from " + teardownSlug

// pinnedAt returns the previews slug pinned by acme/backend-1#42 alone.
func pinnedAt(slug string) map[string]*preview.Values {
	return map[string]*preview.Values{slug: {Services: []preview.ServiceEntry{pinnedBy42()}}}
}

// newTeardownServer returns a server backed by fg, over the app cfg
// configures with fg as its store, forge and fan-out, and configured by opts,
// with every log record captured.
func newTeardownServer(t *testing.T, fg *fakeGitops, cfg app.Config, opts ...Option) (*Server, *LogCapture) {
	t.Helper()
	capture := &LogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fg.open(t)
	cfg.Store, cfg.Forge, cfg.FanOut = fg, fg, fakeFanOut(fg)
	return NewServer(app.New(cfg), fg, nil, fg, "t", opts...), capture
}

// prEvent builds a pull_request event for acme/backend-1#42 on
// feature/add-pricing.
func prEvent(action string) forge.Event {
	return forge.Event{
		Name: "pull_request",
		Kind: forge.KindPullRequest,
		PullRequest: forge.PullRequestEvent{
			Owner:    "acme",
			Repo:     "backend-1",
			FullName: "acme/backend-1",
			Number:   42,
			Action:   action,
			HeadRef:  "feature/add-pricing",
		},
	}
}

// closedPR is acme/backend-1#42 on feature/add-pricing, closed, as read from
// GitHub.
func closedPR() *forge.PullRequest {
	pr := fetchedPR(false)
	pr.HeadRef = "feature/add-pricing"
	return pr
}

// closeAndWait delivers a closed event and waits for the background teardown.
func closeAndWait(t *testing.T, s *Server) {
	t.Helper()
	s.handlePullRequest(context.Background(), prEvent("closed"))
	if err := s.app.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
}

// teardownRecord returns the record logged with msg, and its attributes.
func teardownRecord(t *testing.T, capture *LogCapture, msg string) (slog.Record, map[string]slog.Value) {
	t.Helper()
	for _, r := range capture.Records() {
		if r.Message != msg {
			continue
		}
		attrs := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value
			return true
		})
		return r, attrs
	}
	t.Fatalf("no %q log record", msg)
	return slog.Record{}, nil
}

// assertTeardownAttrs checks the attributes every teardown log line carries.
func assertTeardownAttrs(t *testing.T, attrs map[string]slog.Value) {
	t.Helper()
	if got := attrs["slug"].String(); got != teardownSlug {
		t.Errorf("slug = %q, want %q", got, teardownSlug)
	}
	if got := attrs["repo"].String(); got != "acme/backend-1" {
		t.Errorf("repo = %q, want acme/backend-1", got)
	}
	if got := attrs["pr_number"].Int64(); got != 42 {
		t.Errorf("pr_number = %d, want 42", got)
	}
}

func TestTeardown_ClosedStartsTeardown(t *testing.T) {
	fg := &fakeGitops{pr: closedPR(), previews: pinnedAt(teardownSlug)}
	s, _ := newTeardownServer(t, fg, app.Config{})
	s.handlePullRequest(context.Background(), prEvent("closed"))
	if err := s.app.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	if fg.teardownSlug != teardownSlug || fg.teardownOwner != "acme" ||
		fg.teardownRepo != "backend-1" || fg.teardownNumber != 42 {
		t.Errorf("teardown(%q, %q, %q, %d), want (%q, acme, backend-1, 42)",
			fg.teardownSlug, fg.teardownOwner, fg.teardownRepo, fg.teardownNumber, teardownSlug)
	}
}

func TestTeardown_OpenedStartsNoTeardown(t *testing.T) {
	fg := &fakeGitops{}
	s, _ := newTeardownServer(t, fg, app.Config{})
	s.handlePullRequest(context.Background(), prEvent("opened"))
	if err := s.app.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	if fg.teardownSlug != "" {
		t.Errorf("opened started a teardown for %q", fg.teardownSlug)
	}
}

func TestTeardown_RunsInBackground(t *testing.T) {
	fg := &fakeGitops{applyGate: make(chan struct{}), pr: closedPR(), previews: pinnedAt(teardownSlug)}
	s, _ := newTeardownServer(t, fg, app.Config{})

	returned := make(chan struct{})
	go func() {
		s.handlePullRequest(context.Background(), prEvent("closed"))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("handlePullRequest blocked on the teardown")
	}

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.app.WaitBackground(expired); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitBackground while blocked = %v, want context.Canceled", err)
	}

	close(fg.applyGate)
	if err := s.app.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	if fg.teardownSlug != teardownSlug {
		t.Errorf("teardown slug = %q, want %q", fg.teardownSlug, teardownSlug)
	}
}

func TestTeardown_UpdateComments(t *testing.T) {
	fg := &fakeGitops{
		pr:      closedPR(),
		catalog: []string{"backend-1", "backend-2", "front"},
		previews: map[string]*preview.Values{teardownSlug: {Services: []preview.ServiceEntry{
			pinnedBy42(),
			{Name: "backend-2", CommitSha: "bbbbbbbbbb", Metadata: &preview.Metadata{
				PRURL: "https://github.com/acme/backend-2/pull/7", PRNumber: "7",
			}},
			{Name: "front"},
		}}},
	}
	s, capture := newTeardownServer(t, fg, app.Config{})
	closeAndWait(t, s)
	commitURL := fg.headURL()

	if len(fg.upserts) != 1 {
		t.Fatalf("upserts = %+v, want one on the closed PR", fg.upserts)
	}
	up := fg.upserts[0]
	if up.owner != "acme" || up.repo != "backend-1" || up.number != 42 {
		t.Errorf("upsert target = %s/%s#%d, want acme/backend-1#42", up.owner, up.repo, up.number)
	}
	wantComment := "## 🧹 Preview: `feature-add-pricing`\n\n" +
		"Removed `backend-1` from this preview (PR closed).\n\n" +
		"---\n" +
		"🔗 [" + teardownSubject + "](" + commitURL + ")"
	if up.body != wantComment {
		t.Errorf("closed-PR comment mismatch\n got:\n%s\nwant:\n%s", up.body, wantComment)
	}

	wantSummary := "## 🚀 Preview: `feature-add-pricing`\n\n" +
		"- **backend-1** · 🔄 `main`\n" +
		"- **backend-2** · 📌 [`bbbbbbbb`](https://github.com/acme/backend-2/commit/bbbbbbbbbb)" +
		" · [PR #7](https://github.com/acme/backend-2/pull/7)\n" +
		"- **front** · 🔄 `main`\n\n" +
		"> ⏳ A pinned service starts once CI has pushed the image for its commit. " +
		"Until then its link may not respond.\n\n" +
		"---\n" +
		"🔗 [" + teardownSubject + "](" + commitURL + ")"
	if !fg.fanOutBodyCalled || fg.capturedSummary != wantSummary {
		t.Errorf("fan-out summary mismatch (called=%v)\n got:\n%s\nwant:\n%s",
			fg.fanOutBodyCalled, fg.capturedSummary, wantSummary)
	}

	r, attrs := teardownRecord(t, capture, "preview teardown")
	if r.Level != slog.LevelInfo {
		t.Errorf("level = %v, want INFO", r.Level)
	}
	assertTeardownAttrs(t, attrs)
	if got := attrs["removed"].String(); got != "backend-1" {
		t.Errorf("removed = %q, want backend-1", got)
	}
	if attrs["deleted"].Bool() {
		t.Error("deleted = true, want false")
	}
	if got := attrs["gitops_commit_url"].String(); got != commitURL {
		t.Errorf("gitops_commit_url = %q", got)
	}
}

func TestTeardown_UpdateCommentsLinkArgoCD(t *testing.T) {
	fg := &fakeGitops{
		pr: closedPR(),
		badilYAML: "services:\n" +
			"  backend-1: {repo: https://github.com/acme/backend-1.git}\n" +
			"  front: {repo: https://github.com/acme/front.git}\n" +
			"preview:\n  argocdURL: https://argocd.example.com\n",
		previews: map[string]*preview.Values{teardownSlug: {Services: []preview.ServiceEntry{
			pinnedBy42(),
			{Name: "front", CommitSha: "cccccccccc", Metadata: &preview.Metadata{
				PRURL: "https://github.com/acme/front/pull/9",
			}},
		}}},
	}
	s, _ := newTeardownServer(t, fg, app.Config{})
	closeAndWait(t, s)

	want := preview.ArgoCDAppsURL("https://argocd.example.com", "feature-add-pricing")
	if !fg.fanOutBodyCalled || !strings.Contains(fg.capturedSummary, want) {
		t.Errorf("fan-out summary (called=%v) missing %q, got:\n%s", fg.fanOutBodyCalled, want, fg.capturedSummary)
	}
	if len(fg.upserts) != 1 || strings.Contains(fg.upserts[0].body, "Argo CD") {
		t.Errorf("upserts = %+v, want one teardown comment without the Argo CD link", fg.upserts)
	}
}

func TestTeardown_DeleteComment(t *testing.T) {
	fg := &fakeGitops{pr: closedPR(), previews: pinnedAt(teardownSlug)}
	s, capture := newTeardownServer(t, fg, app.Config{})
	closeAndWait(t, s)

	wantComment := "## 🧹 Preview: `feature-add-pricing`\n\n" +
		"Preview deleted (PR closed): no other PR is pinned.\n\n" +
		"---\n" +
		"🔗 [" + teardownSubject + "](" + fg.headURL() + ")"
	if len(fg.upserts) != 1 || fg.upserts[0].body != wantComment {
		t.Errorf("upserts = %+v, want one with body:\n%s", fg.upserts, wantComment)
	}
	if fg.fanOutBodyCalled {
		t.Error("a deleted preview must not fan out to remaining PRs")
	}
	_, attrs := teardownRecord(t, capture, "preview teardown")
	if !attrs["deleted"].Bool() {
		t.Error("deleted = false, want true")
	}
}

func TestTeardown_Failure(t *testing.T) {
	fg := &fakeGitops{applyErr: errors.New("boom"), pr: closedPR()}
	s, capture := newTeardownServer(t, fg, app.Config{})
	closeAndWait(t, s)

	r, attrs := teardownRecord(t, capture, "preview teardown failed")
	if r.Level != slog.LevelError {
		t.Errorf("level = %v, want ERROR", r.Level)
	}
	assertTeardownAttrs(t, attrs)
	if got := attrs["err"].String(); got != "boom" {
		t.Errorf("err = %q, want boom", got)
	}
	if len(fg.upserts) != 0 || fg.fanOutBodyCalled {
		t.Error("a failed teardown must not comment")
	}
}

func TestTeardown_NothingRemoved(t *testing.T) {
	fg := &fakeGitops{pr: closedPR()}
	s, capture := newTeardownServer(t, fg, app.Config{})
	closeAndWait(t, s)

	r, attrs := teardownRecord(t, capture, "preview teardown skipped")
	if r.Level != slog.LevelInfo {
		t.Errorf("level = %v, want INFO", r.Level)
	}
	assertTeardownAttrs(t, attrs)
	if got := attrs["reason"].String(); got != "no preview or no matching service" {
		t.Errorf("reason = %q", got)
	}
	if len(fg.upserts) != 0 || fg.fanOutBodyCalled {
		t.Error("a skipped teardown must not comment")
	}
}

func TestTeardown_PanicRecovered(t *testing.T) {
	fg := &fakeGitops{applyPanic: true, pr: closedPR()}
	s, capture := newTeardownServer(t, fg, app.Config{})
	closeAndWait(t, s)

	r, attrs := teardownRecord(t, capture, "preview teardown panicked")
	if r.Level != slog.LevelError {
		t.Errorf("level = %v, want ERROR", r.Level)
	}
	assertTeardownAttrs(t, attrs)
	if got := attrs["panic"].String(); got != "fake apply panic" {
		t.Errorf("panic = %q", got)
	}
}

func TestTeardown_ClosedEmptyBranchSlugWithNamedLabel(t *testing.T) {
	pr := fetchedPR(false, "preview:pricing")
	pr.HeadRef = "---"
	fg := &fakeGitops{badilYAML: servicesWithPreview, pr: pr, previews: pinnedAt("pricing")}
	s, _ := newTeardownServer(t, fg, app.Config{})
	ev := prEvent("closed")
	ev.PullRequest.HeadRef = "---"
	ev.PullRequest.Labels = []string{"preview:pricing"}
	s.handlePullRequest(context.Background(), ev)
	if err := s.app.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}

	if fg.prCalls != 1 {
		t.Errorf("GetPullRequest calls = %d, want 1", fg.prCalls)
	}
	if fg.teardownSlug != "pricing" || fg.teardownNumber != 42 {
		t.Errorf("teardown(%q, #%d), want (pricing, #42)", fg.teardownSlug, fg.teardownNumber)
	}
}

func TestTeardown_ClosedWithoutSlugSkipped(t *testing.T) {
	fg := &fakeGitops{pr: closedPR()}
	s, capture := newTeardownServer(t, fg, app.Config{})
	ev := prEvent("closed")
	ev.PullRequest.HeadRef = "---"
	ev.PullRequest.Labels = []string{"preview", "preview:---"}
	s.handlePullRequest(context.Background(), ev)
	if err := s.app.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}

	_, attrs := teardownRecord(t, capture, "preview teardown skipped")
	if got := attrs["reason"].String(); got != "no preview slug" {
		t.Errorf("reason = %q, want 'no preview slug'", got)
	}
	if fg.prCalls != 0 {
		t.Errorf("GetPullRequest calls = %d, want 0", fg.prCalls)
	}
}
