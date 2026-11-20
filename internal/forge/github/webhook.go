package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/forge"
)

// installationEvent is implemented by go-github event types that carry the
// installation the delivery was sent for.
type installationEvent interface {
	GetInstallation() *gh.Installation
}

// webhookEvent maps a verified X-Github-Event value to the closed set of the
// event label of the webhook deliveries metric.
func webhookEvent(eventType string) string {
	switch eventType {
	case "ping", "installation", "installation_repositories", "pull_request", "push":
		return eventType
	}
	return "other"
}

// ParseWebhook validates the delivery's signature, parses its payload, checks
// that it comes from the allowed installation and returns it as a
// forge.Event. A rejected signature is logged and returns
// forge.ErrUnverified with a zero Event. Every later outcome has the Event's
// Name: a payload that can't be parsed is logged and its error returned; a
// delivery from another installation, or without one, is logged and returns
// forge.ErrForbidden (ping is exempt because it carries no installation).
// ping, installation and installation_repositories deliveries, and the event
// types badil does not handle, are logged here and returned with
// forge.KindOther. The X-Github-Event value is logged as the event attribute.
func (f *Forge) ParseWebhook(r *http.Request) (forge.Event, error) {
	eventType := r.Header.Get("X-Github-Event")
	ctx := r.Context()

	payload, err := f.app.ValidateWebhook(r)
	if err != nil {
		// A rejected delivery means a misconfigured secret or someone probing
		// the public endpoint with forged requests — worth surfacing. Log the
		// source and event type only; never the (untrusted) payload.
		slog.WarnContext(ctx, "webhook signature rejected",
			slog.String("event", eventType),
			slog.String("remote_addr", r.RemoteAddr),
		)
		return forge.Event{}, fmt.Errorf("%w: %w", forge.ErrUnverified, err)
	}
	ev := forge.Event{Name: webhookEvent(eventType), Kind: forge.KindOther}

	parsed, err := gh.ParseWebHook(eventType, payload)
	if err != nil {
		slog.ErrorContext(ctx, "webhook parse failed",
			slog.String("event", eventType), slog.String("err", err.Error()))
		return ev, err
	}

	if eventType != "ping" {
		var inst *gh.Installation
		if ie, ok := parsed.(installationEvent); ok {
			inst = ie.GetInstallation()
		}
		if inst.GetID() == 0 || inst.GetID() != f.installationID {
			slog.WarnContext(ctx, "webhook installation rejected",
				slog.String("event", eventType),
				slog.Int64("installation_id", inst.GetID()),
				slog.String("account", inst.GetAccount().GetLogin()),
			)
			return ev, forge.ErrForbidden
		}
	}

	switch e := parsed.(type) {
	case *gh.PingEvent:
		logPing(ctx, e)
	case *gh.InstallationEvent:
		logInstallation(ctx, e)
	case *gh.InstallationRepositoriesEvent:
		logInstallationRepositories(ctx, e)
	case *gh.PullRequestEvent:
		ev.Kind = forge.KindPullRequest
		ev.PullRequest = pullRequestEvent(e)
	case *gh.PushEvent:
		ev.Kind = forge.KindPush
		ev.Push = pushEvent(e)
	default:
		// GitHub adds event types over time; that's not an error.
		slog.WarnContext(ctx, "unhandled webhook event", slog.String("event", eventType))
	}
	return ev, nil
}

// pullRequestEvent returns the payload of a pull_request delivery.
func pullRequestEvent(e *gh.PullRequestEvent) forge.PullRequestEvent {
	var labels []string
	if pr := e.GetPullRequest(); pr != nil {
		labels = labelNames(pr.Labels)
	}
	return forge.PullRequestEvent{
		Owner:    e.GetRepo().GetOwner().GetLogin(),
		Repo:     e.GetRepo().GetName(),
		FullName: e.GetRepo().GetFullName(),
		Number:   e.GetNumber(),
		Action:   e.GetAction(),
		Label:    e.GetLabel().GetName(),
		Labels:   labels,
		HeadRef:  e.GetPullRequest().GetHead().GetRef(),
		HeadSHA:  e.GetPullRequest().GetHead().GetSHA(),
		Author:   e.GetPullRequest().GetUser().GetLogin(),
	}
}

// pushEvent returns the payload of a push delivery.
func pushEvent(e *gh.PushEvent) forge.PushEvent {
	return forge.PushEvent{
		FullName:      e.GetRepo().GetFullName(),
		Ref:           e.GetRef(),
		DefaultBranch: e.GetRepo().GetDefaultBranch(),
		After:         e.GetAfter(),
		Deleted:       e.GetDeleted(),
	}
}

func logPing(ctx context.Context, e *gh.PingEvent) {
	slog.InfoContext(ctx, "webhook ping",
		slog.String("zen", e.GetZen()),
		slog.Int64("hook_id", e.GetHookID()),
	)
}

func logInstallation(ctx context.Context, e *gh.InstallationEvent) {
	slog.InfoContext(ctx, "webhook installation",
		slog.String("action", e.GetAction()),
		slog.Int64("installation_id", e.GetInstallation().GetID()),
		slog.String("account", e.GetInstallation().GetAccount().GetLogin()),
		slog.Int("repositories", len(e.Repositories)),
	)
}

func logInstallationRepositories(ctx context.Context, e *gh.InstallationRepositoriesEvent) {
	slog.InfoContext(ctx, "webhook installation_repositories",
		slog.String("action", e.GetAction()),
		slog.Int64("installation_id", e.GetInstallation().GetID()),
		slog.Int("repositories_added", len(e.RepositoriesAdded)),
		slog.Int("repositories_removed", len(e.RepositoriesRemoved)),
	)
}
