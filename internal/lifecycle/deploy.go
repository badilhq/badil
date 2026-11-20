package lifecycle

import (
	"context"
	"fmt"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// DeployStore is the subset of store.GitHubStore used by DeployPR.
type DeployStore interface {
	WritePreviewWithRetry(
		ctx context.Context,
		slug string,
		catalog *store.ServiceCatalog,
		serviceName, commitSha string,
		inputs preview.DeployInputs,
		createMessage, updateMessage string,
		skipUnchanged bool,
	) (*store.WriteResult, error)
}

// DeployAPI is what DeployPR uses: the store, the forge and the comment
// fan-out.
type DeployAPI struct {
	Store  DeployStore
	Forge  forge.Forge
	FanOut FanOutFunc
}

// DeployPR pins inputs.ServiceName at inputs.CommitSha in the preview slug,
// then posts the preview summary on every open PR of the
// preview. It returns the write result, the fan-out error (possibly joined,
// nil when every comment landed) and the write error; on a write error
// nothing is commented. With skipUnchanged, a write that would change nothing
// but the service's updated-at is skipped (store.GitHubStore.WritePreviewWithRetry):
// the result is then Unchanged and nothing is commented. It does not log.
func DeployPR(
	ctx context.Context,
	api DeployAPI,
	catalog *store.ServiceCatalog,
	slug string,
	inputs preview.DeployInputs,
	skipUnchanged bool,
) (*store.WriteResult, error, error) {
	createMsg := fmt.Sprintf("chore(preview): create %s preview", slug)
	updateMsg := fmt.Sprintf("chore(preview): update %s in %s", inputs.ServiceName, slug)

	result, err := api.Store.WritePreviewWithRetry(
		ctx, slug, catalog, inputs.ServiceName, inputs.CommitSha, inputs, createMsg, updateMsg,
		skipUnchanged,
	)
	if err != nil {
		return nil, nil, err //nolint:nilnil // the write error is the last result; no fan-out ran
	}
	if result.Unchanged {
		return result, nil, nil
	}

	summary := preview.BuildSummary(
		slug, result.Values, catalog.URLs, catalog.ArgoCDURL, result.Excluded, result.CommitMessage, result.CommitURL,
	)
	return result, api.FanOut(ctx, api.Forge, result.Values, summary), nil
}
