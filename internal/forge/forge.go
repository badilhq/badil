// Package forge defines what badil needs from the forge hosting the service
// repos: pull request reads, the preview comment and webhook deliveries,
// through neutral types.
package forge

import (
	"context"
	"errors"
	"net/http"
)

// MaxListedFiles is the most files a forge lists for a pull request: GitHub
// responses "include a maximum of 3000 files".
const MaxListedFiles = 3000

// Sentinel errors. Callers test them with errors.Is.
var (
	// ErrUnverified is a webhook delivery whose signature is missing or bad.
	ErrUnverified = errors.New("forge: webhook signature rejected")
	// ErrForbidden is a webhook delivery from an installation that is not
	// allowed.
	ErrForbidden = errors.New("forge: installation not allowed")
	// ErrNotFound is a pull request, or a comment target, that does not
	// exist.
	ErrNotFound = errors.New("gitops: not found")
	// ErrUnauthorized is a request the forge refused for its credentials.
	ErrUnauthorized = errors.New("gitops: unauthorized")
)

// Forge reads pull requests, upserts badil's comment on them and parses
// webhook deliveries.
type Forge interface {
	// GetPullRequest reads owner/repo#number. Lookup failures are returned
	// as errors.
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error)
	// ListChangedFiles lists the files owner/repo#number changes, every page,
	// at most MaxListedFiles. A failure on any page is returned as an error.
	ListChangedFiles(ctx context.Context, owner, repo string, number int) ([]ChangedFile, error)
	// UpsertComment posts body as badil's comment on owner/repo#number: the
	// existing one is updated, or a new one is created.
	UpsertComment(ctx context.Context, owner, repo string, number int, body string) error
	// ParseWebhook verifies, parses and checks a webhook delivery. A bad or
	// missing signature returns ErrUnverified and a zero Event. Otherwise the
	// Event has its Name, also with a parse error, or with ErrForbidden for a
	// delivery from an installation that is not allowed.
	ParseWebhook(r *http.Request) (Event, error)
}

// PullRequest is the state of a pull request as read from the forge.
type PullRequest struct {
	// Open is true when the state is open; a merged PR is closed.
	Open    bool
	HeadRef string
	HeadSHA string
	// Author is the PR author's login.
	Author string
	// URL is the PR's html_url.
	URL string
	// Labels holds the names of the PR's labels.
	Labels []string
	// FullName is the base repository's owner/repo.
	FullName string
	// HeadFullName is the head repository's owner/repo; it differs from
	// FullName on a fork PR.
	HeadFullName string
	// ChangedFiles is the number of files the PR changes (changed_files).
	ChangedFiles int
}

// ChangedFile is a file a pull request changes, its path relative to the
// repository root. PreviousFilename is its path before a rename, empty
// otherwise.
type ChangedFile struct {
	Filename         string
	PreviousFilename string
}

// Kind is what a webhook delivery is to badil.
type Kind int

// Kinds of webhook deliveries.
const (
	// KindOther is a delivery badil does not act on.
	KindOther Kind = iota
	// KindPullRequest is a pull request delivery; Event.PullRequest is set.
	KindPullRequest
	// KindPush is a push delivery; Event.Push is set.
	KindPush
)

// Event is a webhook delivery.
type Event struct {
	// Name is the delivery's event, the event label of the webhook
	// deliveries metric: ping, installation, installation_repositories,
	// pull_request, push or other.
	Name string
	Kind Kind
	// PullRequest is set when Kind is KindPullRequest.
	PullRequest PullRequestEvent
	// Push is set when Kind is KindPush.
	Push PushEvent
}

// PullRequestEvent is a pull request delivery's payload.
type PullRequestEvent struct {
	// Owner and Repo name the repository; FullName is owner/repo.
	Owner    string
	Repo     string
	FullName string
	Number   int
	// Action is the webhook action (opened, labeled, ...).
	Action string
	// Label is the added or removed label, on labeled and unlabeled.
	Label string
	// Labels holds the names of the PR's labels.
	Labels  []string
	HeadRef string
	HeadSHA string
	// Author is the PR author's login.
	Author string
}

// PushEvent is a push delivery's payload.
type PushEvent struct {
	// FullName is the pushed repository's owner/repo.
	FullName string
	// Ref is the pushed ref, e.g. refs/heads/main.
	Ref string
	// DefaultBranch is the repository's default branch.
	DefaultBranch string
	// After is the SHA the ref points to after the push.
	After string
	// Deleted is true when the push deleted the ref.
	Deleted bool
}
