package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
)

func TestReconcile_UnchangedDeployWritesNothing(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics:   metrics.New(),
		pr:        fetchedPR("front", true, "preview"),
		unchanged: map[string]bool{"front": true},
	}
	lifecycle.ReconcilePR(context.Background(), f, event("labeled", "front", "preview", "preview"))

	if len(f.writes) != 0 || len(f.fanOuts) != 0 || len(f.upserts) != 0 {
		t.Errorf("writes = %v, fanOuts = %v, upserts = %v; want none", f.writes, f.fanOuts, f.upserts)
	}
	if len(f.skipped) != 1 || !f.skipped[0].skipUnchanged || f.skipped[0].commitSha != fetchedSHA {
		t.Errorf("skipped = %+v, want front with skipUnchanged at the head SHA", f.skipped)
	}
	unchanged := capture.find(slog.LevelInfo, "preview deploy unchanged")
	if len(unchanged) != 1 || unchanged[0]["slug"].String() != "feature-pricing" ||
		unchanged[0]["service"].String() != "front" || unchanged[0]["repo"].String() != "acme/front" ||
		unchanged[0]["pr_number"].Int64() != 42 || unchanged[0]["commit_sha"].String() != fetchedSHA {
		t.Errorf("preview deploy unchanged logs = %v", unchanged)
	}
	if _, ok := unchanged[0]["gitops_commit_url"]; ok {
		t.Errorf("preview deploy unchanged has gitops_commit_url: %v", unchanged[0])
	}
	if logs := capture.find(slog.LevelInfo, "preview deploy"); len(logs) != 0 {
		t.Errorf("preview deploy logs = %v, want none", logs)
	}
	skipped := capture.find(slog.LevelInfo, "preview reconcile skipped")
	if len(skipped) != 1 || skipped[0]["reason"].String() != "already deployed" ||
		skipped[0]["repo"].String() != "acme/front" || skipped[0]["pr_number"].Int64() != 42 ||
		skipped[0]["action"].String() != "labeled" {
		t.Errorf("preview reconcile skipped logs = %v", skipped)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="skipped"}`:              1,
		`badil_reconcile_skips_total{reason="already deployed"}`: 1,
	}, "badil_reconciles_total", "badil_reconcile_skips_total")
}

func TestReconcile_ChangedDeployWritesWithSkipUnchanged(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "preview")}
	lifecycle.ReconcilePR(context.Background(), f, event("opened", "front", "", "preview"))

	if len(f.writes) != 1 || !f.writes[0].skipUnchanged || len(f.fanOuts) != 1 {
		t.Errorf("writes = %+v, fanOuts = %d; want one write with skipUnchanged and one fan-out",
			f.writes, len(f.fanOuts))
	}
}

func TestReconcile_SomeServicesUnchanged(t *testing.T) {
	cases := []struct {
		name     string
		writeErr map[string]error
		outcome  string
		written  []string
	}{
		{name: "one written", outcome: "deployed", written: []string{"worker"}},
		{name: "one failed", writeErr: map[string]error{"worker": errors.New("boom")}, outcome: "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{
				Metrics:   metrics.New(),
				pr:        fetchedPR("monorepo", true, "preview"),
				unchanged: map[string]bool{"api": true},
				writeErr:  tc.writeErr,
			}
			lifecycle.ReconcilePR(context.Background(), f, event("labeled", "monorepo", "preview", "preview"))

			if got := services(f.writes); !slices.Equal(got, tc.written) {
				t.Errorf("written = %v, want %v", got, tc.written)
			}
			if logs := capture.find(slog.LevelInfo, "preview deploy unchanged"); len(logs) != 1 {
				t.Errorf("preview deploy unchanged logs = %v, want one", logs)
			}
			if logs := capture.find(slog.LevelInfo, "preview reconcile skipped"); len(logs) != 0 {
				t.Errorf("preview reconcile skipped logs = %v, want none", logs)
			}
			assertMetrics(t, f.Metrics, map[string]float64{
				`badil_reconciles_total{outcome="` + tc.outcome + `"}`: 1,
			}, "badil_reconciles_total", "badil_reconcile_skips_total")
		})
	}
}

func TestReconcile_CleanupDecidesSkipUnchanged(t *testing.T) {
	cases := []struct {
		name          string
		teardown      *gitops.TeardownResult
		teardownErr   error
		skipUnchanged bool
	}{
		{name: "removed from another preview", teardown: removedFront("feature-pricing")},
		{
			name:     "unpinned from another preview",
			teardown: &gitops.TeardownResult{Removed: []string{"front"}, CommitURL: sweepCommitURL},
		},
		{name: "nothing removed", teardown: &gitops.TeardownResult{}, skipUnchanged: true},
		{name: "teardown failed", teardownErr: errors.New("boom"), skipUnchanged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			// The PR moves from its branch preview to pricing.
			f := &reconcileFake{
				pr:          fetchedPR("front", true, "preview:pricing"),
				teardown:    tc.teardown,
				teardownErr: tc.teardownErr,
				unchanged:   map[string]bool{"front": true},
			}
			lifecycle.ReconcilePR(
				context.Background(),
				f,
				event("labeled", "front", "preview:pricing", "preview:pricing"),
			)

			if len(f.teardowns) != 1 || f.teardowns[0].slug != "feature-pricing" {
				t.Fatalf("teardowns = %+v, want feature-pricing", f.teardowns)
			}
			if tc.skipUnchanged {
				if len(f.skipped) != 1 || len(f.writes) != 0 || len(f.fanOuts) != 0 {
					t.Errorf("skipped = %v, writes = %v, fanOuts = %v; want one skipped deploy",
						f.skipped, f.writes, f.fanOuts)
				}
				return
			}
			assertDeployCommentedLast(t, f)
		})
	}
}

// assertDeployCommentedLast checks f wrote one deploy to pricing without
// skipUnchanged, and that its summary was the last comment.
func assertDeployCommentedLast(t *testing.T, f *reconcileFake) {
	t.Helper()
	if len(f.writes) != 1 || f.writes[0].skipUnchanged || f.writes[0].slug != "pricing" {
		t.Errorf("writes = %+v, want one to pricing without skipUnchanged", f.writes)
	}
	last := len(f.fanOuts) - 1
	if last < 0 || f.calls[len(f.calls)-1] != "fanout" || !strings.HasPrefix(f.fanOuts[last], "## 🚀") {
		t.Errorf("fanOuts = %q, calls = %v; want the deploy summary last", f.fanOuts, f.calls)
	}
}
