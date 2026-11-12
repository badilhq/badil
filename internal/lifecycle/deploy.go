package lifecycle

import (
	"context"
	"fmt"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/preview"
)

// DeployAPI is the subset of gitops.Client used by DeployPR.
type DeployAPI interface {
	WritePreviewWithRetry(
		ctx context.Context,
		slug string,
		catalog []string,
		urls preview.URLConfig,
		serviceName, commitSha string,
		inputs preview.DeployInputs,
		createMessage, updateMessage string,
		skipUnchanged bool,
	) (*gitops.WriteResult, error)
	FanOutPRComments(ctx context.Context, vals preview.PreviewValues, body string) error
}

// DeployPR pins inputs.ServiceName at inputs.CommitSha in the preview slug,
// then posts the preview summary on every open PR of the
// preview. It returns the write result, the fan-out error (possibly joined,
// nil when every comment landed) and the write error; on a write error
// nothing is commented. With skipUnchanged, a write that would change nothing
// but the service's updated-at is skipped (gitops.Client.WritePreviewWithRetry):
// the result is then Unchanged and nothing is commented. It does not log.
func DeployPR(
	ctx context.Context,
	api DeployAPI,
	catalog *gitops.ServiceCatalog,
	slug string,
	inputs preview.DeployInputs,
	skipUnchanged bool,
) (*gitops.WriteResult, error, error) {
	createMsg := fmt.Sprintf("chore(preview): create %s preview", slug)
	updateMsg := fmt.Sprintf("chore(preview): update %s in %s", inputs.ServiceName, slug)

	result, err := api.WritePreviewWithRetry(
		ctx, slug, catalog.Services, catalog.URLs, inputs.ServiceName, inputs.CommitSha, inputs, createMsg, updateMsg,
		skipUnchanged,
	)
	if err != nil {
		return nil, nil, err //nolint:nilnil // the write error is the last result; no fan-out ran
	}
	if result.Unchanged {
		return result, nil, nil
	}

	summary := preview.BuildSummary(
		slug, result.Values, catalog.URLs, catalog.Branches, result.CommitMessage, result.CommitURL,
	)
	return result, api.FanOutPRComments(ctx, result.Values, summary), nil
}
