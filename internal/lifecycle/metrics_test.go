package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store/storetest"
)

// assertMetrics fails t unless the samples of m's metrics named names are
// exactly want, keyed by name and labels as exposed.
func assertMetrics(t *testing.T, m *metrics.Metrics, want map[string]float64, names ...string) {
	t.Helper()
	// headers holds the HELP and TYPE lines of the metrics these tests assert.
	headers := map[string]string{
		"badil_reconciles_total": "# HELP badil_reconciles_total Pull request reconciles, by outcome.\n" +
			"# TYPE badil_reconciles_total counter\n",
		"badil_reconcile_skips_total": "# HELP badil_reconcile_skips_total Skipped pull request reconciles, by reason.\n" +
			"# TYPE badil_reconcile_skips_total counter\n",
		"badil_teardowns_total": "# HELP badil_teardowns_total Preview teardowns of a pull request, by trigger and result.\n" +
			"# TYPE badil_teardowns_total counter\n",
		"badil_previews": "# HELP badil_previews Previews in the gitops repo: listed at startup, " +
			"counted on each create and delete by this server, and reset by each in-server cleanup sweep.\n" +
			"# TYPE badil_previews gauge\n",
		"badil_sweep_previews_deleted_total": "# HELP badil_sweep_previews_deleted_total " +
			"Previews deleted by the cleanup sweep, by rule.\n# TYPE badil_sweep_previews_deleted_total counter\n",
		"badil_previews_synced_total": "# HELP badil_previews_synced_total " +
			"Previews rewritten to match badil.yaml, by a sweep, a push or /v1/sync.\n" +
			"# TYPE badil_previews_synced_total counter\n",
		"badil_catchup_reconciles_total": "# HELP badil_catchup_reconciles_total " +
			"Pull request reconciles started by catch-up.\n# TYPE badil_catchup_reconciles_total counter\n",
	}
	var expected strings.Builder
	matched := 0
	for _, name := range names {
		var lines strings.Builder
		for key, v := range want {
			if key == name || strings.HasPrefix(key, name+"{") {
				fmt.Fprintf(&lines, "%s %v\n", key, v)
				matched++
			}
		}
		if lines.Len() > 0 {
			expected.WriteString(headers[name] + lines.String())
		}
	}
	if matched != len(want) {
		t.Fatalf("want %v names a metric outside %v", want, names)
	}
	if err := testutil.GatherAndCompare(m.Gatherer(), strings.NewReader(expected.String()), names...); err != nil {
		t.Error(err)
	}
}

