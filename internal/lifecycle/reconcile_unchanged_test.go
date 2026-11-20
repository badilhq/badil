package lifecycle_test

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// deployFirst runs ev on f once, then gives f fresh comments, calls and
// metrics, and returns a fresh log capture: what follows sees the preview as
// the first reconcile left it.
func deployFirst(t *testing.T, f *reconcileFake, ev lifecycle.Event) *logCapture {
	t.Helper()
	captureLogs(t)
	lifecycle.ReconcilePR(context.Background(), f.api(t), ev)
	f.calls, f.upserts, f.fanOuts = nil, nil, nil
	f.Metrics = metrics.New()
	return captureLogs(t)
}

// setPreview commits vals as the preview slug of f's remote.
func (f *reconcileFake) setPreview(t *testing.T, slug string, vals preview.PreviewValues) {
	t.Helper()
	f.g.remote.Set("previews/"+slug+"/values.yaml", marshal(t, vals))
}

func TestReconcile_UnchangedDeployWritesNothing(t *testing.T) {
	f := &reconcileFake{pr: fetchedPR("front", true, "preview")}
	ev := event("labeled", "front", "preview", "preview")
	capture := deployFirst(t, f, ev)
	before := len(f.g.pushed())
	lifecycle.ReconcilePR(context.Background(), f.api(t), ev)

	if len(f.g.pushed()) != before || len(f.fanOuts) != 0 || len(f.upserts) != 0 {
		t.Errorf("pushed = %q, fanOuts = %v, upserts = %v; want nothing new", f.g.subjects(), f.fanOuts, f.upserts)
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

func TestReconcile_ChangedDeployWrites(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "preview")}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("opened", "front", "", "preview"))

	if len(f.g.pushed()) != 1 || len(f.fanOuts) != 1 {
		t.Errorf("pushed = %q, fanOuts = %d; want one commit and one fan-out", f.g.subjects(), len(f.fanOuts))
	}
}

func TestReconcile_SomeServicesUnchanged(t *testing.T) {
	f := &reconcileFake{pr: fetchedPR("monorepo", true, "preview")}
	ev := event("labeled", "monorepo", "preview", "preview")
	capture := deployFirst(t, f, ev)
	// worker was deployed at an older commit since.
	vals := *f.g.file("feature-pricing")
	for i := range vals.Services {
		if vals.Services[i].Name == "worker" {
			vals.Services[i].CommitSha, vals.Services[i].ImageTag = "0000000old", "0000000old"
		}
	}
	f.setPreview(t, "feature-pricing", vals)
	lifecycle.ReconcilePR(context.Background(), f.api(t), ev)

	if got := deployedServices(capture); !slices.Equal(got, []string{"worker"}) {
		t.Errorf("written = %v, want [worker]", got)
	}
	if got := f.lastCommit(t).Body(); !slices.Equal(got, []string{"feature-pricing: pin worker@fe7c4ed"}) {
		t.Errorf("body = %q, want worker's pin only", got)
	}
	if logs := capture.find(slog.LevelInfo, "preview deploy unchanged"); len(logs) != 1 {
		t.Errorf("preview deploy unchanged logs = %v, want one", logs)
	}
	if logs := capture.find(slog.LevelInfo, "preview reconcile skipped"); len(logs) != 0 {
		t.Errorf("preview reconcile skipped logs = %v, want none", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="deployed"}`: 1,
	}, "badil_reconciles_total", "badil_reconcile_skips_total")
}

func TestReconcile_CleanupDecidesSkipUnchanged(t *testing.T) {
	cases := []struct {
		name          string
		file          *preview.PreviewValues
		raw           string
		skipUnchanged bool
	}{
		{name: "removed from another preview", file: onlyFront42()},
		{
			name: "unpinned from another preview",
			file: values(pinnedBy("api", "https://github.com/acme/monorepo/pull/7"), pinnedBy("front", front42)),
		},
		{name: "nothing removed", file: values(preview.ServiceEntry{Name: "front"}), skipUnchanged: true},
		{name: "teardown failed", raw: "services: [\n", skipUnchanged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// front is already deployed to pricing; the PR also is in its
			// branch preview.
			f := &reconcileFake{pr: fetchedPR("front", true, "preview:pricing")}
			ev := event("labeled", "front", "preview:pricing", "preview:pricing")
			capture := deployFirst(t, f, ev)
			if tc.file != nil {
				f.setPreview(t, "feature-pricing", *tc.file)
			} else {
				f.g.remote.Set("previews/feature-pricing/values.yaml", tc.raw)
			}
			before := len(f.g.pushed())
			lifecycle.ReconcilePR(context.Background(), f.api(t), ev)

			if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"feature-pricing"}) {
				t.Fatalf("teardowns = %v, want feature-pricing", got)
			}
			if tc.skipUnchanged {
				unchanged := capture.find(slog.LevelInfo, "preview deploy unchanged")
				if len(unchanged) != 1 || len(f.g.pushed()) != before || len(f.fanOuts) != 0 {
					t.Errorf("unchanged = %v, pushed = %q, fanOuts = %v; want one skipped deploy",
						unchanged, f.g.subjects(), f.fanOuts)
				}
				return
			}
			assertDeployCommentedLast(t, f, capture)
		})
	}
}

// assertDeployCommentedLast checks f deployed front to pricing although it
// was unchanged, and that its summary was the last comment.
func assertDeployCommentedLast(t *testing.T, f *reconcileFake, capture *logCapture) {
	t.Helper()
	if got := deployedLogged(capture); !slices.Equal(got, []string{"pricing/front"}) {
		t.Errorf("deployed = %v, want pricing/front", got)
	}
	last := len(f.fanOuts) - 1
	if last < 0 || f.calls[len(f.calls)-1] != "fanout" || !strings.HasPrefix(f.fanOuts[last], "## 🚀") {
		t.Errorf("fanOuts = %q, calls = %v; want the deploy summary last", f.fanOuts, f.calls)
	}
}
