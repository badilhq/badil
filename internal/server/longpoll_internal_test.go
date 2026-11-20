package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/store"
)

// answer is the answer of a wait served in the background.
type answer struct {
	code    int
	resp    WaitResponse
	elapsed time.Duration
}

// startWait serves GET path on s in the background, within ctx, and returns
// the channel its answer is sent on.
func startWait(ctx context.Context, s *Server, path string) <-chan answer {
	out := make(chan answer, 1)
	go func() {
		start := time.Now()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, http.NoBody)
		req.Header.Set("Authorization", "Bearer "+testReadToken)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		a := answer{code: rec.Code, elapsed: time.Since(start)}
		_ = json.Unmarshal(rec.Body.Bytes(), &a.resp)
		out <- a
	}()
	return out
}

// await returns the answer sent on ch, failing t unless it comes within 2 s.
func await(t *testing.T, ch <-chan answer) answer {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(2 * time.Second):
		t.Fatal("no answer within 2s")
	}
	return answer{}
}

// assertBlocked fails t if ch gets an answer within quiet.
func assertBlocked(t *testing.T, ch <-chan answer) {
	t.Helper()
	select {
	case a := <-ch:
		t.Fatalf("answered %d %+v, want a blocked wait", a.code, a.resp)
	case <-time.After(quiet):
	}
}

func TestWait_NonFinalAnswersAtTheTimeout(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)))
	s.waitTimeout = 300 * time.Millisecond
	a := await(t, startWait(context.Background(), s, "/v1/wait?repo=acme/backend-1&pr=42"))
	if a.code != http.StatusOK || a.resp.State != "progressing" {
		t.Errorf("answer = %d %s, want 200 progressing", a.code, a.resp.State)
	}
	if a.elapsed < s.waitTimeout {
		t.Errorf("answered after %s, want at least %s", a.elapsed, s.waitTimeout)
	}
}

func TestWait_StatusEventEndsTheWaitEarly(t *testing.T) {
	for _, path := range []string{"/v1/previews/feature-x/wait", "/v1/wait?repo=acme/backend-1&pr=42&sha=" + statusSHA} {
		t.Run(path, func(t *testing.T) {
			s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)))
			s.waitTimeout = time.Minute
			ch := startWait(context.Background(), s, path)
			assertBlocked(t, ch)
			sendStatus(t, s, statusBody("backend-1", statusSHA, "ready", time.Now().UTC().Format(time.RFC3339)))
			if a := await(t, ch); a.resp.State != "ready" {
				t.Errorf("answer = %d %+v, want ready", a.code, a.resp)
			}
		})
	}
}

func TestWait_NonFinalStatusKeepsWaiting(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)))
	s.waitTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	ch := startWait(ctx, s, "/v1/previews/feature-x/wait")
	assertBlocked(t, ch)
	sendStatus(t, s, statusBody("backend-1", statusSHA, "progressing", "2026-11-19T21:05:00Z"))
	assertBlocked(t, ch)
	cancel()
	await(t, ch)
}

func TestWait_OutcomeEndsTheWaitEarly(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)))
	s.waitTimeout = time.Minute
	ch := startWait(context.Background(), s, "/v1/wait?repo=acme/front&pr=9")
	assertBlocked(t, ch)
	s.RecordOutcome(lifecycle.Target{Owner: "acme", Repo: "front", Number: 9},
		lifecycle.ReconcileResult{Outcome: lifecycle.OutcomeSkipped, Reason: lifecycle.ServiceHeldReason})
	if a := await(t, ch); a.resp.State != "refused" || a.resp.Reason != "service held by another pr" {
		t.Errorf("answer = %+v, want refused service held by another pr", a.resp)
	}
}

// TestWait_HeadMovingFetchWakes checks that a fetch moving the gitops head
// to a commit that deletes the preview wakes its waits: the PR's wait is
// then pending, and the slug's 404.
func TestWait_HeadMovingFetchWakes(t *testing.T) {
	w := NewWake()
	fg := readFake(readBadil, pinnedEntry("backend-1", backend42))
	fg.storeOpts = []store.Option{store.WithHeadMoved(w.Notify)}
	s, _, _ := newReadServer(t, fg, WithWake(w))
	s.waitTimeout = time.Minute
	byPR := startWait(context.Background(), s, "/v1/wait?repo=acme/backend-1&pr=42")
	bySlug := startWait(context.Background(), s, "/v1/previews/feature-x/wait")
	assertBlocked(t, byPR)

	fg.remote.Set("previews/feature-x/values.yaml", "")
	fg.st.Invalidate()
	if _, err := fg.st.ListPreviewFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := await(t, byPR); a.code != http.StatusOK || a.resp.State != "pending" {
		t.Errorf("PR wait = %d %s, want 200 pending", a.code, a.resp.State)
	}
	if a := await(t, bySlug); a.code != http.StatusNotFound {
		t.Errorf("slug wait = %d, want 404", a.code)
	}
}

func TestWait_EndWaitsAnswersAtOnce(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)))
	s.waitTimeout = time.Minute
	ch := startWait(context.Background(), s, "/v1/previews/feature-x/wait")
	assertBlocked(t, ch)
	s.EndWaits()
	if a := await(t, ch); a.code != http.StatusOK || a.resp.State != "progressing" {
		t.Errorf("answer = %d %s, want 200 progressing", a.code, a.resp.State)
	}
	s.EndWaits()
}

// TestWait_GoroutinesReturnToBaseline checks that many waits, ended by their
// client's disconnect or by a status event, leave no goroutine behind.
func TestWait_GoroutinesReturnToBaseline(t *testing.T) {
	s, _, _ := newReadServer(t, readFake(readBadil, pinnedEntry("backend-1", backend42)))
	s.waitTimeout = time.Minute
	baseline := runtime.NumGoroutine()

	const waiters = 50
	ctx, cancel := context.WithCancel(context.Background())
	var chans []<-chan answer
	for i := range waiters {
		wctx := context.Background()
		if i%2 == 0 {
			wctx = ctx
		}
		ch := startWait(wctx, s, "/v1/previews/feature-x/wait")
		chans = append(chans, ch)
	}
	time.Sleep(quiet)
	cancel()
	sendStatus(t, s, statusBody("backend-1", statusSHA, "ready", time.Now().UTC().Format(time.RFC3339)))
	for i, ch := range chans {
		if a := await(t, ch); i%2 == 1 && a.resp.State != "ready" {
			t.Errorf("waiter %d answered %+v, want ready", i, a.resp)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want at most the baseline %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
