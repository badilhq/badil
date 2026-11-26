package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// failingExpiry is a lifecycle.ExpiryStore whose every call fails.
type failingExpiry struct{}

func (failingExpiry) Mark(context.Context, string, string, string, time.Time) error {
	return errors.New("mark boom")
}

func (failingExpiry) Recorded(context.Context, string) (string, bool, error) {
	return "", false, errors.New("read boom")
}
func (failingExpiry) Clear(context.Context, string) error            { return errors.New("clear boom") }
func (failingExpiry) Prune(context.Context, func(string) bool) error { return errors.New("prune boom") }

func TestCatchUp_ExpiryPrune(t *testing.T) {
	captureLogs(t)
	open := listed(1, "s1", "bug")
	f := &reconcileFake{
		Metrics:  metrics.New(),
		openPRs:  map[string][]forge.PullRequest{"acme/front": {open}},
		listErrs: map[string]error{"acme/monorepo": errors.New("boom")},
	}
	f.open(t)
	expiry := newMapExpiry()
	ctx := context.Background()
	for _, key := range []string{"acme/front#1", "acme/front#2", "acme/monorepo#3", "acme/gone#4", "Acme/Front#1"} {
		_ = expiry.Mark(ctx, key, "", "x", now)
	}
	r := &reconciles{}
	lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f, Expiry: expiry}, r.reconcile).Run(ctx)
	for key, want := range map[string]bool{
		"acme/front#1":    true,  // listed open (unlabelled counts)
		"acme/front#2":    false, // its repo listed, it is not open any more
		"acme/monorepo#3": true,  // its repo's listing failed
		"acme/gone#4":     false, // not a catalog repo
		"Acme/Front#1":    false, // not a PRKey (keys are lowercased)
	} {
		if _, ok := expiry.record(key); ok != want {
			t.Errorf("record of %s kept = %v, want %v", key, ok, want)
		}
	}
}

func TestCatchUp_ExpiryFailuresFailOpen(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics: metrics.New(),
		openPRs: map[string][]forge.PullRequest{"acme/front": {listed(1, "s1", "preview")}},
	}
	f.open(t)
	r := &reconciles{}
	lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f, Expiry: failingExpiry{}}, r.reconcile).
		Run(context.Background())
	if got := r.take(""); len(got) != 1 {
		t.Fatalf("reconciled = %v, want [1] (a failed expiry read must not skip)", got)
	}
	if logs := capture.find(slog.LevelWarn, "catch-up expiry read failed"); len(logs) != 1 ||
		logs[0]["repo"].String() != "acme/front" || logs[0]["pr_number"].Int64() != 1 ||
		logs[0]["err"].String() != "read boom" {
		t.Errorf("read failed logs = %v", logs)
	}
	if logs := capture.find(slog.LevelWarn, "expiry prune failed"); len(logs) != 1 {
		t.Errorf("prune failed logs = %v", logs)
	}
}

func TestCatchUp_NilExpiry(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics: metrics.New(),
		openPRs: map[string][]forge.PullRequest{"acme/front": {listed(1, "s1", "preview")}},
	}
	r := &reconciles{}
	catchUp(t, f, r).Run(context.Background())
	if got := r.take(""); len(got) != 1 {
		t.Fatalf("reconciled = %v, want [1]", got)
	}
	if got := catchUpInt(t, capture, "skipped_expired"); got != 0 {
		t.Errorf("skipped_expired = %d, want 0", got)
	}
}

