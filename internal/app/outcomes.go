package app

import (
	"sync"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
)

// Outcome is the latest reconcile outcome of a PR, its reason and head SHA,
// and when it was recorded. At keeps time.Now's monotonic reading, so it
// compares with a catch-up listing time on the monotonic clock.
type Outcome struct {
	lifecycle.ReconcileResult

	At time.Time
}

// outcomes holds the latest reconcile outcome of each PR, by
// lifecycle.PRKey, in memory only. Its methods are safe for concurrent use.
type outcomes struct {
	mu sync.Mutex
	m  map[string]Outcome
}

func newOutcomes() *outcomes {
	return &outcomes{m: map[string]Outcome{}}
}

func (o *outcomes) set(key string, v Outcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[key] = v
}

// setIfOlder sets v under key unless the outcome stored there was recorded
// at or after since, and reports whether it did.
func (o *outcomes) setIfOlder(key string, v Outcome, since time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if old, ok := o.m[key]; ok && !old.At.Before(since) {
		return false
	}
	o.m[key] = v
	return true
}

func (o *outcomes) get(key string) (Outcome, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.m[key]
	return v, ok
}

// RecordOutcome records res as the latest reconcile outcome of the PR of t,
// replacing the one before, and wakes the waits. A skip for a label that is
// not a preview label (lifecycle.OtherLabelReason) is not recorded: the event
// did not concern the PR's preview.
func (a *App) RecordOutcome(t lifecycle.Target, res lifecycle.ReconcileResult) {
	if res.Outcome == lifecycle.OutcomeSkipped && res.Reason == lifecycle.OtherLabelReason {
		return
	}
	a.outcomes.set(lifecycle.PRKey(t), Outcome{ReconcileResult: res, At: time.Now()})
	a.wake.Notify()
}

// RecordCatchUpSkip records res, the skip catch-up gives a PR it filters
// out, as the latest outcome of the PR of t, and wakes the waits, only when
// the PR's stored outcome was recorded before listedAt, the time catch-up
// started listing the PR's repo. A stale listing then never replaces an
// outcome a reconcile recorded after it.
func (a *App) RecordCatchUpSkip(t lifecycle.Target, res lifecycle.ReconcileResult, listedAt time.Time) {
	if a.outcomes.setIfOlder(lifecycle.PRKey(t), Outcome{ReconcileResult: res, At: time.Now()}, listedAt) {
		a.wake.Notify()
	}
}

// LatestOutcome returns the latest reconcile outcome of the PR of t, and
// whether one is recorded.
func (a *App) LatestOutcome(t lifecycle.Target) (Outcome, bool) {
	return a.outcomes.get(lifecycle.PRKey(t))
}
