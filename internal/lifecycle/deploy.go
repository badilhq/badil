package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/badilhq/badil/internal/catalog"
	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/preview"
)

// ErrPreviewLimit is the error of a deploy refused because creating its
// preview would exceed preview.maxPreviews. Callers test it with errors.Is.
var ErrPreviewLimit = errors.New("preview limit reached")

// ErrNamespaceInUse is the error of a deploy refused because the namespace
// of its new preview exists and is not a preview namespace for its slug.
// Callers test it with errors.Is.
var ErrNamespaceInUse = errors.New("namespace in use")

// NamespaceChecker reads the namespace of a new preview.
type NamespaceChecker interface {
	// Foreign reports whether the namespace name exists and is not a preview
	// namespace for the slug name: one labelled badil.dev/preview=<name>. A
	// failed read is returned.
	Foreign(ctx context.Context, name string) (bool, error)
}

// NamespaceCheckError is the error of a failed namespace check of a new
// preview (NamespaceChecker.Foreign).
type NamespaceCheckError struct {
	Err error
}

func (e *NamespaceCheckError) Error() string { return "namespace check failed: " + e.Err.Error() }

func (e *NamespaceCheckError) Unwrap() error { return e.Err }

// CapCheckError is the error of a read the preview cap check of DeployPR
// makes on the tree it writes: Op is "read preview" when the preview could
// not be read, "list previews" when the previews could not be listed.
type CapCheckError struct {
	Op  string
	Err error
}

func (e *CapCheckError) Error() string { return e.Op + ": " + e.Err.Error() }

func (e *CapCheckError) Unwrap() error { return e.Err }

// DeployAPI is what DeployPR uses: the store, the forge, the Recorder, the
// comment fan-out and the namespace check of a new preview (nil checks
// nothing).
type DeployAPI struct {
	Store      Applier
	Forge      forge.Forge
	Recorder   Recorder
	FanOut     FanOutFunc
	Namespaces NamespaceChecker
}

// DeployResult is the outcome of DeployPR: the operation's commit, the
// remote head when the deploy changed nothing.
type DeployResult struct {
	Commit Result
}

// DeployPR pins inputs.ServiceName at inputs.CommitSha in the preview slug,
// as one deploy operation, then posts the preview summary on every open PR
// of the preview. The deploy always writes the service, refreshing its
// updated-at; it is refused with ErrPreviewLimit when the preview does not
// exist and catalog.MaxPreviews previews already do, a check made on the
// tree the deploy writes, whose read errors are a *CapCheckError. Otherwise,
// when the preview does not exist, it is refused with ErrNamespaceInUse when
// its namespace exists and is not a preview namespace for slug
// (api.Namespaces); a failed check is a *NamespaceCheckError. A deploy
// that changes nothing (the same inputs
// within the same second) commits nothing: its result and comments carry the
// remote head's commit. It returns the result, the fan-out error (possibly
// joined, nil when every comment landed) and the operation's error; on an
// error nothing is commented. It does not log.
func DeployPR(
	ctx context.Context,
	api DeployAPI,
	catalog *catalog.Catalog,
	slug string,
	inputs preview.DeployInputs,
) (*DeployResult, error, error) {
	var write *WriteResult
	limited, inUse := false, false
	foreign := foreignFunc(ctx, api.Namespaces)
	res, err := api.Store.Apply(ctx, Op{
		Kind: OpDeploy,
		Change: func(tree Tree) (Summary, error) {
			write, limited, inUse = nil, false, false
			var checkErr error
			limited, inUse, checkErr = refusal(tree, catalog.MaxPreviews, slug, api.Namespaces != nil, foreign)
			if checkErr != nil || limited || inUse {
				return Summary{}, checkErr
			}
			var deployErr error
			write, deployErr = deployPreview(tree, slug, catalog, inputs.ServiceName, inputs.CommitSha, inputs, false)
			subject := fmt.Sprintf("chore(preview): deploy %s@%s to %s",
				inputs.ServiceName, preview.ShortSHA(inputs.CommitSha), slug)
			return Summary{Subject: subject}, deployErr
		},
	})
	if err != nil {
		return nil, nil, err //nolint:nilnil // the operation error is the last result; no fan-out ran
	}
	if limited {
		//nolint:nilnil // the refusal is the last result; no fan-out ran
		return nil, nil, fmt.Errorf("%w: %d previews", ErrPreviewLimit, catalog.MaxPreviews)
	}
	if inUse {
		//nolint:nilnil // the refusal is the last result; no fan-out ran
		return nil, nil, fmt.Errorf("%w: %s", ErrNamespaceInUse, slug)
	}
	if write.Created {
		api.Recorder.PreviewCreated()
	}
	result := &DeployResult{Commit: res}
	summary := preview.BuildSummary(
		api.Forge.ParsePRURL, slug, write.Values, catalog.URLs, catalog.ArgoCDURL, write.Excluded, res.Subject, res.URL,
	)
	return result, api.FanOut(ctx, api.Forge, write.Values, summary), nil
}

// refusal reports whether the deploy of the preview slug in tree is refused
// because the preview does not exist and either maxPreviews previews already
// do (limited) or foreign reports its namespace foreign (inUse). It reads the
// preview once, and nothing when maxPreviews is 0 (no cap) and check is
// false. A read error is a *CapCheckError; a failed check is foreign's error.
func refusal(
	tree Tree, maxPreviews int, slug string, check bool, foreign func(string) (bool, error),
) (bool, bool, error) {
	if maxPreviews <= 0 && !check {
		return false, false, nil
	}
	_, err := tree.GetPreview(slug)
	if err == nil {
		return false, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return false, false, &CapCheckError{Op: "read preview", Err: err}
	}
	limited, err := limitReached(tree, maxPreviews)
	if err != nil {
		return false, false, &CapCheckError{Op: "list previews", Err: err}
	}
	if limited {
		return true, false, nil
	}
	inUse, err := foreign(slug)
	return false, inUse, err
}
