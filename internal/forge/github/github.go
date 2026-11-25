// Package github implements forge.Forge and forge.WebhookParser over the
// GitHub API, authenticated as the GitHub App installation.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/forge"
	ghapp "github.com/badilhq/badil/internal/github"
)

// prCommentMarker is the HTML comment used as a sentinel for find-or-create
// semantics on PR comments.
const prCommentMarker = "<!-- badil:preview -->"

// perPage is the page size of the list calls, GitHub's maximum.
const perPage = 100

// Forge is the GitHub forge: pull requests and comments through client,
// webhook deliveries signed with app's secret from the installation
// installationID, and the repo and pull request URLs of its web URL.
type Forge struct {
	gh             *gh.Client
	app            *ghapp.App
	installationID int64
	// webURL is GITHUB_URL as the canonical base ghapp.ParseWebURL returns.
	webURL string
	// prURL matches a pull request URL of webURL: owner, repo and number.
	prURL *regexp.Regexp
}

var _ forge.Forge = (*Forge)(nil)

var _ forge.WebhookParser = (*Forge)(nil)

// New returns the GitHub forge over client. Webhook deliveries are validated
// with app and accepted only from the installation installationID. Repo and
// pull request URLs are those of webURL (GITHUB_URL), which must pass
// ghapp.ParseWebURL.
func New(client *gh.Client, app *ghapp.App, installationID int64, webURL string) (*Forge, error) {
	web, err := ghapp.ParseWebURL(webURL)
	if err != nil {
		return nil, fmt.Errorf("GITHUB_URL %w", err)
	}
	return &Forge{
		gh:             client,
		app:            app,
		installationID: installationID,
		webURL:         web,
		prURL:          regexp.MustCompile(`^` + regexp.QuoteMeta(web) + `/([^/]+)/([^/]+)/pull/(\d+)`),
	}, nil
}

// classifyHTTPError translates a go-github response into the forge sentinel
// errors for status codes the caller cares about. Other errors pass through
// unchanged.
func classifyHTTPError(resp *gh.Response, err error) error {
	if resp == nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", forge.ErrNotFound, err)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %w", forge.ErrUnauthorized, err)
	}
	return err
}

// GetPullRequest reads owner/repo#number. Lookup failures (including 404 and
// 403) are returned as errors.
func (f *Forge) GetPullRequest(ctx context.Context, owner, repo string, number int) (*forge.PullRequest, error) {
	pr, resp, err := f.gh.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, classifyHTTPError(resp, err)
	}
	out := pullRequest(pr)
	return &out, nil
}

// ListOpenPullRequests lists the open pull requests of owner/repo, reading
// every page (100 each). Their ChangedFiles is 0: a listed pull request has
// no changed_files. A failure on any page is returned as an error.
func (f *Forge) ListOpenPullRequests(ctx context.Context, owner, repo string) ([]forge.PullRequest, error) {
	opts := &gh.PullRequestListOptions{State: "open", ListOptions: gh.ListOptions{PerPage: perPage}}
	var prs []forge.PullRequest
	for {
		page, resp, err := f.gh.PullRequests.List(ctx, owner, repo, opts)
		if err != nil {
			return nil, classifyHTTPError(resp, err)
		}
		for _, pr := range page {
			prs = append(prs, pullRequest(pr))
		}
		if resp == nil || resp.NextPage == 0 {
			return prs, nil
		}
		opts.Page = resp.NextPage
	}
}

// pullRequest converts a go-github pull request.
func pullRequest(pr *gh.PullRequest) forge.PullRequest {
	return forge.PullRequest{
		Open:         pr.GetState() == "open",
		HeadRef:      pr.GetHead().GetRef(),
		HeadSHA:      pr.GetHead().GetSHA(),
		Author:       pr.GetUser().GetLogin(),
		URL:          pr.GetHTMLURL(),
		Labels:       labelNames(pr.Labels),
		FullName:     pr.GetBase().GetRepo().GetFullName(),
		HeadFullName: pr.GetHead().GetRepo().GetFullName(),
		Number:       pr.GetNumber(),
		ChangedFiles: pr.GetChangedFiles(),
	}
}

