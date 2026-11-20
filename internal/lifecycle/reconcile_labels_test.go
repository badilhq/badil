package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store/storetest"
)

// onlyFront42 is a preview whose last pin is front, by acme/front#42.
func onlyFront42() *preview.PreviewValues {
	return values(pinnedBy("front", front42))
}

// deletedBody is the comment of a teardown that deleted the preview slug,
// pushed as c.
func deletedBody(slug, reason string, c storetest.Commit) string {
	return "## 🧹 Preview: `" + slug + "`\n\n" +
		"Preview deleted (" + reason + "): no other PR is pinned.\n\n" +
		"---\n" + link(c)
}

// teardownSlugs returns the slugs of teardowns.
func teardownSlugs(teardowns []teardownCall) []string {
	out := make([]string, 0, len(teardowns))
	for _, td := range teardowns {
		out = append(out, td.slug)
	}
	return out
}

func TestReconcile_NamedLabelSharesPreviewAcrossRepos(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{}

	f.pr = fetchedPR("front", true, "preview:pricing")
	f.pr.HeadRef = "x"
	lifecycle.ReconcilePR(
		context.Background(),
		f.api(t),
		event("labeled", "front", "preview:pricing", "preview:pricing"),
	)
	// The second PR deploys to the preview the first one pinned front in.
	f.pr = fetchedPR("monorepo", true, "Preview:Pricing")
	f.pr.HeadRef = "y"
	f.upserts = nil
	lifecycle.ReconcilePR(
		context.Background(),
		f.api(t),
		event("labeled", "monorepo", "Preview:Pricing", "Preview:Pricing"),
	)
	if len(f.upserts) != 0 {
		t.Errorf("upserts = %v, want none: front is not a monorepo service", f.upserts)
	}
	want := []string{"pricing/front", "pricing/api", "pricing/worker"}
	if got := deployedLogged(capture); !slices.Equal(got, want) {
		t.Fatalf("deployed = %v, want %v", got, want)
	}
	refs := map[string]string{}
	for _, svc := range f.g.file("pricing").Services {
		refs[svc.Name] = svc.Metadata.VCSRefName
	}
	if refs["front"] != "x" || refs["api"] != "y" || refs["worker"] != "y" {
		t.Errorf("head refs = %v; want front x, api and worker y", refs)
	}

	// front moves to another preview: only front's PR is torn down.
	capture = captureLogs(t)
	f.pr = fetchedPR("front", true, "preview:other")
	f.pr.HeadRef = "x"
	lifecycle.ReconcilePR(
		context.Background(),
		f.api(t),
		event("unlabeled", "front", "preview:pricing", "preview:other"),
	)
	wantTeardowns := []teardownCall{
		{slug: "pricing", owner: "acme", repo: "front", number: 42},
		{slug: "x", owner: "acme", repo: "front", number: 42},
	}
	if got := teardownsLogged(capture); !slices.Equal(got, wantTeardowns) {
		t.Errorf("teardowns = %+v, want %+v", got, wantTeardowns)
	}
	if pins := f.g.pins("pricing"); pins["front"] != "" || pins["api"] != fetchedSHA {
		t.Errorf("pricing pins = %v, want front unpinned, api kept", pins)
	}
	if got := f.lastCommit(t).Subject(); got != "chore(preview): deploy acme/front#42 to other, unpin from pricing" {
		t.Errorf("subject = %q", got)
	}
}

