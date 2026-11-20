package server

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// ReconcileTimeout caps the background work for a single reconcile, shared by
// every service it deploys or by its teardown.
const ReconcileTimeout = 20 * time.Second

// prTarget builds the Target of a pull_request event, without its slug.
func prTarget(e forge.PullRequestEvent) lifecycle.Target {
	return lifecycle.Target{
		Owner:    e.Owner,
		Repo:     e.Repo,
		FullName: e.FullName,
		Number:   e.Number,
	}
}

// missingPRFields reports whether t lacks the fields needed to find its PR.
func missingPRFields(t lifecycle.Target) bool {
	return t.Owner == "" || t.Repo == "" || t.Number == 0
}

// runJob runs job in the background, so the webhook is answered within
// GitHub's 10-second limit. The job is tracked for WaitBackground, runs once
// no other job holds s.jobs, gets a context detached from ctx's cancellation
// and capped at s.jobTimeout from when it holds s.jobs, and has its panics
// passed to onPanic.
func (s *Server) runJob(ctx context.Context, onPanic func(rec any), job func(context.Context)) {
	s.bg.Go(func() {
		defer func() {
			if rec := recover(); rec != nil {
				onPanic(rec)
			}
		}()
		s.jobs.Lock()
		defer s.jobs.Unlock()
		jctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.jobTimeout)
		defer cancel()
		job(jctx)
	})
}

// startReconcile reconciles the event's PR with its preview in a background
// job. Events missing the fields needed to find the PR, and closed events
// with neither a head branch slug nor a named label slug, start nothing. A closed event logs as a teardown.
func (s *Server) startReconcile(ctx context.Context, e forge.PullRequestEvent) {
	ev := lifecycle.Event{
		Target:  prTarget(e),
		Action:  e.Action,
		HeadRef: e.HeadRef,
		Label:   e.Label,
		Labels:  e.Labels,
	}
	if ev.Action == "closed" {
		s.startClosed(ctx, ev)
		return
	}
	if missingPRFields(ev.Target) {
		slog.InfoContext(ctx, "preview reconcile skipped",
			slog.String("repo", ev.FullName),
			slog.Int("pr_number", ev.Number),
			slog.String("action", ev.Action),
			slog.String("reason", "missing pr fields"),
		)
		return
	}
	s.QueueReconcile(ctx, ev, func(lifecycle.ReconcileResult) {})
}

// QueueReconcile reconciles ev in a background job (runJob), as a
// pull_request delivery does: after the jobs before it, alone, and capped at
// the job timeout. done gets ReconcilePR's result once it completes, or the
// failed outcome when it panics. It is a lifecycle.ReconcileFunc.
func (s *Server) QueueReconcile(ctx context.Context, ev lifecycle.Event, done func(lifecycle.ReconcileResult)) {
	s.runJob(ctx, func(rec any) {
		slog.ErrorContext(ctx, "preview reconcile panicked",
			slog.String("repo", ev.FullName),
			slog.Int("pr_number", ev.Number),
			slog.String("action", ev.Action),
			slog.Any("panic", rec),
		)
		done(lifecycle.ReconcileResult{Outcome: lifecycle.OutcomeFailed})
	}, func(jctx context.Context) {
		done(lifecycle.ReconcilePR(jctx, s.reconcileAPI(), ev))
	})
}

// startClosed reconciles a closed event, logging as a teardown of the
// payload's head branch. It starts nothing when the event misses the fields
// needed to find the PR, or when neither its head branch nor a named label of
// its payload has a slug.
func (s *Server) startClosed(ctx context.Context, ev lifecycle.Event) {
	slug := preview.Slugify(ev.HeadRef)
	reason := ""
	switch {
	case missingPRFields(ev.Target):
		reason = "missing pr fields"
	case slug == "" && !hasNamedSlug(ev.Labels):
		reason = "no preview slug"
	}
	if reason != "" {
		slog.InfoContext(ctx, "preview teardown skipped",
			slog.String("slug", slug),
			slog.String("repo", ev.FullName),
			slog.Int("pr_number", ev.Number),
			slog.String("reason", reason),
		)
		return
	}

	s.runJob(ctx, func(rec any) {
		slog.ErrorContext(ctx, "preview teardown panicked",
			slog.String("slug", slug),
			slog.String("repo", ev.FullName),
			slog.Int("pr_number", ev.Number),
			slog.Any("panic", rec),
		)
	}, func(jctx context.Context) {
		lifecycle.ReconcilePR(jctx, s.reconcileAPI(), ev)
	})
}

// hasNamedSlug reports whether labels hold a named label of the default
// preview label with a non-empty slug.
func hasNamedSlug(labels []string) bool {
	return slices.ContainsFunc(labels, func(l string) bool {
		slug, named, _ := preview.ParseLabel(l, store.DefaultLabel)
		return named && slug != ""
	})
}
