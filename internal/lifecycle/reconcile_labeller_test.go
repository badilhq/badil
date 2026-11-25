package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// labellerBody is the refusal comment of a PR refused the preview slug
// because login added label without write access to acme/front.
func labellerBody(slug, login, label string) string {
	return "## ⚠️ Preview: `" + slug + "`\n\n" +
		"`" + login + "` added the `" + label + "` label but does not have write access to `acme/front`, " +
		"so badil does not deploy this PR. Someone with write access must add the label again."
}

// labellerNotFoundBody is the refusal comment of a PR refused the preview
// slug because who added label was not found.
func labellerNotFoundBody(slug, label string) string {
	return "## ⚠️ Preview: `" + slug + "`\n\n" +
		"badil could not find who added the `" + label + "` label, so it does not deploy this PR. " +
		"Someone with write access must add the label again."
}

// noWriteLogins returns the logins without write access of the labeller
// tests: a Triage user, a bot and a login the permission read answers 404
// for, which the GitHub forge's CanWrite reports as no write access.
func noWriteLogins() map[string]bool {
	return map[string]bool{"triager": true, "github-actions[bot]": true, "ghost": true}
}

// assertLabellerRefused fails t unless res is the labeller refusal, logged
// with labeller, and f posted body and nothing else.
func assertLabellerRefused(
	t *testing.T, f *reconcileFake, capture *logCapture, res lifecycle.ReconcileResult, labeller, body string,
) {
	t.Helper()
	if res.Outcome != lifecycle.OutcomeSkipped || res.Reason != lifecycle.LabellerReason {
		t.Errorf("result = %+v, want skipped %q", res, lifecycle.LabellerReason)
	}
	if len(f.upserts) == 0 || f.upserts[len(f.upserts)-1] != body {
		t.Errorf("upserts = %q, want last:\n%s", f.upserts, body)
	}
	if len(f.fanOuts) != 0 {
		t.Errorf("fanOuts = %q, want none", f.fanOuts)
	}
	logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
	if len(logs) != 1 || logs[0]["reason"].String() != "labeller without write access" ||
		logs[0]["labeller"].String() != labeller || logs[0]["repo"].String() != "acme/front" {
		t.Errorf("skip logs = %v, want one with labeller %q", logs, labeller)
	}
	if deployed := deployedLogged(capture); len(deployed) != 0 {
		t.Errorf("deployed = %v, want none", deployed)
	}
}

func TestReconcile_LabellerWithWriteDeploys(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:          fetchedPR("front", true, "preview:pricing"),
		labelEvents: []labelEvent{{"triager", "preview:pricing"}, {"writer", "preview:pricing"}},
		noWrite:     noWriteLogins(),
	}
	res := lifecycle.ReconcilePR(context.Background(), f.api(t),
		event("labeled", "front", "preview:pricing", "preview:pricing"))

	if res.Outcome != lifecycle.OutcomeDeployed {
		t.Errorf("result = %+v, want deployed", res)
	}
	if got := deployedLogged(capture); !slices.Equal(got, []string{"pricing/front"}) {
		t.Errorf("deployed = %v, want pricing/front", got)
	}
	if want := [][]string{{"preview:pricing"}}; !slices.EqualFunc(f.labelled, want, slices.Equal) {
		t.Errorf("labelled = %v, want %v", f.labelled, want)
	}
	if !slices.Equal(f.checked, []string{"writer"}) {
		t.Errorf("checked = %v, want [writer]", f.checked)
	}
}

func TestReconcile_LabellerRefused(t *testing.T) {
	cases := []struct {
		name     string
		events   []labelEvent
		labeller string
		body     string
	}{
		{
			name:     "triage user",
			events:   []labelEvent{{"writer", "preview:pricing"}, {"triager", "preview:pricing"}},
			labeller: "triager",
			body:     labellerBody("pricing", "triager", "preview:pricing"),
		},
		{
			name:     "bot",
			events:   []labelEvent{{"github-actions[bot]", "preview:pricing"}},
			labeller: "github-actions[bot]",
			body:     labellerBody("pricing", "github-actions[bot]", "preview:pricing"),
		},
		{
			name:     "permission read 404",
			events:   []labelEvent{{"ghost", "preview:pricing"}},
			labeller: "ghost",
			body:     labellerBody("pricing", "ghost", "preview:pricing"),
		},
		{
			name:   "no labeled event",
			events: []labelEvent{{"writer", "bug"}},
			body:   labellerNotFoundBody("pricing", "preview:pricing"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{
				Metrics:     metrics.New(),
				pr:          fetchedPR("front", true, "bug", "preview:pricing"),
				labelEvents: tc.events,
				noWrite:     noWriteLogins(),
			}
			res := lifecycle.ReconcilePR(context.Background(), f.api(t),
				event("labeled", "front", "preview:pricing", "bug", "preview:pricing"))

			assertLabellerRefused(t, f, capture, res, tc.labeller, tc.body)
			if want := []string{"apply", "upsert"}; !slices.Equal(f.calls, want) {
				t.Errorf("calls = %v, want %v", f.calls, want)
			}
			f.assertNothingPushed(t)
			if len(f.upserts) != 1 {
				t.Errorf("upserts = %q, want only the refusal", f.upserts)
			}
			assertMetrics(t, f.Metrics, map[string]float64{
				`badil_reconciles_total{outcome="skipped"}`:                           1,
				`badil_reconcile_skips_total{reason="labeller without write access"}`: 1,
			}, "badil_reconciles_total", "badil_reconcile_skips_total")
		})
	}
}

