package server

import (
	"context"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/lifecycle"
)

// catchUpEvent is the event catch-up reconciles acme/backend-1#42 with.
func catchUpEvent() lifecycle.Event {
	return lifecycle.Event{
		Target:  lifecycle.Target{Owner: "acme", Repo: "backend-1", FullName: "acme/backend-1", Number: 42},
		Action:  "synchronize",
		HeadRef: "feature/add-pricing",
		Labels:  []string{"preview"},
	}
}

// result waits for the result sent on results.
func result(t *testing.T, results chan lifecycle.ReconcileResult) lifecycle.ReconcileResult {
	t.Helper()
	select {
	case res := <-results:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("done never called")
	}
	return lifecycle.ReconcileResult{}
}

func TestQueueReconcile_WaitsForWebhookReconciles(t *testing.T) {
	fg := &fakeGitops{
		badilYAML: servicesWithPreview, applyGate: make(chan struct{}), pr: closedPR(), started: make(chan string, 8),
	}
	s, _ := newTeardownServer(t, fg, app.Config{})
	ctx := context.Background()
	s.handlePullRequest(ctx, prEvent("closed"))
	waitStarted(t, fg.started, "catalog")
	waitStarted(t, fg.started, "apply")

	results := make(chan lifecycle.ReconcileResult, 1)
	s.app.QueueReconcile(ctx, catchUpEvent(), func(res lifecycle.ReconcileResult) { results <- res })
	select {
	case got := <-fg.started:
		t.Fatalf("catch-up reconcile reached %q while the webhook reconcile held the gate", got)
	case <-time.After(quiet):
	}

	close(fg.applyGate)
	if err := s.app.WaitBackground(ctx); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	if got := drain(fg.started); len(got) != 2 || got[0] != "catalog" || got[1] != "apply" {
		t.Errorf("after release started = %v, want [catalog apply]", got)
	}
	if res := result(t, results); res.Outcome != "torn_down" {
		t.Errorf("result = %+v, want the reconcile's torn_down outcome", res)
	}
}

func TestQueueReconcile_PanicIsAFailure(t *testing.T) {
	fg := reconcileFake()
	fg.pr = closedPR()
	fg.applyPanic = true
	s, capture := newTeardownServer(t, fg, app.Config{})
	results := make(chan lifecycle.ReconcileResult, 1)
	s.app.QueueReconcile(context.Background(), catchUpEvent(), func(res lifecycle.ReconcileResult) { results <- res })
	if err := s.app.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}

	if res := result(t, results); res.Outcome != lifecycle.OutcomeFailed {
		t.Errorf("result = %+v, want the failed outcome", res)
	}
	if _, attrs := teardownRecord(t, capture, "preview reconcile panicked"); attrs["action"].String() != "synchronize" {
		t.Errorf("panic logged with %v, want action synchronize", attrs)
	}
}
