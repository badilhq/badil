package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store/storetest"
)

// pathsBadil is a monorepo, acme/platform, whose services declare paths, and
// billing, in another repo.
const pathsBadil = `services:
  api:
    repo: https://github.com/acme/platform.git
    paths: ["api/**", "libs/common/**", "go.mod"]
  web:
    repo: https://github.com/acme/platform.git
    paths: ["web/**"]
  worker:
    repo: https://github.com/acme/platform.git
    paths: ["worker/**", "libs/common/**"]
  billing:
    repo: https://github.com/acme/billing.git
`

const (
	platformPR42 = "https://github.com/acme/platform/pull/42"
	platformPR7  = "https://github.com/acme/platform/pull/7"
)

// noServiceBody is the no-service comment of the preview feature-pricing.
const noServiceBody = "## ⏭️ Preview: `feature-pricing`\n\n" +
	"This PR changes no service of the preview (`paths` in badil.yaml), so nothing is deployed."

// changed returns files changing each of names.
func changed(names ...string) []forge.ChangedFile {
	files := make([]forge.ChangedFile, 0, len(names))
	for _, name := range names {
		files = append(files, forge.ChangedFile{Filename: name})
	}
	return files
}

// deployedServices returns the services logged as deployed to capture.
func deployedServices(capture *logCapture) []string {
	var out []string
	for _, d := range deployedLogged(capture) {
		_, service, _ := strings.Cut(d, "/")
		out = append(out, service)
	}
	return out
}

// platformFake is a reconcileFake of pathsBadil, for acme/platform#42 open
// with the preview label, whose PR changes files.
func platformFake(files ...forge.ChangedFile) *reconcileFake {
	return &reconcileFake{
		Metrics: metrics.New(),
		badil:   pathsBadil,
		pr:      fetchedPR("platform", true, "preview"),
		files:   files,
	}
}

func labeledPlatform() lifecycle.Event {
	return event("labeled", "platform", "preview", "preview")
}

func TestReconcile_PathsSelectTouchedServices(t *testing.T) {
	cases := []struct {
		name  string
		files []forge.ChangedFile
		want  []string
	}{
		{name: "one service", files: changed("web/src/app.tsx"), want: []string{"web"}},
		{
			name:  "shared library",
			files: changed("web/src/app.tsx", "libs/common/auth.go"),
			want:  []string{"api", "web", "worker"},
		},
		{name: "root file", files: changed("go.mod"), want: []string{"api"}},
		{
			name:  "renamed out of a service",
			files: []forge.ChangedFile{{Filename: "tools/x.go", PreviousFilename: "api/x.go"}},
			want:  []string{"api"},
		},
		{
			name:  "renamed into a service",
			files: []forge.ChangedFile{{Filename: "worker/x.go", PreviousFilename: "tools/x.go"}},
			want:  []string{"worker"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := platformFake(tc.files...)
			lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

			if got := deployedServices(capture); !slices.Equal(got, tc.want) {
				t.Errorf("deployed = %v, want %v", got, tc.want)
			}
			if !slices.Contains(f.calls, "files") {
				t.Errorf("calls = %v, want a files listing", f.calls)
			}
			assertMetrics(t, f.Metrics, map[string]float64{`badil_reconciles_total{outcome="deployed"}`: 1},
				"badil_reconciles_total", "badil_reconcile_skips_total")
		})
	}
}

func TestReconcile_PathsOverListingLimitTouchesEveryService(t *testing.T) {
	capture := captureLogs(t)
	f := platformFake(changed("docs/README.md")...)
	f.pr.ChangedFiles = forge.MaxListedFiles + 1
	lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

	if got, want := deployedServices(capture), []string{"api", "web", "worker"}; !slices.Equal(got, want) {
		t.Errorf("deployed = %v, want %v", got, want)
	}
	if slices.Contains(f.calls, "files") {
		t.Errorf("calls = %v, want no files listing", f.calls)
	}
}