func TestReconcileMetrics_Outcomes(t *testing.T) {
	held := values(pin("front", "https://github.com/acme/front/pull/7", recent))
	fork := fetchedPR("front", true, "preview")
	fork.HeadFullName = "octocat/front"
	branchPreview := map[string]*preview.Values{"feature-pricing": onlyFront42()}
	cases := []struct {
		name string
		f    *reconcileFake
		ev   lifecycle.Event
		want map[string]float64
	}{
		{
			name: "deployed",
			f:    &reconcileFake{pr: fetchedPR("front", true, "preview")},
			ev:   event("labeled", "front", "preview", "preview"),
			want: map[string]float64{
				`badil_reconciles_total{outcome="deployed"}`: 1,
			},
		},
		{
			name: "payload skip",
			f:    &reconcileFake{},
			ev:   event("labeled", "front", "bug", "bug"),
			want: map[string]float64{
				`badil_reconciles_total{outcome="skipped"}`:         1,
				`badil_reconcile_skips_total{reason="other label"}`: 1,
			},
		},
		{
			name: "fork skip",
			f:    &reconcileFake{pr: fork},
			ev:   event("labeled", "front", "preview", "preview"),
			want: map[string]float64{
				`badil_reconciles_total{outcome="skipped"}`:     1,
				`badil_reconcile_skips_total{reason="fork pr"}`: 1,
			},
		},
		{
			name: "held skip after cleanup",
			f: &reconcileFake{
				pr: fetchedPR("front", true, "preview:pricing"),
				previews: map[string]*preview.Values{
					"pricing": held, "feature-pricing": onlyFront42(),
				},
			},
			ev: event("labeled", "front", "preview:pricing", "preview:pricing"),
			want: map[string]float64{
				`badil_reconciles_total{outcome="skipped"}`:                        1,
				`badil_reconcile_skips_total{reason="service held by another pr"}`: 1,
				`badil_teardowns_total{result="deleted",trigger="webhook"}`:        1,
			},
		},
		{
			name: "conflict skip",
			f:    &reconcileFake{pr: fetchedPR("front", true, "preview:a", "preview:b")},
			ev:   event("labeled", "front", "preview:b", "preview:b"),
			want: map[string]float64{
				`badil_reconciles_total{outcome="skipped"}`:                        1,
				`badil_reconcile_skips_total{reason="conflicting preview labels"}`: 1,
				`badil_teardowns_total{result="noop",trigger="webhook"}`:           3,
			},
		},
		{
			name: "catalog failure",
			f:    &reconcileFake{catalogErr: errors.New("boom")},
			ev:   event("labeled", "front", "preview", "preview"),
			want: map[string]float64{`badil_reconciles_total{outcome="failed"}`: 1},
		},
		{
			name: "preview read failure",
			f: &reconcileFake{
				pr:  fetchedPR("front", true, "preview:pricing"),
				raw: map[string]string{"previews/pricing/values.yaml": "services: [\n"},
			},
			ev: event("labeled", "front", "preview:pricing", "preview:pricing"),
			want: map[string]float64{
				`badil_reconciles_total{outcome="failed"}`:               1,
				`badil_teardowns_total{result="noop",trigger="webhook"}`: 1,
			},
		},
		{
			name: "every write failed",
			f: &reconcileFake{
				pr:         fetchedPR("front", true, "preview"),
				beforePush: func(*storetest.Remote) error { return errors.New("boom") },
			},
			ev:   event("labeled", "front", "preview", "preview"),
			want: map[string]float64{`badil_reconciles_total{outcome="failed"}`: 1},
		},
		{
			name: "torn down",
			f:    &reconcileFake{pr: fetchedPR("front", true, "bug"), previews: branchPreview},
			ev:   event("labeled", "front", "preview", "preview"),
			want: map[string]float64{
				`badil_reconciles_total{outcome="torn_down"}`:               1,
				`badil_teardowns_total{result="deleted",trigger="webhook"}`: 1,
			},
		},
		{
			name: "payload teardown",
			f: &reconcileFake{
				catalogErr: errors.New("boom"),
				previews:   map[string]*preview.Values{"feature-x": onlyFront42()},
			},
			ev: lifecycle.Event{Target: event("", "front", "").Target, Action: "closed", HeadRef: "feature/x"},
			want: map[string]float64{
				`badil_reconciles_total{outcome="torn_down"}`:               1,
				`badil_teardowns_total{result="deleted",trigger="webhook"}`: 1,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			tc.f.Metrics = metrics.New()
			lifecycle.ReconcilePR(context.Background(), tc.f.api(t), tc.ev)

			assertMetrics(t, tc.f.Metrics, tc.want,
				"badil_reconciles_total", "badil_reconcile_skips_total", "badil_teardowns_total")
		})
	}
}

