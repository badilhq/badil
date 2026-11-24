package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// quiet is how long a test waits to conclude that nothing arrives.
const quiet = 100 * time.Millisecond

func TestJobs_PanicReleasesLock(t *testing.T) {
	a := New(Config{})
	ctx := context.Background()

	started := make(chan struct{})
	panicked := make(chan any, 1)
	a.RunJob(ctx, func(rec any) { panicked <- rec }, func(context.Context) {
		close(started)
		panic("job 1 panic")
	})
	<-started
	done := make(chan struct{})
	a.RunJob(ctx, func(any) {}, func(context.Context) { close(done) })

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job 2 never ran: the panicking job kept the lock")
	}
	if err := a.WaitBackground(ctx); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	if rec := <-panicked; rec != "job 1 panic" {
		t.Errorf("panic = %v, want job 1 panic", rec)
	}
}

func TestGo_TrackedByWaitBackground(t *testing.T) {
	a := New(Config{})
	release := make(chan struct{})
	done := make(chan struct{})
	a.Go(func() {
		<-release
		close(done)
	})

	ctx, cancel := context.WithTimeout(context.Background(), quiet)
	defer cancel()
	if err := a.WaitBackground(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitBackground before release = %v, want deadline exceeded", err)
	}
	close(release)
	if err := a.WaitBackground(context.Background()); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	select {
	case <-done:
	default:
		t.Error("WaitBackground returned before f finished")
	}
}
