package server

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
)

// Wake is a broadcast signal: each Notify wakes every goroutine blocked on
// the channel wait returned before it. Its methods are safe for concurrent
// use, and Notify never blocks.
type Wake struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewWake returns a Wake no one waits on yet.
func NewWake() *Wake {
	return &Wake{ch: make(chan struct{})}
}

// Notify wakes every waiter: it closes the current channel and replaces it.
func (w *Wake) Notify() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.ch)
	w.ch = make(chan struct{})
}

// wait returns the channel the next Notify closes.
func (w *Wake) wait() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

// outcome is the latest reconcile outcome of a PR, its reason and head SHA,
// and when it was recorded. At keeps time.Now's monotonic reading, so it
// compares with a catch-up listing time on the monotonic clock.
type outcome struct {
	lifecycle.ReconcileResult

	At time.Time
}

// outcomes holds the latest reconcile outcome of each PR, by outcomeKey, in
// memory only. Its methods are safe for concurrent use.
type outcomes struct {
	mu sync.Mutex
	m  map[string]outcome
}

func newOutcomes() *outcomes {
	return &outcomes{m: map[string]outcome{}}
}

// outcomeKey is the key of owner/repo#number, owner and repo lowercased:
// GitHub compares them case-insensitively.
func outcomeKey(owner, repo string, number int) string {
	return strings.ToLower(owner+"/"+repo) + "#" + strconv.Itoa(number)
}

func (o *outcomes) set(key string, v outcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[key] = v
}

// setIfOlder sets v under key unless the outcome stored there was recorded
// at or after since, and reports whether it did.
func (o *outcomes) setIfOlder(key string, v outcome, since time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if old, ok := o.m[key]; ok && !old.At.Before(since) {
		return false
	}
	o.m[key] = v
	return true
}

func (o *outcomes) get(key string) (outcome, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.m[key]
	return v, ok
}

// RecordOutcome records res as the latest reconcile outcome of the PR of t,
// replacing the one before, and wakes the waits. A skip for a label that is
// not a preview label (lifecycle.OtherLabelReason) is not recorded: the event
// did not concern the PR's preview.
func (s *Server) RecordOutcome(t lifecycle.Target, res lifecycle.ReconcileResult) {
	if res.Outcome == lifecycle.OutcomeSkipped && res.Reason == lifecycle.OtherLabelReason {
		return
	}
	s.outcomes.set(outcomeKey(t.Owner, t.Repo, t.Number), outcome{ReconcileResult: res, At: time.Now()})
	s.wake.Notify()
}

// RecordCatchUpSkip records res, the skip catch-up gives a PR it filters
// out, as the latest outcome of the PR of t, and wakes the waits, only when
// the PR's stored outcome was recorded before listedAt, the time catch-up
// started listing the PR's repo. A stale listing then never replaces an
// outcome a reconcile recorded after it.
func (s *Server) RecordCatchUpSkip(t lifecycle.Target, res lifecycle.ReconcileResult, listedAt time.Time) {
	if s.outcomes.setIfOlder(outcomeKey(t.Owner, t.Repo, t.Number), outcome{ReconcileResult: res, At: time.Now()},
		listedAt) {
		s.wake.Notify()
	}
}

// EndWaits ends every blocked wait, which answers its current state at once,
// and every later one after its first evaluation. It is registered with
// http.Server.RegisterOnShutdown, so shutdown does not wait for long polls.
func (s *Server) EndWaits() {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}
