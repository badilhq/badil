package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
)

// countDelivery records a delivery answered with status. event is the
// event label: the Name of a verified delivery, or "unverified".
func (s *Server) countDelivery(event string, status int) {
	s.metrics.WebhookDelivery(event, strconv.Itoa(status))
}

// handleWebhook has the forge verify, parse and check the delivery
// (forge.WebhookParser.ParseWebhook), then dispatches it to a per-event
// handler. A rejected signature gets 401, and a delivery from any other
// installation, or without one, gets 403. Parse and dispatch failures are
// answered with 200: the delivery is authentic and there is nothing to
// re-queue, so a non-2xx would only flag it as failed in Recent Deliveries.
// GitHub does not automatically redeliver failed deliveries. Every delivery
// is counted with its status; its event is "unverified" until the signature
// is checked, since the event type is not covered by the signature.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ev, err := s.webhooks.ParseWebhook(r)
	switch {
	case errors.Is(err, forge.ErrUnverified):
		s.countDelivery("unverified", http.StatusUnauthorized)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	case errors.Is(err, forge.ErrForbidden):
		s.countDelivery(ev.Name, http.StatusForbidden)
		http.Error(w, "installation not allowed", http.StatusForbidden)
		return
	case err != nil:
		s.countDelivery(ev.Name, http.StatusOK)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Backstop: a handler that panics would, without recovery, surface as a
	// 500 that marks the delivery failed. Recover, log, 200.
	defer func() {
		if rec := recover(); rec != nil {
			slog.ErrorContext(ctx, "webhook handler panicked",
				slog.String("event", ev.Name), slog.Any("panic", rec))
		}
		s.countDelivery(ev.Name, http.StatusOK)
		w.WriteHeader(http.StatusOK)
	}()

	s.dispatchEvent(ctx, ev)
}

// dispatchEvent routes a pull request or push event to its handler. The
// forge already logged the other events.
func (s *Server) dispatchEvent(ctx context.Context, ev forge.Event) {
	switch ev.Kind {
	case forge.KindPullRequest:
		s.handlePullRequest(ctx, ev)
	case forge.KindPush:
		s.handlePush(ctx, ev.Push)
	case forge.KindOther:
	}
}

func (s *Server) handlePullRequest(ctx context.Context, ev forge.Event) {
	e := ev.PullRequest
	slog.InfoContext(ctx, "webhook pull_request",
		slog.String("action", e.Action),
		slog.String("repo", e.FullName),
		slog.Int("pr_number", e.Number),
		slog.String("head_ref", e.HeadRef),
		slog.String("head_sha", e.HeadSHA),
		slog.String("pr_author", e.Author),
	)
	switch e.Action {
	case "opened", "reopened", "synchronize", "labeled", "unlabeled", "closed":
		s.startReconcile(ctx, e)
	}
}

// handlePush starts a config sync in the background for a push to the gitops
// repo's default branch that did not delete it, once the store is told to
// check the remote on its next read. Any other push is ignored.
func (s *Server) handlePush(ctx context.Context, e forge.PushEvent) {
	repo := e.FullName
	reason := ""
	switch {
	case !strings.EqualFold(repo, s.store.FullName()):
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
		s.store.Invalidate()
		s.startPushSync(ctx)
	}
}

// startPushSync runs the push config sync in the background, tracked for
// WaitBackground. It does not take s.jobs: the syncer's guard serializes it
// with the other config syncs, and caps it at syncTimeout from when it holds
// the guard. Its context is detached from ctx's cancellation.
func (s *Server) startPushSync(ctx context.Context) {
	sctx := context.WithoutCancel(ctx)
	s.Go(func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(sctx, "config sync panicked",
					slog.String("trigger", lifecycle.TriggerPush),
					slog.Any("panic", rec),
				)
			}
		}()
		s.syncer.Push(sctx)
	})
}