func TestReconcile_Moves(t *testing.T) {
	cases := []struct {
		name      string
		ev        lifecycle.Event
		labels    []string
		removed   string // the slug whose teardown comments
		reason    string
		teardowns []string
		subject   string
	}{
		{
			name:      "plain to named",
			ev:        event("labeled", "front", "preview:b", "preview", "preview:b"),
			labels:    []string{"preview", "preview:b"},
			removed:   "feature-pricing",
			reason:    "moved to preview `b`",
			teardowns: []string{"feature-pricing"},
			subject:   "chore(preview): deploy acme/front#42 to b, unpin from feature-pricing",
		},
		{
			name:      "named to named",
			ev:        event("unlabeled", "front", "preview:a", "preview:b"),
			labels:    []string{"preview:b"},
			removed:   "a",
			reason:    "label `preview:a` removed",
			teardowns: []string{"a", "feature-pricing"},
			subject:   "chore(preview): deploy acme/front#42 to b, unpin from a",
		},
		{
			name:      "named to plain",
			ev:        event("unlabeled", "front", "preview:a", "preview"),
			labels:    []string{"preview"},
			removed:   "a",
			reason:    "label `preview:a` removed",
			teardowns: []string{"a"},
			subject:   "chore(preview): deploy acme/front#42 to feature-pricing, unpin from a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{
				pr:       fetchedPR("front", true, tc.labels...),
				previews: map[string]*preview.PreviewValues{tc.removed: onlyFront42()},
			}
			lifecycle.ReconcilePR(context.Background(), f.api(t), tc.ev)

			teardowns := teardownsLogged(capture)
			if got := teardownSlugs(teardowns); !slices.Equal(got, tc.teardowns) {
				t.Errorf("teardowns = %v, want %v", got, tc.teardowns)
			}
			for _, td := range teardowns {
				if td.owner != "acme" || td.repo != "front" || td.number != 42 {
					t.Errorf("teardown %+v, want acme/front#42 only", td)
				}
			}
			c := f.lastCommit(t)
			if c.Subject() != tc.subject || len(f.g.pushed()) != 1 {
				t.Errorf("pushed = %q, want one commit %q", f.g.subjects(), tc.subject)
			}
			if want := deletedBody(tc.removed, tc.reason, c); len(f.upserts) != 1 || f.upserts[0] != want {
				t.Errorf("upserts = %q, want one with body:\n%s", f.upserts, want)
			}
			if want := []string{"apply", "upsert", "fanout"}; !slices.Equal(f.calls, want) {
				t.Errorf("calls = %v, want %v", f.calls, want)
			}
		})
	}
}

func TestReconcile_ConflictingNamedLabels(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:       fetchedPR("front", true, "preview:b", "Preview:A", "preview"),
		previews: map[string]*preview.PreviewValues{"a": onlyFront42()},
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "preview:b", "preview:b"))

	if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"a", "b", "feature-pricing"}) {
		t.Errorf("teardowns = %v, want a, b, feature-pricing", got)
	}
	c := f.assertOneCommit(t, "chore(preview): unpin acme/front#42 from a", "a: delete")
	wantConflict := "## ⚠️ Preview: conflicting labels\n\n" +
		"Keep one `preview:` label. This PR has `Preview:A`, `preview:b`."
	if len(f.upserts) != 2 || f.upserts[0] != deletedBody("a", "conflicting `preview:` labels", c) ||
		f.upserts[1] != wantConflict {
		t.Errorf("upserts = %q", f.upserts)
	}
	logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
	if len(logs) != 1 || logs[0]["reason"].String() != "conflicting preview labels" {
		t.Errorf("skip logs = %v", logs)
	}
}

func TestReconcile_ConflictCommentFailure(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:        fetchedPR("front", true, "preview:a", "preview:b"),
		upsertErr: errors.New("comment boom"),
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "preview:b", "preview:b"))

	logs := capture.find(slog.LevelWarn, "preview conflict comment failed")
	if len(logs) != 1 || logs[0]["err"].String() != "comment boom" || logs[0]["repo"].String() != "acme/front" ||
		logs[0]["pr_number"].Int64() != 42 {
		t.Errorf("conflict comment failed logs = %v", logs)
	}
}

func TestReconcile_ClosedWithNamedLabel(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:       fetchedPR("front", false, "preview:a"),
		previews: map[string]*preview.PreviewValues{"a": onlyFront42()},
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("closed", "front", "", "preview:a"))

	if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"a", "feature-pricing"}) {
		t.Errorf("teardowns = %v, want a, feature-pricing", got)
	}
	c := f.assertOneCommit(t, "chore(preview): unpin acme/front#42 from a", "a: delete")
	if want := deletedBody("a", lifecycle.PRClosedReason, c); len(f.upserts) != 1 || f.upserts[0] != want {
		t.Errorf("upserts = %q, want one with body:\n%s", f.upserts, want)
	}
}

