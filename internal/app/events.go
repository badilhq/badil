package app

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
)

// SyncTimeout caps each config sync run (a push to the gitops default
// branch, or Sync), counted from when the run holds the syncer's guard.
const SyncTimeout = 10 * time.Minute

// OnPullRequest reconciles the PR of a pull_request event whose action is
// opened, reopened, synchronize, labeled, unlabeled or closed, in a
// background job (startReconcile). Any other action starts nothing.
func (a *App) OnPullRequest(ctx context.Context, e forge.PullRequestEvent) {
	switch e.Action {
	case "opened", "reopened", "synchronize", "labeled", "unlabeled", "closed":
		a.startReconcile(ctx, e)
	}
}

// OnPush starts a config sync in the background for a push to the gitops
// repo's default branch that did not delete it, once the store is told to
// check the remote on its next read. Any other push is ignored.
func (a *App) OnPush(ctx context.Context, e forge.PushEvent) {
	repo := e.FullName
	reason := ""
	switch {
	case !strings.EqualFold(repo, a.store.FullName()):
		reason = "not the gitops repo"
	case e.Ref != "refs/heads/"+e.DefaultBranch:
		reason = "not the default branch"
	case e.Deleted:
		reason = "branch deleted"
	}
	attrs := []any{
		slog.String("repo", repo),
		slog.String("ref", e.Ref),
		slog.String("after", e.After),
		slog.Bool("deleted", e.Deleted),
	}
	if reason != "" {
		attrs = append(attrs, slog.String("reason", reason))
	}
	slog.InfoContext(ctx, "webhook push", attrs...)
	if reason == "" {
		a.store.Invalidate()
		a.startPushSync(ctx)
	}
}

// startPushSync runs the push config sync in the background, tracked for
// WaitBackground. It does not take the job queue: the syncer's guard
// serializes it with the other config syncs, and caps it at SyncTimeout from
// when it holds the guard. Its context is detached from ctx's cancellation.
func (a *App) startPushSync(ctx context.Context) {
	sctx := context.WithoutCancel(ctx)
	a.Go(func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(sctx, "config sync panicked",
					slog.String("trigger", lifecycle.TriggerPush),
					slog.Any("panic", rec),
				)
			}
		}()
		a.syncer.Push(sctx)
	})
}

// Sync syncs every preview, or the preview slug when it is not empty, with
// badil.yaml (lifecycle.Syncer.Sync), waiting for the config sync guard.
func (a *App) Sync(ctx context.Context, slug string) (lifecycle.SyncReport, error) {
	return a.syncer.Sync(ctx, slug)
}
