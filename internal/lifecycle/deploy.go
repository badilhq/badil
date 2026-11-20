package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// ErrPreviewLimit is the error of a deploy refused because creating its
// preview would exceed preview.maxPreviews. Callers test it with errors.Is.
var ErrPreviewLimit = errors.New("preview limit reached")

// DeployAPI is what DeployPR uses: the store, the forge, the Recorder and
// the comment fan-out.
type DeployAPI struct {
	Store    Applier
	Forge    forge.Forge
	Recorder Recorder
	FanOut   FanOutFunc
}

// DeployResult is the outcome of DeployPR: the deploy's write and the
// operation's commit, the remote head when the deploy changed nothing.
type DeployResult struct {
	store.WriteResult

	Commit store.Result
}

// DeployPR pins inputs.ServiceName at inputs.CommitSha in the preview slug,
// as one deploy operation, then posts the preview summary on every open PR
// of the preview. The deploy always writes the service, refreshing its
// updated-at; it is refused with ErrPreviewLimit when the preview does not
// exist and catalog.MaxPreviews previews already do, a check made on the
// tree the deploy writes. A deploy that changes nothing (the same inputs
// within the same second) commits nothing: its result and comments carry the
// remote head's commit. It returns the result, the fan-out error (possibly
// joined, nil when every comment landed) and the operation's error; on an
// error nothing is commented. It does not log.
func DeployPR(
	ctx context.Context,
	api DeployAPI,
	catalog *store.ServiceCatalog,
	slug string,
	inputs preview.DeployInputs,
) (*DeployResult, error, error) {
	var write *store.WriteResult
	limited := false
	res, err := api.Store.Apply(ctx, store.Op{
		Kind: store.OpDeploy,
		Change: func(tree *store.Tree) (store.Summary, error) {
			write, limited = nil, false
			if _, getErr := tree.GetPreview(slug); errors.Is(getErr, store.ErrNotFound) {
				var limitErr error
				if limited, limitErr = limitReached(tree, catalog.MaxPreviews); limitErr != nil || limited {
					return store.Summary{}, limitErr
				}
			}
			var deployErr error
			write, deployErr = tree.Deploy(slug, catalog, inputs.ServiceName, inputs.CommitSha, inputs, false)
			subject := fmt.Sprintf("chore(preview): deploy %s@%s to %s",
				inputs.ServiceName, store.ShortSHA(inputs.CommitSha), slug)
			return store.Summary{Subject: subject}, deployErr
		},
	})
	if err != nil {
		return nil, nil, err //nolint:nilnil // the operation error is the last result; no fan-out ran
	}
	if limited {
		//nolint:nilnil // the refusal is the last result; no fan-out ran
		return nil, nil, fmt.Errorf("%w: %d previews", ErrPreviewLimit, catalog.MaxPreviews)
	}
	if write.Created {
		api.Recorder.PreviewCreated()
	}
	result := &DeployResult{WriteResult: *write, Commit: res}
	summary := preview.BuildSummary(
		slug, write.Values, catalog.URLs, catalog.ArgoCDURL, write.Excluded, res.Subject, res.URL,
	)
	return result, api.FanOut(ctx, api.Forge, write.Values, summary), nil
}