func TestReconcile_PayloadTeardownWithNamedLabel(t *testing.T) {
	cases := []struct {
		name string
		f    *reconcileFake
	}{
		{name: "catalog error", f: &reconcileFake{catalogErr: errors.New("boom")}},
		{name: "pull request error", f: &reconcileFake{prErr: errors.New("boom")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			tc.f.Metrics = metrics.New()
			tc.f.previews = map[string]*preview.PreviewValues{"a": onlyFront42()}
			ev := event("closed", "front", "", "preview:a", "preview:feature-pricing", "bug")
			ev.HeadRef = "feature/pricing"
			lifecycle.ReconcilePR(context.Background(), tc.f.api(t), ev)

			if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"feature-pricing", "a"}) {
				t.Errorf("teardowns = %v, want feature-pricing, a", got)
			}
			tc.f.assertOneCommit(t, "chore(preview): unpin acme/front#42 from a", "a: delete")
			assertMetrics(t, tc.f.Metrics, map[string]float64{
				`badil_reconciles_total{outcome="torn_down"}`:               1,
				`badil_teardowns_total{result="deleted",trigger="webhook"}`: 1,
				`badil_teardowns_total{result="noop",trigger="webhook"}`:    1,
			}, "badil_reconciles_total", "badil_teardowns_total")
		})
	}
}

func TestReconcile_PayloadTeardownSkipsEmptyBranchSlug(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{prErr: errors.New("boom")}
	ev := event("closed", "front", "", "preview:a")
	ev.HeadRef = "---"
	lifecycle.ReconcilePR(context.Background(), f.api(t), ev)

	if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"a"}) {
		t.Errorf("teardowns = %v, want a", got)
	}
}

func TestReconcile_IgnoredNamedLabelLogged(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "preview", "preview:")}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "preview", "preview", "preview:"))

	if got := deployedLogged(capture); !slices.Equal(got, []string{"feature-pricing/front"}) {
		t.Errorf("deployed = %v, want feature-pricing/front", got)
	}
	logs := capture.find(slog.LevelInfo, "preview label ignored")
	if len(logs) != 1 || logs[0]["label"].String() != "preview:" || logs[0]["repo"].String() != "acme/front" ||
		logs[0]["pr_number"].Int64() != 42 {
		t.Errorf("label ignored logs = %v", logs)
	}
}

// heldBody is the refusal comment of front held in pricing by holder.
func heldBody(holder string) string {
	return "## ⚠️ Preview: `pricing`\n\n" +
		"`front` is already pinned in this preview by " + holder + ". " +
		"Once that pull request leaves this preview, push to this pull request or re-add its label. " +
		"If that pull request is closed, or now carries a different `preview:` label, " +
		"it is released within the next cleanup run."
}

func TestReconcile_ServiceHeldByAnotherPR(t *testing.T) {
	capture := captureLogs(t)
	holder := "https://github.com/acme/front/pull/7"
	f := &reconcileFake{
		pr: fetchedPR("front", true, "preview:pricing"),
		previews: map[string]*preview.PreviewValues{"pricing": values(
			preview.ServiceEntry{Name: "api"},
			pin("front", holder, recent),
		)},
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
	if got := teardownSlugs(teardownsLogged(capture)); !slices.Equal(got, []string{"feature-pricing"}) {
		t.Errorf("teardowns = %v, want feature-pricing", got)
	}
	if want := heldBody(holder); len(f.upserts) != 1 || f.upserts[0] != want {
		t.Errorf("upserts = %q, want one with body:\n%s", f.upserts, want)
	}
	logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
	if len(logs) != 1 || logs[0]["reason"].String() != "service held by another pr" ||
		logs[0]["holder"].String() != holder || logs[0]["repo"].String() != "acme/front" {
		t.Errorf("skip logs = %v", logs)
	}
}

func TestReconcile_HolderCheckDeploys(t *testing.T) {
	cases := []struct {
		name string
		file *preview.PreviewValues
	}{
		{name: "own pin", file: values(pin("front", "https://github.com/ACME/Front/pull/42", recent))},
		{name: "other service", file: values(pin("api", "https://github.com/acme/monorepo/pull/7", recent))},
		{name: "unparsable pr-url", file: values(pin("front", "not-a-pr-url", recent))},
		{name: "no preview"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{pr: fetchedPR("front", true, "preview:pricing")}
			if tc.file != nil {
				f.previews = map[string]*preview.PreviewValues{"pricing": tc.file}
			}
			lifecycle.ReconcilePR(
				context.Background(),
				f.api(t),
				event("labeled", "front", "preview:pricing", "preview:pricing"),
			)

			if got := deployedLogged(capture); !slices.Equal(got, []string{"pricing/front"}) {
				t.Errorf("deployed = %v, want pricing/front", got)
			}
		})
	}
}

