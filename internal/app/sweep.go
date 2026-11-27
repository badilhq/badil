package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
)

// CleanupTimeout bounds one cleanup sweep over every preview.
const CleanupTimeout = 10 * time.Minute

// CatchUpRunner runs one catch-up: *lifecycle.CatchUp.
type CatchUpRunner interface {
	Run(ctx context.Context)
}

// RunCatchUp runs one catch-up of c, bounded by CleanupTimeout and cancelled
// with ctx.
func RunCatchUp(ctx context.Context, c CatchUpRunner) {
	cctx, cancel := context.WithTimeout(ctx, CleanupTimeout)
	defer cancel()
	c.Run(cctx)
}

// SweepRecorder records the runs of the sweep loop: *metrics.Metrics.
type SweepRecorder interface {
	SweepRun(ok bool, at time.Time)
}

// StartSweepLoop runs runSweepLoop in the background of a, so shutdown
// waits for it. It starts nothing when interval is 0.
func (a *App) StartSweepLoop(
	ctx context.Context,
	api lifecycle.SweepAPI,
	catchUp CatchUpRunner,
	rec SweepRecorder,
	interval, ttl time.Duration,
) {
	if interval == 0 {
		return
	}
	slog.Default().InfoContext(ctx, "starting cleanup loop",
		slog.String("interval", interval.String()),
		slog.String("ttl", ttl.String()),
	)
	a.Go(func() {
		runSweepLoop(ctx, api, catchUp, rec, interval, ttl, time.Now)
	})
}

// runSweepLoop sweeps every interval until ctx is done, starting one interval
// from now, and runs a catch-up after each sweep, whether it succeeded or
// failed, unless ctx is done. Runs never overlap: a tick that comes during a
// run waits for it. Each sweep and each catch-up is bounded by CleanupTimeout
// and cancelled with ctx.
func runSweepLoop(
	ctx context.Context,
	api lifecycle.SweepAPI,
	catchUp CatchUpRunner,
	rec SweepRecorder,
	interval, ttl time.Duration,
	now func() time.Time,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		runSweep(ctx, api, rec, ttl, now)
		if ctx.Err() != nil {
			return
		}
		RunCatchUp(ctx, catchUp)
	}
}

// runSweep runs one sweep and records it with rec, unless ctx was done: a
// sweep cancelled by shutdown is not recorded, one that hit CleanupTimeout is
// recorded as failed.
func runSweep(
	ctx context.Context,
	api lifecycle.SweepAPI,
	rec SweepRecorder,
	ttl time.Duration,
	now func() time.Time,
) {
	sctx, cancel := context.WithTimeout(ctx, CleanupTimeout)
	defer cancel()
	err := lifecycle.Sweep(sctx, api, now().UTC(), ttl)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "cleanup failed", slog.String("err", err.Error()))
	}
	rec.SweepRun(err == nil, now())
}