func TestReconcile_NoPathsListsNoFiles(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("monorepo", true, "preview"), files: changed("docs/README.md")}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "monorepo", "preview", "preview"))

	if got, want := deployedServices(capture), []string{"api", "worker"}; !slices.Equal(got, want) {
		t.Errorf("deployed = %v, want %v", got, want)
	}
	if slices.Contains(f.calls, "files") {
		t.Errorf("calls = %v, want no files listing", f.calls)
	}
}

func TestReconcile_PathsMixedRepo(t *testing.T) {
	const badil = `services:
  api: {repo: https://github.com/acme/platform.git, paths: ["api/**"]}
  tools: {repo: https://github.com/acme/platform.git}
`
	cases := []struct {
		name  string
		files []forge.ChangedFile
		want  []string
	}{
		{name: "no service with paths", files: changed("docs/README.md"), want: []string{"tools"}},
		{name: "service with paths", files: changed("api/main.go"), want: []string{"api", "tools"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := platformFake(tc.files...)
			f.badil = badil
			lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

			if got := deployedServices(capture); !slices.Equal(got, tc.want) {
				t.Errorf("deployed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconcile_PathsUnlabeledRedeploy(t *testing.T) {
	capture := captureLogs(t)
	f := platformFake(changed("web/src/app.tsx")...)
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("unlabeled", "platform", "preview:old", "preview"))

	if got := deployedServices(capture); !slices.Equal(got, []string{"web"}) {
		t.Errorf("deployed = %v, want [web]", got)
	}
	if want := []string{"files", "apply", "fanout"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"old"}) {
		t.Errorf("teardowns = %v, want old", got)
	}
}

func TestReconcile_PathsListingFails(t *testing.T) {
	cases := []struct {
		name   string
		action string
		label  string
	}{
		{name: "labeled", action: "labeled", label: "preview"},
		{name: "closed delivery of an open PR", action: "closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := platformFake()
			f.filesErr = errors.New("files boom")
			lifecycle.ReconcilePR(context.Background(), f.api(t), event(tc.action, "platform", tc.label, "preview"))

			if want := []string{"files"}; !slices.Equal(f.calls, want) || len(teardownsLogged(capture)) != 0 {
				t.Errorf("calls = %v, want only the files listing", f.calls)
			}
			f.assertNothingPushed(t)
			logs := capture.find(slog.LevelError, "preview reconcile failed")
			if len(logs) != 1 || logs[0]["err"].String() != "files boom" {
				t.Errorf("reconcile failed logs = %v", logs)
			}
			fallback := capture.find(slog.LevelWarn, "preview reconcile fell back to payload teardown")
			if len(fallback) != 0 {
				t.Errorf("payload teardown logs = %v, want none", fallback)
			}
			assertMetrics(t, f.Metrics, map[string]float64{`badil_reconciles_total{outcome="failed"}`: 1},
				"badil_reconciles_total", "badil_reconcile_skips_total", "badil_teardowns_total")
		})
	}
}

func TestReconcile_PathsHolderCheckOnTouchedServices(t *testing.T) {
	cases := []struct {
		name     string
		files    []forge.ChangedFile
		deployed []string
		held     bool
	}{
		{name: "untouched service held", files: changed("web/src/app.tsx"), deployed: []string{"web"}},
		{name: "touched service held", files: changed("api/main.go"), held: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := platformFake(tc.files...)
			f.previews = map[string]*preview.PreviewValues{
				"feature-pricing": values(pin("api", platformPR7, recent), preview.ServiceEntry{Name: "web"}),
			}
			lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

			if got := deployedServices(capture); !slices.Equal(got, tc.deployed) {
				t.Errorf("deployed = %v, want %v", got, tc.deployed)
			}
			if held := len(f.upserts) == 1; held != tc.held || len(f.upserts) > 1 {
				t.Errorf("upserts = %q, want held comment %v", f.upserts, tc.held)
			}
			if logs := capture.find(slog.LevelInfo, "preview teardown"); len(logs) != 0 {
				t.Errorf("teardown logs = %v, want none", logs)
			}
		})
	}
}

func TestReconcile_PathsUnpinUntouchedBeforeDeploy(t *testing.T) {
	capture := captureLogs(t)
	f := platformFake(changed("web/src/app.tsx")...)
	// billing was pinned by the PR through /v1/deploy: it is not a service of
	// the PR's repo and stays pinned.
	f.previews = map[string]*preview.PreviewValues{
		"feature-pricing": values(
			pin("api", platformPR42, recent),
			preview.ServiceEntry{Name: "web"},
			pin("worker", platformPR42, recent),
			pin("billing", platformPR42, recent),
		),
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

	f.assertOneCommit(t, "chore(preview): deploy acme/platform#42 to feature-pricing",
		"feature-pricing: unpin api", "feature-pricing: unpin worker", "feature-pricing: pin web@fe7c4ed")
	if want := []string{"files", "apply", "fanout"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v: one summary of the preview", f.calls, want)
	}
	want := map[string]string{"api": "", "web": fetchedSHA, "worker": "", "billing": "abc123"}
	if pins := f.g.pins("feature-pricing"); !maps.Equal(pins, want) {
		t.Errorf("pins = %v, want %v", pins, want)
	}
	if len(f.upserts) != 0 {
		t.Errorf("upserts = %q; want none", f.upserts)
	}
	logs := capture.find(slog.LevelInfo, "preview teardown")
	if len(logs) != 1 || logs[0]["removed"].String() != "api, worker" || logs[0]["deleted"].Bool() {
		t.Errorf("preview teardown logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="deployed"}`:                 1,
		`badil_teardowns_total{result="unpinned",trigger="webhook"}`: 1,
	}, "badil_reconciles_total", "badil_teardowns_total")
}

func TestReconcile_PathsUnpinKeepsLastPinFile(t *testing.T) {
	capture := captureLogs(t)
	f := platformFake(changed("web/src/app.tsx")...)
	f.previews = map[string]*preview.PreviewValues{
		"feature-pricing": values(pin("api", platformPR42, recent), preview.ServiceEntry{Name: "web"}),
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

	// The PR's last pin is unpinned without deleting the file, then web is
	// deployed.
	f.assertOneCommit(t, "chore(preview): deploy acme/platform#42 to feature-pricing",
		"feature-pricing: unpin api", "feature-pricing: pin web@fe7c4ed")
	if got := deployedServices(capture); !slices.Equal(got, []string{"web"}) {
		t.Errorf("deployed = %v, want web", got)
	}
}

func TestReconcile_PathsNoUnpin(t *testing.T) {
	cases := []struct {
		name     string
		previews map[string]*preview.PreviewValues
	}{
		{name: "preview does not exist"},
		{
			name: "no untouched service pinned by the PR",
			previews: map[string]*preview.PreviewValues{
				"feature-pricing": values(
					pin("api", platformPR7, recent), pin("web", platformPR42, recent),
					pin("billing", platformPR42, recent),
				),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := platformFake(changed("web/src/app.tsx")...)
			f.previews = tc.previews
			lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

			if logs := capture.find(slog.LevelInfo, "preview teardown"); len(logs) != 0 {
				t.Errorf("teardown logs = %v, want none", logs)
			}
			f.assertOneCommit(t, "chore(preview): deploy acme/platform#42 to feature-pricing",
				"feature-pricing: pin web@fe7c4ed")
			assertMetrics(t, f.Metrics, map[string]float64{`badil_reconciles_total{outcome="deployed"}`: 1},
				"badil_reconciles_total", "badil_teardowns_total")
		})
	}
}

func TestReconcile_PathsUnpinFails(t *testing.T) {
	capture := captureLogs(t)
	f := platformFake(changed("web/src/app.tsx")...)
	f.previews = map[string]*preview.PreviewValues{
		"feature-pricing": values(pin("api", platformPR42, recent), preview.ServiceEntry{Name: "web"}),
	}
	f.beforePush = func(*storetest.Remote) error { return errors.New("push boom") }
	lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

	f.assertNothingPushed(t)
	if len(f.fanOuts) != 0 {
		t.Errorf("fanOuts = %v; want none", f.fanOuts)
	}
	if logs := capture.find(slog.LevelError, "preview teardown failed"); len(logs) != 1 {
		t.Errorf("preview teardown failed logs = %v, want one", logs)
	}
	logs := capture.find(slog.LevelError, "preview reconcile failed")
	if len(logs) != 1 || !strings.Contains(logs[0]["err"].String(), "push boom") {
		t.Errorf("reconcile failed logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="failed"}`:                1,
		`badil_teardowns_total{result="error",trigger="webhook"}`: 1,
	}, "badil_reconciles_total", "badil_teardowns_total")
}

func TestReconcile_PathsNoServiceChanged(t *testing.T) {
	cases := []struct {
		name     string
		previews map[string]*preview.PreviewValues
		push     error
		result   string
		calls    []string
	}{
		{
			name:   "preview does not exist",
			result: "noop",
			calls:  []string{"files", "apply", "upsert"},
		},
		{
			name: "PR pinned in the preview",
			previews: map[string]*preview.PreviewValues{
				"feature-pricing": values(pin("api", platformPR7, recent), pin("web", platformPR42, recent)),
			},
			result: "unpinned",
			calls:  []string{"files", "apply", "fanout", "upsert"},
		},
		{
			name: "teardown fails",
			previews: map[string]*preview.PreviewValues{
				"feature-pricing": values(pin("api", platformPR7, recent), pin("web", platformPR42, recent)),
			},
			push:   errors.New("push boom"),
			result: "error",
			calls:  []string{"files", "apply", "upsert"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := platformFake(changed("docs/README.md")...)
			// Neither the holder nor the limit check runs: api is held and
			// the cap is reached.
			f.previews = tc.previews
			if f.previews == nil {
				f.previews = listedPreviews("checkout")
			}
			f.maxPreviews = 1
			if tc.push != nil {
				f.beforePush = func(*storetest.Remote) error { return tc.push }
			}
			lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

			if !slices.Equal(f.calls, tc.calls) {
				t.Errorf("calls = %v, want %v", f.calls, tc.calls)
			}
			if len(f.upserts) != 1 || f.upserts[0] != noServiceBody {
				t.Errorf("upserts = %q, want one with body:\n%s", f.upserts, noServiceBody)
			}
			if logs := capture.find(slog.LevelInfo, "preview deploy"); len(logs) != 0 {
				t.Errorf("preview deploy logs = %v, want none", logs)
			}
			logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
			if len(logs) != 1 || logs[0]["reason"].String() != "no service changed" {
				t.Errorf("skip logs = %v", logs)
			}
			assertMetrics(t, f.Metrics, map[string]float64{
				`badil_reconciles_total{outcome="skipped"}`:                           1,
				`badil_reconcile_skips_total{reason="no service changed"}`:            1,
				`badil_teardowns_total{result="` + tc.result + `",trigger="webhook"}`: 1,
			}, "badil_reconciles_total", "badil_reconcile_skips_total", "badil_teardowns_total")
		})
	}
}

func TestReconcile_PathsNoServiceCommentFails(t *testing.T) {
	capture := captureLogs(t)
	f := platformFake(changed("docs/README.md")...)
	f.upsertErr = errors.New("upsert boom")
	lifecycle.ReconcilePR(context.Background(), f.api(t), labeledPlatform())

	logs := capture.find(slog.LevelWarn, "preview conflict comment failed")
	if len(logs) != 1 || logs[0]["err"].String() != "upsert boom" {
		t.Errorf("conflict comment failed logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="skipped"}`:                1,
		`badil_reconcile_skips_total{reason="no service changed"}`: 1,
	}, "badil_reconciles_total", "badil_reconcile_skips_total")
}