func TestReconcile_HolderCheckReadError(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:  fetchedPR("front", true, "preview:pricing"),
		raw: map[string]string{"previews/pricing/values.yaml": "services: [\n"},
	}
	lifecycle.ReconcilePR(
		context.Background(),
		f.api(t),
		event("labeled", "front", "preview:pricing", "preview:pricing"),
	)

	f.assertNothingPushed(t)
	if len(f.upserts) != 0 {
		t.Errorf("upserts = %q; want none", f.upserts)
	}
	logs := capture.find(slog.LevelError, "preview reconcile failed")
	if len(logs) != 1 || !strings.HasPrefix(logs[0]["err"].String(), "parse previews/pricing/values.yaml") {
		t.Errorf("reconcile failed logs = %v", logs)
	}
}

// TestReconcile_HolderCheckOnRetriedTree loses the first push to a deploy of
// front by another PR: the retry finds front held and refuses.
func TestReconcile_HolderCheckOnRetriedTree(t *testing.T) {
	capture := captureLogs(t)
	holder := "https://github.com/acme/front/pull/7"
	f := &reconcileFake{pr: fetchedPR("front", true, "preview:pricing")}
	raced := false
	f.beforePush = func(r *storetest.Remote) error {
		if !raced {
			raced = true
			r.Set("previews/pricing/values.yaml", marshal(t, *values(pin("front", holder, recent))))
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
	if want := heldBody(holder); len(f.upserts) != 1 || f.upserts[0] != want {
		t.Errorf("upserts = %q, want the held comment", f.upserts)
	}
	if logs := capture.find(slog.LevelInfo, "preview deploy"); len(logs) != 0 {
		t.Errorf("preview deploy logs = %v, want none", logs)
	}
	if pins := f.g.pins("pricing"); pins["front"] != "abc123" {
		t.Errorf("pricing pins = %v, want the holder's", pins)
	}
}

// TestReconcile_ConflictingLabelsPushFailure fails the commit of a PR whose
// named labels conflict: its teardown is an error, and it is still told to
// keep one label and skipped.
func TestReconcile_ConflictingLabelsPushFailure(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics:    metrics.New(),
		pr:         fetchedPR("front", true, "preview:a", "preview:b"),
		previews:   map[string]*preview.PreviewValues{"a": onlyFront42()},
		beforePush: func(*storetest.Remote) error { return errors.New("push boom") },
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "preview:b", "preview:b"))

	f.assertNothingPushed(t)
	wantConflict := "## ⚠️ Preview: conflicting labels\n\n" +
		"Keep one `preview:` label. This PR has `preview:a`, `preview:b`."
	if len(f.upserts) != 1 || f.upserts[0] != wantConflict {
		t.Errorf("upserts = %q, want only the conflict comment", f.upserts)
	}
	logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
	if len(logs) != 1 || logs[0]["reason"].String() != "conflicting preview labels" {
		t.Errorf("skip logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="skipped"}`:               1,
		`badil_teardowns_total{result="error",trigger="webhook"}`: 1,
		`badil_teardowns_total{result="noop",trigger="webhook"}`:  2,
	}, "badil_reconciles_total", "badil_teardowns_total")
}

// TestReconcile_TargetReadErrorKeepsCleanup reads an unreadable target
// preview: the PR's teardown from its other preview is still committed, and
// the reconcile fails.
func TestReconcile_TargetReadErrorKeepsCleanup(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics:  metrics.New(),
		pr:       fetchedPR("front", true, "preview:pricing"),
		previews: map[string]*preview.PreviewValues{"a": onlyFront42()},
		raw:      map[string]string{"previews/pricing/values.yaml": "services: [\n"},
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("unlabeled", "front", "preview:a", "preview:pricing"))

	c := f.assertOneCommit(t, "chore(preview): unpin acme/front#42 from a", "a: delete")
	if len(f.upserts) != 1 || f.upserts[0] != deletedBody("a", "label `preview:a` removed", c) {
		t.Errorf("upserts = %q, want the teardown comment of a", f.upserts)
	}
	logs := capture.find(slog.LevelError, "preview reconcile failed")
	if len(logs) != 1 || !strings.HasPrefix(logs[0]["err"].String(), "parse previews/pricing/values.yaml") {
		t.Errorf("reconcile failed logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="failed"}`:                  1,
		`badil_teardowns_total{result="deleted",trigger="webhook"}`: 1,
		`badil_teardowns_total{result="noop",trigger="webhook"}`:    1,
	}, "badil_reconciles_total", "badil_teardowns_total")
}
