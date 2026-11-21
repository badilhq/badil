// Package github implements forge.Forge over the GitHub API, authenticated as
// the GitHub App installation.
package github

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
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
		return fmt.Errorf("%w: %v", forge.ErrNotFound, err)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %v", forge.ErrUnauthorized, err)
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

// labelNames returns the names of labels.
func labelNames(labels []*gh.Label) []string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.GetName())
	}
	return names
}
