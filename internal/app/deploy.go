package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

// ErrServiceNotInCatalog is a deploy of a service badil.yaml does not
// declare.
var ErrServiceNotInCatalog = errors.New("service not in catalog")

// CatalogReadError is a deploy whose catalog could not be read: Err is the
// read's error.
type CatalogReadError struct {
	Err error
}

func (e *CatalogReadError) Error() string {
	return e.Err.Error()
}

func (e *CatalogReadError) Unwrap() error {
	return e.Err
}

// ErrServiceRepoNotOnForge is a deploy of a service whose badil.yaml repo is
// not a repository URL of the forge.
var ErrServiceRepoNotOnForge = errors.New("service repo is not a repository of the forge")

// PullRequestReadError is a deploy whose pull request Owner/Repo#Number
// could not be read: Err is the forge's error, forge.ErrNotFound when the
// pull request does not exist.
type PullRequestReadError struct {
	Owner  string
	Repo   string
	Number int
	Err    error
}

func (e *PullRequestReadError) Error() string {
	return fmt.Sprintf("read pull request %s/%s#%d: %v", e.Owner, e.Repo, e.Number, e.Err)
}

func (e *PullRequestReadError) Unwrap() error {
	return e.Err
}

// DeployRequest is a manual deploy of a service's commit into the preview of
// the head branch of its repository's pull request PRNumber.
type DeployRequest struct {
	Service        string
	CommitSha      string
	PRNumber       int
	WorkflowRunURL string
}

// DeployOutcome is what a deploy did: the slug of the preview it targets,
// the catalog's preview cap, the deploy's result and its fan-out error
// (possibly joined, nil when every comment landed).
type DeployOutcome struct {
	Slug        string
	MaxPreviews int
	Deployed    *lifecycle.DeployResult
	FanOutErr   error
}

// Deploy deploys r through lifecycle.DeployPR, stamped with the current
// time. A manual deploy always writes: it refreshes updated-at (the TTL
// clock) and always returns a gitops commit. Its head branch, author and URL
// are those of the pull request r.PRNumber of the service's repository, read
// from the forge; the preview's slug is the slugified head branch, set in
// DeployOutcome.Slug on every return after that read. It is refused with
// lifecycle.ErrPreviewLimit when the preview does not exist and
// preview.maxPreviews previews already do, or with
// lifecycle.ErrNamespaceInUse when the preview does not exist and its
// namespace is not a preview's; a failed namespace check is a
// *lifecycle.NamespaceCheckError. A catalog that can't be read is a
// *CatalogReadError, a service it does not declare ErrServiceNotInCatalog, a
// service whose repo is not a repository of the forge
// ErrServiceRepoNotOnForge and a pull request that cannot be read a
// *PullRequestReadError; each returns a zero DeployOutcome. It does not log.
func (a *App) Deploy(ctx context.Context, r DeployRequest) (DeployOutcome, error) {
	services, err := a.store.FetchServiceCatalog(ctx)
	if err != nil {
		return DeployOutcome{}, &CatalogReadError{Err: err}
	}
	if !slices.Contains(services.Services, r.Service) {
		return DeployOutcome{}, ErrServiceNotInCatalog
	}

	owner, repo, ok := a.forge.ParseRepoURL(services.Catalog.Repos[r.Service])
	if !ok {
		return DeployOutcome{}, ErrServiceRepoNotOnForge
	}
	pr, err := a.forge.GetPullRequest(ctx, owner, repo, r.PRNumber)
	if err != nil {
		return DeployOutcome{}, &PullRequestReadError{Owner: owner, Repo: repo, Number: r.PRNumber, Err: err}
	}

	slug := preview.Slugify(pr.HeadRef)
	inputs := preview.DeployInputs{
		GitopsRepo:     "", // not used downstream; carry-over from earlier shape
		ServiceName:    r.Service,
		HeadRef:        pr.HeadRef,
		CommitSha:      r.CommitSha,
		PRAuthor:       pr.Author,
		PRURL:          pr.URL,
		PRNumber:       strconv.Itoa(r.PRNumber),
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		WorkflowRunURL: r.WorkflowRunURL,
	}

	result, fanErr, err := lifecycle.DeployPR(ctx, a.DeployAPI(), services, slug, inputs)
	return DeployOutcome{Slug: slug, MaxPreviews: services.MaxPreviews, Deployed: result, FanOutErr: fanErr}, err
}