func TestTeardownMetrics_Results(t *testing.T) {
	cases := []struct {
		name     string
		previews map[string]*preview.Values
		raw      map[string]string
		result   string
	}{
		{name: "noop", result: "noop"},
		{name: "error", raw: map[string]string{"previews/feature-x/values.yaml": "services: [\n"}, result: "error"},
		{name: "deleted", previews: map[string]*preview.Values{"feature-x": onlyFront42()}, result: "deleted"},
		{
			name: "unpinned",
			previews: map[string]*preview.Values{"feature-x": values(
				pinnedBy("api", "https://github.com/acme/monorepo/pull/7"), pinnedBy("front", front42),
			)},
			result: "unpinned",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := &reconcileFake{Metrics: metrics.New(), previews: tc.previews, raw: tc.raw}
			target := event("", "front", "").Target
			lifecycle.TeardownPRs(context.Background(), f.teardownAPI(t), target, []string{"feature-x"},
				lifecycle.PRClosedReason)

			assertMetrics(t, f.Metrics, map[string]float64{
				`badil_teardowns_total{result="` + tc.result + `",trigger="webhook"}`: 1,
			}, "badil_teardowns_total")
		})
	}
}

func TestTeardownMetrics_WebhookDeleteDecrementsPreviews(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{
		Metrics:  metrics.New(),
		previews: map[string]*preview.Values{"feature-x": onlyFront42()},
	}
	f.PreviewsListed(2)
	target := event("", "front", "").Target
	lifecycle.TeardownPRs(context.Background(), f.teardownAPI(t), target, []string{"feature-x"},
		lifecycle.PRClosedReason)

	assertMetrics(t, f.Metrics, map[string]float64{`badil_previews`: 1}, "badil_previews")
}

// TestReconcileMetrics_CreateIncrementsPreviews checks a deploy that creates
// its preview increments the previews gauge, and one to an existing preview
// does not.
func TestReconcileMetrics_CreateIncrementsPreviews(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{Metrics: metrics.New(), pr: fetchedPR("monorepo", true, "preview")}
	f.PreviewsListed(1)
	ev := event("labeled", "monorepo", "preview", "preview")
	lifecycle.ReconcilePR(context.Background(), f.api(t), ev)
	assertMetrics(t, f.Metrics, map[string]float64{`badil_previews`: 2}, "badil_previews")

	f.pr = fetchedPR("front", true, "preview")
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "preview", "preview"))
	assertMetrics(t, f.Metrics, map[string]float64{`badil_previews`: 2}, "badil_previews")
}

func TestSweepMetrics(t *testing.T) {
	captureLogs(t)
	f := &fakeAPI{
		Metrics: metrics.New(),
		previews: map[string]*preview.Values{
			"unpinned": values(preview.ServiceEntry{Name: "backend-1"}),
			"expired":  values(pin("backend-1", pr42, stale)),
			"closed":   values(pin("backend-2", pr7, recent)),
			"open":     values(pin("backend-1", pr42, recent)),
		},
		// "open" syncs unchanged, "stale" is rewritten.
		raw: map[string]string{
			"previews/stale/values.yaml": "services:\n  - name: front\n    commitSha: abc123\n" +
				"    metadata: {pr-url: \"https://github.com/acme/front/pull/3\", updated-at: \"" + recent + "\"}\n",
		},
		prOpen:    map[string]bool{"acme/backend-1#42": true, "acme/front#3": true},
		prHeadRef: map[string]string{"acme/backend-1#42": "open", "acme/front#3": "stale"},
		prLabels:  map[string][]string{"acme/backend-1#42": {"preview"}, "acme/front#3": {"preview"}},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_previews`: 2,
		`badil_sweep_previews_deleted_total{rule="no_pin"}`:       1,
		`badil_sweep_previews_deleted_total{rule="ttl"}`:          1,
		`badil_teardowns_total{result="deleted",trigger="sweep"}`: 1,
		`badil_previews_synced_total`:                             1,
	}, "badil_previews", "badil_sweep_previews_deleted_total", "badil_teardowns_total",
		"badil_previews_synced_total")
}

func TestSweepMetrics_UnpinOnlyIsASweepTeardown(t *testing.T) {
	captureLogs(t)
	f := openPRAt("feature-x", "preview:other")
	f.Metrics = metrics.New()
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_teardowns_total{result="deleted",trigger="sweep"}`: 1,
	}, "badil_teardowns_total")
}
