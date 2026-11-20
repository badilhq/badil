package lifecycle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// TestDeployPR_NoOpStillComments deploys the same inputs twice: the second
// deploy changes nothing, so it commits nothing, and still posts the summary,
// linking to the remote head.
func TestDeployPR_NoOpStillComments(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{Metrics: metrics.New()}
	r := f.api(t)
	api := lifecycle.DeployAPI{Store: r.Store, Forge: r.Forge, Recorder: r.Recorder, FanOut: r.FanOut}
	catalog, err := f.FetchServiceCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	inputs := preview.DeployInputs{
		ServiceName: "front", CommitSha: fetchedSHA, HeadRef: "feature/pricing", PRAuthor: "octocat",
		PRURL: front42, PRNumber: "42", Timestamp: "2026-11-20T10:00:00Z",
	}
	for i := range 2 {
		res, fanErr, deployErr := lifecycle.DeployPR(context.Background(), api, catalog, "feature-pricing", inputs)
		if deployErr != nil || fanErr != nil {
			t.Fatalf("deploy %d: %v, fan-out %v", i, deployErr, fanErr)
		}
		c := f.lastCommit(t)
		if res.Commit.NoOp != (i == 1) || res.Commit.URL != commitURL(c) {
			t.Errorf("deploy %d: commit = %+v, want no-op %v on %s", i, res.Commit, i == 1, c.Hash)
		}
		if len(f.fanOuts) != i+1 || !strings.HasSuffix(f.fanOuts[i], link(c)) {
			t.Errorf("deploy %d: fanOuts = %q, want %d, the last linking %s", i, f.fanOuts, i+1, link(c))
		}
	}
	f.assertOneCommit(t, "chore(preview): deploy front@fe7c4ed to feature-pricing",
		"feature-pricing: pin front@fe7c4ed")
}
