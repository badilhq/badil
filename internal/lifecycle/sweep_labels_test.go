package lifecycle_test

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/preview"
)

// openPRAt is a sweep fake whose only preview, slug, is pinned by the open PR
// acme/backend-1#42 on branch feature/x with labels.
func openPRAt(slug string, labels ...string) *fakeAPI {
	return &fakeAPI{
		previews:  map[string]*preview.Values{slug: values(pin("backend-1", pr42, recent))},
		prOpen:    map[string]bool{"acme/backend-1#42": true},
		prLabels:  map[string][]string{"acme/backend-1#42": labels},
		prHeadRef: map[string]string{"acme/backend-1#42": "feature/x"},
	}
}

func TestSweep_OpenPRUnpinnedWithoutComment(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
	}{
		{name: "conflict", labels: []string{"preview:pricing", "preview:other"}},
		{name: "moved to a named preview", labels: []string{"preview:other"}},
		{name: "moved to its branch preview", labels: []string{"preview"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := openPRAt("pricing", tc.labels...)
			if err := f.sweep(t, ttl); err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			want := []teardownCall{{slug: "pricing", owner: "acme", repo: "backend-1", number: 42}}
			if got := teardownsLogged(capture); !slices.Equal(got, want) {
				t.Errorf("teardowns = %+v, want %+v", got, want)
			}
			f.assertOneCommit(t, "chore(preview): sweep: delete 1, unpin 0, sync 0", "pricing: delete")
			if len(f.upserts) != 0 {
				t.Errorf("upserts = %v, want none", f.upserts)
			}
		})
	}
}

func TestSweep_OpenPRLabelGoneTearsDown(t *testing.T) {
	capture := captureLogs(t)
	f := openPRAt("pricing", "bug")
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"pricing"}) {
		t.Errorf("teardowns = %v, want one from pricing", got)
	}
	c := f.assertOneCommit(t, "chore(preview): sweep: delete 1, unpin 0, sync 0", "pricing: delete")
	wantBody := "## 🧹 Preview: `pricing`\n\n" +
		"Preview deleted (label `preview` removed): no other PR is pinned.\n\n" +
		"---\n" + link(c)
	if len(f.bodies) != 1 || f.bodies[0] != wantBody {
		t.Errorf("bodies = %q, want one with body:\n%s", f.bodies, wantBody)
	}
}

func TestSweep_OpenPRLeftAlone(t *testing.T) {
	cases := []struct {
		name   string
		slug   string
		labels []string
	}{
		{name: "unlabeled at its branch slug", slug: "feature-x"},
		{name: "plain label at its branch slug", slug: "feature-x", labels: []string{"preview"}},
		{name: "named label at its slug", slug: "pricing", labels: []string{"preview", "Preview:Pricing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := openPRAt(tc.slug, tc.labels...)
			if err := f.sweep(t, ttl); err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if got := teardownsLogged(capture); len(got) != 0 || len(f.upserts) != 0 || len(f.g.pushed()) != 0 {
				t.Errorf("teardowns = %+v, upserts = %v, pushed = %q; want none", got, f.upserts, f.g.subjects())
			}
		})
	}
}

// TestSweep_NoCatalogRunsOnlyTheClosedRule checks that without a catalog the
// open PR that moved is left alone and the closed one is torn down, which,
// keeping a pin, needs the catalog to project the file and fails.
func TestSweep_NoCatalogRunsOnlyTheClosedRule(t *testing.T) {
	capture := captureLogs(t)
	f := openPRAt("pricing", "preview:other")
	f.noCatalog = true
	f.previews["pricing"] = values(pin("backend-1", pr42, recent), pin("backend-2", pr7, recent))
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v (a catalog error is not a failure)", err)
	}
	// backend-1#42 is open but moved: left alone. backend-2#7 is closed.
	want := []teardownCall{{slug: "pricing", owner: "acme", repo: "backend-2", number: 7}}
	if got := teardownsLogged(capture); !slices.Equal(got, want) {
		t.Errorf("teardowns = %+v, want %+v", got, want)
	}
	if logs := capture.find(slog.LevelError, "preview teardown failed"); len(logs) != 1 {
		t.Errorf("teardown failed logs = %v, want one", logs)
	}
	warns := capture.find(slog.LevelWarn, "cleanup catalog unavailable")
	if len(warns) != 1 || warns[0]["err"].String() != "gitops: not found: badil.yaml" {
		t.Errorf("catalog unavailable logs = %v", warns)
	}
	if n := capture.count(slog.LevelWarn); n != len(warns) {
		t.Errorf("%d WARN records, want only cleanup catalog unavailable", n)
	}
}