// TestReconcile_LabellerRefusedKeepsCleanup moves a PR from its branch
// preview to a named one its labeller may not deploy: the teardown from the
// branch preview is committed, nothing is written in the target.
func TestReconcile_LabellerRefusedKeepsCleanup(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:          fetchedPR("front", true, "preview:b"),
		previews:    map[string]*preview.Values{"feature-pricing": onlyFront42()},
		labelEvents: []labelEvent{{"triager", "preview:b"}},
		noWrite:     noWriteLogins(),
	}
	res := lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "preview:b", "preview:b"))

	assertLabellerRefused(t, f, capture, res, "triager", labellerBody("b", "triager", "preview:b"))
	c := f.assertOneCommit(t, "chore(preview): unpin acme/front#42 from feature-pricing", "feature-pricing: delete")
	if f.g.file("feature-pricing") != nil || f.g.file("b") != nil {
		t.Errorf("previews feature-pricing, b = %v, %v; want both absent",
			f.g.file("feature-pricing"), f.g.file("b"))
	}
	want := []string{
		deletedBody("feature-pricing", "moved to preview `b`", c),
		labellerBody("b", "triager", "preview:b"),
	}
	if !slices.Equal(f.upserts, want) {
		t.Errorf("upserts = %q, want %q", f.upserts, want)
	}
}

// TestReconcile_LabellerRefusedIntoExistingPreview pushes to a PR deployed
// in its preview whose label was added by a Triage user: the push is
// refused and its pins stay at their old SHA.
func TestReconcile_LabellerRefusedIntoExistingPreview(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:          fetchedPR("front", true, "preview:pricing"),
		previews:    map[string]*preview.Values{"pricing": onlyFront42()},
		labelEvents: []labelEvent{{"triager", "preview:pricing"}},
		noWrite:     noWriteLogins(),
	}
	res := lifecycle.ReconcilePR(context.Background(), f.api(t),
		event("synchronize", "front", "", "preview:pricing"))

	assertLabellerRefused(t, f, capture, res, "triager", labellerBody("pricing", "triager", "preview:pricing"))
	f.assertNothingPushed(t)
	if pins := f.g.pins("pricing"); pins["front"] != "abc123" {
		t.Errorf("pricing pins = %v, want front kept at abc123", pins)
	}
}

// TestReconcile_LabellerOnCatchUp runs catch-up's reconcile of a PR whose
// label a Triage user added, already deployed at an older SHA: it is
// refused as a synchronize is.
func TestReconcile_LabellerOnCatchUp(t *testing.T) {
	capture := captureLogs(t)
	pr := listed(42, fetchedSHA, "preview")
	f := &reconcileFake{
		pr:          &pr,
		previews:    map[string]*preview.Values{"feature-42": values(pinnedAt(pr, "old"))},
		openPRs:     map[string][]forge.PullRequest{"acme/front": {pr}},
		labelEvents: []labelEvent{{"triager", "preview"}},
		noWrite:     noWriteLogins(),
	}
	var results []lifecycle.ReconcileResult
	f.open(t)
	lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f},
		func(ctx context.Context, ev lifecycle.Event, done func(lifecycle.ReconcileResult)) {
			res := lifecycle.ReconcilePR(ctx, f.api(t), ev)
			results = append(results, res)
			done(res)
		}).Run(context.Background())

	if len(results) != 1 {
		t.Fatalf("results = %+v, want one reconcile", results)
	}
	assertLabellerRefused(t, f, capture, results[0], "triager", labellerBody("feature-42", "triager", "preview"))
	f.assertNothingPushed(t)
	if pins := f.g.pins("feature-42"); pins["front"] != "old" {
		t.Errorf("feature-42 pins = %v, want front kept at old", pins)
	}
}

func TestReconcile_LabellerReadErrors(t *testing.T) {
	cases := []struct {
		name      string
		eventsErr error
		permErr   error
	}{
		{name: "events", eventsErr: errors.New("events boom")},
		{name: "permission", permErr: errors.New("permission boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{
				Metrics:   metrics.New(),
				pr:        fetchedPR("front", true, "preview:b"),
				previews:  map[string]*preview.Values{"feature-pricing": onlyFront42()},
				eventsErr: tc.eventsErr,
				permErr:   tc.permErr,
			}
			res := lifecycle.ReconcilePR(context.Background(), f.api(t),
				event("labeled", "front", "preview:b", "preview:b"))

			if res.Outcome != lifecycle.OutcomeFailed {
				t.Errorf("result = %+v, want failed", res)
			}
			if len(f.calls) != 0 {
				t.Errorf("calls = %v, want none", f.calls)
			}
			f.assertNothingPushed(t)
			want := "labeller check failed: " + errors.Join(tc.eventsErr, tc.permErr).Error()
			logs := capture.find(slog.LevelError, "preview reconcile failed")
			if len(logs) != 1 || logs[0]["err"].String() != want {
				t.Errorf("failure logs = %v, want err %q", logs, want)
			}
			assertMetrics(t, f.Metrics, map[string]float64{`badil_reconciles_total{outcome="failed"}`: 1},
				"badil_reconciles_total")
		})
	}
}

