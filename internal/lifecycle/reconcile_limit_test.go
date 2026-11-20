package lifecycle_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store/storetest"
)

// limitBody is the refusal comment of a PR refused the preview pricing at a
// cap of 2.
const limitBody = "## ⚠️ Preview: `pricing`\n\n" +
	"The preview limit (2) is reached. " +
	"Once a preview is torn down, push to this pull request or re-add its label."

// listedPreviews returns a preview, pinned by acme/front#7, for each of
// slugs.
func listedPreviews(slugs ...string) map[string]*preview.PreviewValues {
	out := map[string]*preview.PreviewValues{}
	for _, slug := range slugs {
		out[slug] = values(pinnedBy("front", "https://github.com/acme/front/pull/7"))
	}
	return out
}

func TestReconcile_PreviewLimitRefusesNewPreview(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics:     metrics.New(),
		pr:          fetchedPR("front", true, "preview:pricing"),
		maxPreviews: 2,
		previews:    listedPreviews("checkout", "search"),
	}
	lifecycle.ReconcilePR(
		context.Background(),
		f.api(t),
		event("labeled", "front", "preview:pricing", "preview:pricing"),
	)

	if want := []string{"apply", "upsert"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	f.assertNothingPushed(t)
	if len(f.upserts) != 1 || f.upserts[0] != limitBody {
		t.Errorf("upserts = %q, want one with body:\n%s", f.upserts, limitBody)
	}
	logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
	if len(logs) != 1 || logs[0]["reason"].String() != "preview limit reached" ||
		logs[0]["max_previews"].Int64() != 2 || logs[0]["repo"].String() != "acme/front" {
		t.Errorf("skip logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="skipped"}`:                   1,
		`badil_reconcile_skips_total{reason="preview limit reached"}`: 1,
	}, "badil_reconciles_total", "badil_reconcile_skips_total")
}

func TestReconcile_PreviewLimitDeploys(t *testing.T) {
	existing := listedPreviews("checkout")
	existing["pricing"] = values(preview.ServiceEntry{Name: "api"})
	cases := []struct {
		name     string
		max      int
		previews map[string]*preview.PreviewValues
	}{
		{name: "existing preview at the cap", max: 2, previews: existing},
		{name: "new preview below the cap", max: 3, previews: listedPreviews("checkout", "search")},
		{name: "cap unset", previews: listedPreviews("checkout", "search")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{
				pr:          fetchedPR("front", true, "preview:pricing"),
				maxPreviews: tc.max,
				previews:    tc.previews,
			}
			lifecycle.ReconcilePR(
				context.Background(),
				f.api(t),
				event("labeled", "front", "preview:pricing", "preview:pricing"),
			)

			if want := []string{"apply", "fanout"}; !slices.Equal(f.calls, want) {
				t.Errorf("calls = %v, want %v", f.calls, want)
			}
			if got := deployedLogged(capture); !slices.Equal(got, []string{"pricing/front"}) {
				t.Errorf("deployed = %v, want pricing/front", got)
			}
			if len(f.upserts) != 0 {
				t.Errorf("upserts = %q, want none", f.upserts)
			}
		})
	}
}

// TestReconcile_PreviewLimitOnRetriedTree loses the first push to a commit
// creating a preview that reaches the cap: the retry refuses.
func TestReconcile_PreviewLimitOnRetriedTree(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{
		Metrics:     metrics.New(),
		pr:          fetchedPR("front", true, "preview:pricing"),
		maxPreviews: 2,
		previews:    listedPreviews("checkout"),
	}
	raced := false
	f.beforePush = func(r *storetest.Remote) error {
		if !raced {
			raced = true
			r.Set("previews/search/values.yaml", "services: []\n")
		}
		return nil
	}
	lifecycle.ReconcilePR(
		context.Background(),
		f.api(t),
		event("labeled", "front", "preview:pricing", "preview:pricing"),
	)

	if got := f.g.subjects(); !slices.Equal(got, []string{"test"}) {
		t.Errorf("pushed = %q, want only the commit the push lost to", got)
	}
	if len(f.upserts) != 1 || f.upserts[0] != limitBody {
		t.Errorf("upserts = %q, want the limit comment", f.upserts)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="skipped"}`:                   1,
		`badil_reconcile_skips_total{reason="preview limit reached"}`: 1,
	}, "badil_reconciles_total", "badil_reconcile_skips_total")
}