func TestSweep_ExpiryMarksOpenReadPRs(t *testing.T) {
	capture := captureLogs(t)
	const prClosed, prUnknown = "https://github.com/acme/front/pull/3", "https://github.com/acme/front/pull/4"
	f := &fakeAPI{
		previews: map[string]*preview.Values{"feature-x": values(
			pin("backend-1", pr42, stale),
			pin("backend-2", pr7, stale),
			pin("front", prClosed, stale),
			pin("worker", prUnknown, stale),
		)},
		prOpen: map[string]bool{"acme/backend-1#42": true, "acme/backend-2#7": true},
		prErr:  map[string]error{"acme/front#4": errors.New("boom")},
	}
	expiry := newMapExpiry()
	api := f.api(t)
	api.Expiry = expiry
	if err := lifecycle.Sweep(context.Background(), api, now, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for key, want := range map[string]bool{
		"acme/backend-1#42": true, "acme/backend-2#7": true, "acme/front#3": false, "acme/front#4": false,
	} {
		if _, ok := expiry.record(key); ok != want {
			t.Errorf("record of %s = %v, want %v", key, ok, want)
		}
	}

	// A failed Mark is logged and does not fail the sweep.
	g := &fakeAPI{
		previews: map[string]*preview.Values{"feature-y": values(pin("backend-1", pr42, stale))},
		prOpen:   map[string]bool{"acme/backend-1#42": true},
	}
	api = g.api(t)
	api.Expiry = failingExpiry{}
	if err := lifecycle.Sweep(context.Background(), api, now, ttl); err != nil {
		t.Fatalf("Sweep with a failing expiry store: %v", err)
	}
	if logs := capture.find(slog.LevelWarn, "expiry mark failed"); len(logs) != 1 ||
		logs[0]["repo"].String() != "acme/backend-1" || logs[0]["pr_number"].Int64() != 42 {
		t.Errorf("mark failed logs = %v", logs)
	}
}

func TestSweep_ExpiryNotMarkedByOtherRules(t *testing.T) {
	captureLogs(t)
	f := &fakeAPI{
		previews: map[string]*preview.Values{
			"feature-x": values(preview.ServiceEntry{Name: "backend-1"}),
			"feature-y": values(pin("backend-1", pr42, recent)),
		},
		prOpen: map[string]bool{},
	}
	expiry := newMapExpiry()
	api := f.api(t)
	api.Expiry = expiry
	if err := lifecycle.Sweep(context.Background(), api, now, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(expiry.records) != 0 {
		t.Errorf("records = %v, want none (no pin and a closed PR are not TTL expiries)", expiry.records)
	}
}

func TestReconcile_DeployClearsExpiry(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "preview")}
	expiry := newMapExpiry()
	_ = expiry.Mark(context.Background(), "acme/front#42", front42, "old", now)
	api := f.api(t)
	api.Expiry = expiry
	lifecycle.ReconcilePR(context.Background(), api, event("labeled", "front", "preview", "preview"))
	if _, ok := expiry.record("acme/front#42"); ok {
		t.Error("a deploy by the label flow kept the expiry record")
	}
}

func TestReconcile_NoDeployKeepsExpiry(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "bug")}
	expiry := newMapExpiry()
	_ = expiry.Mark(context.Background(), "acme/front#42", front42, "old", now)
	api := f.api(t)
	api.Expiry = expiry
	lifecycle.ReconcilePR(context.Background(), api, event("synchronize", "front", "", "bug"))
	if _, ok := expiry.record("acme/front#42"); !ok {
		t.Error("a reconcile that deployed nothing cleared the expiry record")
	}
}

func TestDeployPR_ClearsExpiry(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{Metrics: metrics.New()}
	r := f.api(t)
	expiry := newMapExpiry()
	_ = expiry.Mark(context.Background(), "acme/front#42", front42, "old", now)
	api := lifecycle.DeployAPI{Store: r.Store, Forge: r.Forge, Recorder: r.Recorder, FanOut: r.FanOut, Expiry: expiry}
	catalog, err := f.FetchServiceCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	inputs := preview.DeployInputs{
		ServiceName: "front", CommitSha: fetchedSHA, HeadRef: "feature/pricing", PRAuthor: "octocat",
		PRURL: "https://github.com/Acme/Front/pull/42", PRNumber: "42", Timestamp: "2026-11-20T10:00:00Z",
	}
	if _, _, deployErr := lifecycle.DeployPR(
		context.Background(),
		api,
		catalog,
		"feature-pricing",
		inputs,
	); deployErr != nil {
		t.Fatalf("deploy: %v", deployErr)
	}
	if _, ok := expiry.record("acme/front#42"); ok {
		t.Error("/v1/deploy kept the expiry record (the PRKey is lowercased)")
	}

	inputs.PRURL = "not a pull request url"
	inputs.Timestamp = "2026-11-20T10:00:01Z"
	if _, _, deployErr := lifecycle.DeployPR(
		context.Background(),
		api,
		catalog,
		"feature-pricing",
		inputs,
	); deployErr != nil {
		t.Fatalf("deploy: %v", deployErr)
	}
	if logs := capture.find(slog.LevelWarn, "expiry clear skipped"); len(logs) != 1 ||
		logs[0]["pr_url"].String() != "not a pull request url" {
		t.Errorf("clear skipped logs = %v", logs)
	}
}
