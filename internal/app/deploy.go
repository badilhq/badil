package app

import (
	"context"
	"errors"
	"slices"
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

// DeployRequest is a manual deploy of a service's commit into the preview of
// its PR's head branch.
type DeployRequest struct {
	Service        string
	HeadRef        string
	CommitSha      string
	PRAuthor       string
	PRURL          string
	PRNumber       string
	WorkflowRunURL string
}

// Slug is the slug of the preview r deploys into.
func (r DeployRequest) Slug() string {
	return preview.Slugify(r.HeadRef)
}

// DeployOutcome is what a deploy did: the catalog's preview cap, the
// deploy's result and its fan-out error (possibly joined, nil when every
// comment landed).
type DeployOutcome struct {
	MaxPreviews int
	Deployed    *lifecycle.DeployResult
	FanOutErr   error
}

// Deploy deploys r through lifecycle.DeployPR, stamped with the current
// time. A manual deploy always writes: it refreshes updated-at (the TTL
// clock) and always returns a gitops commit. It is refused with
// lifecycle.ErrPreviewLimit when the preview does not exist and
// preview.maxPreviews previews already do, or with
// lifecycle.ErrNamespaceInUse when the preview does not exist and its
// namespace is not a preview's; a failed namespace check is a
// *lifecycle.NamespaceCheckError. A catalog that can't be read is a
// *CatalogReadError, a service it does not declare ErrServiceNotInCatalog;
// both return a zero DeployOutcome. It does not log.
func (a *App) Deploy(ctx context.Context, r DeployRequest) (DeployOutcome, error) {
	services, err := a.store.FetchServiceCatalog(ctx)
	if err != nil {
		return DeployOutcome{}, &CatalogReadError{Err: err}
	}
	if !slices.Contains(services.Services, r.Service) {
		return DeployOutcome{}, ErrServiceNotInCatalog
	}

	inputs := preview.DeployInputs{
		GitopsRepo:     "", // not used downstream; carry-over from earlier shape
		ServiceName:    r.Service,
		HeadRef:        r.HeadRef,
		CommitSha:      r.CommitSha,
		PRAuthor:       r.PRAuthor,
		PRURL:          r.PRURL,
		PRNumber:       r.PRNumber,
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		WorkflowRunURL: r.WorkflowRunURL,
	}

	result, fanErr, err := lifecycle.DeployPR(ctx, a.DeployAPI(), services, r.Slug(), inputs)
	return DeployOutcome{MaxPreviews: services.MaxPreviews, Deployed: result, FanOutErr: fanErr}, err
}
