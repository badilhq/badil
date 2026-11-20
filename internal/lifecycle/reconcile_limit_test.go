package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// limitBody is the refusal comment of a PR refused the preview pricing at a
// cap of 2.
const limitBody = "## ⚠️ Preview: `pricing`\n\n" +
	"The preview limit (2) is reached. " +
	"Once a preview is torn down, push to this pull request or re-add its label."

func TestReconcile_PreviewLimitRefusesNewPreview(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics:     metrics.New(),
		pr:          fetchedPR("front", true, "preview:pricing"),
		maxPreviews: 2,
		listed:      []string{"checkout", "search"},
	}
	lifecycle.ReconcilePR(
		context.Background(),
		f.api(),
		event("labeled", "front", "preview:pricing", "preview:pricing"),
	)

	want := []string{"teardown:feature-pricing", "get:pricing", "list", "upsert"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %+v, want none", f.writes)
	}
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
	cases := []struct {
		name     string
		max      int
		listed   []string
		previews map[string]*preview.PreviewValues
		calls    []string
	}{
		{
			name:     "existing preview at the cap",
			max:      2,
			listed:   []string{"checkout", "pricing"},
			previews: map[string]*preview.PreviewValues{"pricing": values(preview.ServiceEntry{Name: "api"})},
			calls:    []string{"teardown:feature-pricing", "get:pricing", "write:pricing", "fanout"},
		},
		{
			name:   "new preview below the cap",
			max:    3,
			listed: []string{"checkout", "search"},
			calls:  []string{"teardown:feature-pricing", "get:pricing", "list", "write:pricing", "fanout"},
		},
		{
			name:   "cap unset",
			listed: []string{"checkout", "search"},
			calls:  []string{"teardown:feature-pricing", "get:pricing", "write:pricing", "fanout"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := &reconcileFake{
				pr:          fetchedPR("front", true, "preview:pricing"),
				maxPreviews: tc.max,
				listed:      tc.listed,
				previews:    tc.previews,
			}
			lifecycle.ReconcilePR(
				context.Background(),
				f.api(),
				event("labeled", "front", "preview:pricing", "preview:pricing"),
			)

			if !slices.Equal(f.calls, tc.calls) {
				t.Errorf("calls = %v, want %v", f.calls, tc.calls)
			}
			if len(f.writes) != 1 || f.writes[0].slug != "pricing" {
				t.Errorf("writes = %+v, want one to pricing", f.writes)
			}
			if len(f.upserts) != 0 {
				t.Errorf("upserts = %q, want none", f.upserts)
			}
		})
	}
}

func TestReconcile_PreviewLimitListError(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics:     metrics.New(),
		pr:          fetchedPR("front", true, "preview:pricing"),
		maxPreviews: 2,
		listErr:     errors.New("list boom"),
	}
	lifecycle.ReconcilePR(
		context.Background(),
		f.api(),
		event("labeled", "front", "preview:pricing", "preview:pricing"),
	)

	if len(f.writes) != 0 || len(f.upserts) != 0 {
		t.Errorf("writes = %+v, upserts = %q; want none", f.writes, f.upserts)
	}
	logs := capture.find(slog.LevelError, "preview reconcile failed")
	if len(logs) != 1 || logs[0]["err"].String() != "list boom" {
		t.Errorf("reconcile failed logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{`badil_reconciles_total{outcome="failed"}`: 1},
		"badil_reconciles_total", "badil_reconcile_skips_total")
}

func TestRefusedByHolder_Exists(t *testing.T) {
	cases := []struct {
		name     string
		open     bool
		slug     string
		previews map[string]*preview.PreviewValues
		exists   bool
		calls    []string
	}{
		{
			name:     "preview read",
			open:     true,
			slug:     "pricing",
			previews: map[string]*preview.PreviewValues{"pricing": values(preview.ServiceEntry{Name: "api"})},
			exists:   true,
			calls:    []string{"get:pricing"},
		},
		{name: "preview not found", open: true, slug: "pricing", calls: []string{"get:pricing"}},
		{name: "pr closed", slug: "pricing"},
		{name: "no target", open: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := &reconcileFake{previews: tc.previews}
			pr := fetchedPR("front", tc.open, "preview:pricing")
			o, r, vals := lifecycle.RefusedByHolder(
				context.Background(), f.api(), event("labeled", "front", "preview:pricing"), pr,
				preview.Resolution{Slug: tc.slug}, []string{"front"}, store.DefaultLabel,
			)

			if exists := vals != nil; o != "" || r != "" || exists != tc.exists {
				t.Errorf("refusedByHolder = (%q, %q, %v), want (\"\", \"\", values read %v)", o, r, vals, tc.exists)
			}
			if tc.exists && vals != tc.previews[tc.slug] {
				t.Errorf("values = %+v, want the preview read", vals)
			}
			if !slices.Equal(f.calls, tc.calls) {
				t.Errorf("calls = %v, want %v", f.calls, tc.calls)
			}
		})
	}
}
