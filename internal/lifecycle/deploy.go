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

// CapCheckError is the error of a read the preview cap check of DeployPR
// makes on the tree it writes: Op is "read preview" when the preview could
// not be read, "list previews" when the previews could not be listed.
type CapCheckError struct {
	Op  string
	Err error
}

func (e *CapCheckError) Error() string { return e.Op + ": " + e.Err.Error() }

func (e *CapCheckError) Unwrap() error { return e.Err }

// DeployAPI is what DeployPR uses: the store, the forge, the Recorder and
// the comment fan-out.
type DeployAPI struct {
	Store    Applier
	Forge    forge.Forge
	Recorder Recorder
	FanOut   FanOutFunc
}

// DeployResult is the outcome of DeployPR: the operation's commit, the
// remote head when the deploy changed nothing.
type DeployResult struct {
	Commit store.Result
}

// DeployPR pins inputs.ServiceName at inputs.CommitSha in the preview slug,
// as one deploy operation, then posts the preview summary on every open PR
// of the preview. The deploy always writes the service, refreshing its
// updated-at; it is refused with ErrPreviewLimit when the preview does not
// exist and catalog.MaxPreviews previews already do, a check made on the
// tree the deploy writes, whose read errors are a *CapCheckError. A deploy
// that changes nothing (the same inputs
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
			var checkErr error
			if limited, checkErr = overLimit(tree, catalog.MaxPreviews, slug); checkErr != nil || limited {
				return store.Summary{}, checkErr
			}
			var deployErr error
			write, deployErr = tree.Deploy(slug, catalog, inputs.ServiceName, inputs.CommitSha, inputs, false)
			subject := fmt.Sprintf("chore(preview): deploy %s@%s to %s",
				inputs.ServiceName, preview.ShortSHA(inputs.CommitSha), slug)
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
	result := &DeployResult{Commit: res}
	summary := preview.BuildSummary(
		slug, write.Values, catalog.URLs, catalog.ArgoCDURL, write.Excluded, res.Subject, res.URL,
	)
	return result, api.FanOut(ctx, api.Forge, write.Values, summary), nil
}

// overLimit reports whether the preview slug does not exist in tree and
// maxPreviews previews already do. It reads nothing when maxPreviews is 0 (no
// cap). A read error is a *CapCheckError.
func overLimit(tree *store.Tree, maxPreviews int, slug string) (bool, error) {
	if maxPreviews <= 0 {
		return false, nil
	}
	_, err := tree.GetPreview(slug)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, &CapCheckError{Op: "read preview", Err: err}
	}
	limited, err := limitReached(tree, maxPreviews)
	if err != nil {
		return false, &CapCheckError{Op: "list previews", Err: err}
	}
	return limited, nil
}