func TestReconcile_LabellerOfTargetLabels(t *testing.T) {
	cases := []struct {
		name     string
		labels   []string
		events   []labelEvent
		want     []string // the labels checked
		labeller string   // the refused labeller, "" when it deploys
		label    string
		slug     string
	}{
		{
			name:   "named target ignores the plain label",
			labels: []string{"preview", "preview:pricing", "bug"},
			events: []labelEvent{{"writer", "preview:pricing"}, {"triager", "preview"}, {"triager", "bug"}},
			want:   []string{"preview:pricing"},
			slug:   "pricing",
		},
		{
			name:     "named target refused for its own label",
			labels:   []string{"preview", "preview:pricing"},
			events:   []labelEvent{{"triager", "preview:pricing"}, {"writer", "preview"}},
			want:     []string{"preview:pricing"},
			labeller: "triager", label: "preview:pricing", slug: "pricing",
		},
		{
			name:     "two named labels of one slug: the newest",
			labels:   []string{"preview:Pricing", "preview:pricing"},
			events:   []labelEvent{{"writer", "preview:Pricing"}, {"triager", "preview:pricing"}},
			want:     []string{"preview:Pricing", "preview:pricing"},
			labeller: "triager", label: "preview:pricing", slug: "pricing",
		},
		{
			name:   "two named labels of one slug: the newest by a writer",
			labels: []string{"preview:Pricing", "preview:pricing"},
			events: []labelEvent{{"triager", "preview:pricing"}, {"writer", "preview:Pricing"}},
			want:   []string{"preview:Pricing", "preview:pricing"},
			slug:   "pricing",
		},
		{
			name:     "plain target",
			labels:   []string{"bug", "Preview"},
			events:   []labelEvent{{"triager", "Preview"}},
			want:     []string{"Preview"},
			labeller: "triager", label: "Preview", slug: "feature-pricing",
		},
		{
			name:   "label added again by a writer",
			labels: []string{"preview"},
			events: []labelEvent{{"triager", "preview"}, {"writer", "preview"}},
			want:   []string{"preview"},
			slug:   "feature-pricing",
		},
		{
			name:   "no event of the target's labels",
			labels: []string{"preview:Pricing", "preview:pricing"},
			events: []labelEvent{},
			want:   []string{"preview:Pricing", "preview:pricing"},
			label:  "preview:Pricing, preview:pricing", slug: "pricing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{
				pr:          fetchedPR("front", true, tc.labels...),
				labelEvents: tc.events,
				noWrite:     noWriteLogins(),
			}
			res := lifecycle.ReconcilePR(context.Background(), f.api(t),
				event("synchronize", "front", "", tc.labels...))

			if !slices.EqualFunc(f.labelled, [][]string{tc.want}, slices.Equal) {
				t.Errorf("labelled = %v, want [%v]", f.labelled, tc.want)
			}
			switch {
			case tc.label == "":
				if res.Outcome != lifecycle.OutcomeDeployed {
					t.Errorf("result = %+v, want deployed", res)
				}
				if got := deployedLogged(capture); !slices.Equal(got, []string{tc.slug + "/front"}) {
					t.Errorf("deployed = %v, want %s/front", got, tc.slug)
				}
			case tc.labeller == "":
				assertLabellerRefused(t, f, capture, res, "", labellerNotFoundBody(tc.slug, tc.label))
			default:
				assertLabellerRefused(t, f, capture, res, tc.labeller, labellerBody(tc.slug, tc.labeller, tc.label))
			}
		})
	}
}

// TestReconcile_LabellerNotCheckedWithoutDeploy closes and unlabels PRs:
// the labeller is not read.
func TestReconcile_LabellerNotCheckedWithoutDeploy(t *testing.T) {
	cases := []struct {
		name string
		pr   *forge.PullRequest
		ev   lifecycle.Event
	}{
		{"closed", fetchedPR("front", false, "preview"), event("closed", "front", "", "preview")},
		{"unlabeled", fetchedPR("front", true), event("unlabeled", "front", "preview")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			f := &reconcileFake{
				pr:        tc.pr,
				previews:  map[string]*preview.Values{"feature-pricing": onlyFront42()},
				eventsErr: errors.New("unexpected events read"),
			}
			res := lifecycle.ReconcilePR(context.Background(), f.api(t), tc.ev)

			if res.Outcome != lifecycle.OutcomeTornDown || len(f.labelled) != 0 {
				t.Errorf("result = %+v, labelled %v; want torn down without a labeller read", res, f.labelled)
			}
		})
	}
}