// ListChangedFiles lists the files owner/repo#number changes, reading every
// page (100 files each). GitHub lists at most forge.MaxListedFiles. A failure
// on any page is returned as an error.
func (f *Forge) ListChangedFiles(ctx context.Context, owner, repo string, number int) ([]forge.ChangedFile, error) {
	opts := &gh.ListOptions{PerPage: perPage}
	var files []forge.ChangedFile
	for {
		page, resp, err := f.gh.PullRequests.ListFiles(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, classifyHTTPError(resp, err)
		}
		for _, file := range page {
			files = append(files, forge.ChangedFile{
				Filename:         file.GetFilename(),
				PreviousFilename: file.GetPreviousFilename(),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			return files, nil
		}
		opts.Page = resp.NextPage
	}
}

// UpsertComment posts body as badil's comment on owner/repo#number, prefixed
// with prCommentMarker: the existing marked comment, searched across every
// page, is updated, or a new one is created.
func (f *Forge) UpsertComment(ctx context.Context, owner, repo string, number int, body string) error {
	body = prCommentMarker + "\n" + body
	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: perPage}}
	for {
		comments, resp, err := f.gh.Issues.ListComments(ctx, owner, repo, number, opts)
		if err != nil {
			return classifyHTTPError(resp, err)
		}
		for _, comment := range comments {
			if comment.Body == nil || !strings.Contains(*comment.Body, prCommentMarker) {
				continue
			}
			_, editResp, editErr := f.gh.Issues.EditComment(
				ctx,
				owner,
				repo,
				comment.GetID(),
				&gh.IssueComment{Body: gh.Ptr(body)},
			)
			if editErr != nil {
				return classifyHTTPError(editResp, editErr)
			}
			return nil
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	_, createResp, createErr := f.gh.Issues.CreateComment(
		ctx,
		owner,
		repo,
		number,
		&gh.IssueComment{Body: gh.Ptr(body)},
	)
	if createErr != nil {
		return classifyHTTPError(createResp, createErr)
	}
	return nil
}

// labeledEvent is the issue event of an added label.
const labeledEvent = "labeled"

// LabelledBy returns who last added one of labels to owner/repo#number: the
// actor and label name of the last labeled event, in listing order (oldest
// first), whose label name equals one of labels, reading every page (100
// events each). The bool is false when there is none. A failure on any page
// is returned as an error.
func (f *Forge) LabelledBy(
	ctx context.Context, owner, repo string, number int, labels []string,
) (string, string, bool, error) {
	opts := &gh.ListOptions{PerPage: perPage}
	var login, label string
	found := false
	for {
		events, resp, err := f.gh.Issues.ListIssueEvents(ctx, owner, repo, number, opts)
		if err != nil {
			return "", "", false, classifyHTTPError(resp, err)
		}
		for _, ev := range events {
			if ev.GetEvent() != labeledEvent || !slices.Contains(labels, ev.GetLabel().GetName()) {
				continue
			}
			login, label, found = ev.GetActor().GetLogin(), ev.GetLabel().GetName(), true
		}
		if resp == nil || resp.NextPage == 0 {
			return login, label, found, nil
		}
		opts.Page = resp.NextPage
	}
}

// CanWrite reports whether login has write access to owner/repo: the
// user.permissions.push of its permission, true for the write, maintain and
// admin roles. A 404 (a login that is not a user) is no write access and no
// error.
func (f *Forge) CanWrite(ctx context.Context, owner, repo, login string) (bool, error) {
	perm, resp, err := f.gh.Repositories.GetPermissionLevel(ctx, owner, repo, login)
	if err != nil {
		err = classifyHTTPError(resp, err)
		if errors.Is(err, forge.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return perm.GetUser().GetPermissions()["push"], nil
}

// labelNames returns the names of labels.
func labelNames(labels []*gh.Label) []string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.GetName())
	}
	return names
}
