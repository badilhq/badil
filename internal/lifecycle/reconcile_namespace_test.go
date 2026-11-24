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
)

// namespaceInUseBody is the refusal comment of a PR refused the preview
// pricing because its namespace is foreign.
const namespaceInUseBody = "## ⚠️ Preview: `pricing`\n\n" +
	"A namespace `pricing` already exists in the cluster and is not a preview, so badil does not deploy into it. " +
	"Rename the branch, or use a `preview:<name>` label."

// checker is a lifecycle.NamespaceChecker: the names in foreign are foreign,
// and every check fails with err when it is set. checked logs the names
// checked. With t set, a check fails t.
type checker struct {
	foreign []string
	err     error
	t       *testing.T
	checked []string
}

func (c *checker) Foreign(_ context.Context, name string) (bool, error) {
	if c.t != nil {
		c.t.Errorf("namespace %s checked, want no check", name)
	}
	c.checked = append(c.checked, name)
	if c.err != nil {
		return false, c.err
	}
	return slices.Contains(c.foreign, name), nil
}

// reconcileWith reconciles a labeled event of acme/front#42 labelled
// preview:pricing over f with the namespace check c.
func reconcileWith(t *testing.T, f *reconcileFake, c lifecycle.NamespaceChecker) lifecycle.ReconcileResult {
	t.Helper()
	api := f.api(t)
	api.Namespaces = c
	return lifecycle.ReconcilePR(context.Background(), api,
		event("labeled", "front", "preview:pricing", "preview:pricing"))
}

func TestReconcile_NamespaceInUseRefusesNewPreview(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{Metrics: metrics.New(), pr: fetchedPR("front", true, "preview:pricing")}
	c := &checker{foreign: []string{"pricing"}}
	res := reconcileWith(t, f, c)

	if res.Outcome != lifecycle.OutcomeSkipped || res.Reason != lifecycle.NamespaceInUseReason {
		t.Errorf("result = %+v, want skipped %q", res, lifecycle.NamespaceInUseReason)
	}
	if !slices.Equal(c.checked, []string{"pricing"}) {
		t.Errorf("checked = %v, want [pricing]", c.checked)
	}
	if want := []string{"apply", "upsert"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	f.assertNothingPushed(t)
	if len(f.upserts) != 1 || f.upserts[0] != namespaceInUseBody {
		t.Errorf("upserts = %q, want one with body:\n%s", f.upserts, namespaceInUseBody)
	}
	logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
	if len(logs) != 1 || logs[0]["reason"].String() != "namespace in use" || logs[0]["repo"].String() != "acme/front" {
		t.Errorf("skip logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="skipped"}`:              1,
		`badil_reconcile_skips_total{reason="namespace in use"}`: 1,
	}, "badil_reconciles_total", "badil_reconcile_skips_total")
}

// TestReconcile_NamespaceNotCheckedForExistingPreview deploys to an existing
// preview without checking its namespace.
func TestReconcile_NamespaceNotCheckedForExistingPreview(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:       fetchedPR("front", true, "preview:pricing"),
		previews: map[string]*preview.Values{"pricing": values(preview.ServiceEntry{Name: "api"})},
	}
	res := reconcileWith(t, f, &checker{t: t})

	if res.Outcome != lifecycle.OutcomeDeployed {
		t.Errorf("result = %+v, want deployed", res)
	}
	if got := deployedLogged(capture); !slices.Equal(got, []string{"pricing/front"}) {
		t.Errorf("deployed = %v, want pricing/front", got)
	}
}

// TestReconcile_NamespaceAllowed deploys a new preview whose namespace is
// not foreign.
func TestReconcile_NamespaceAllowed(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "preview:pricing")}
	res := reconcileWith(t, f, &checker{foreign: []string{"search"}})

	if res.Outcome != lifecycle.OutcomeDeployed {
		t.Errorf("result = %+v, want deployed", res)
	}
	if want := []string{"apply", "fanout"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	if got := deployedLogged(capture); !slices.Equal(got, []string{"pricing/front"}) {
		t.Errorf("deployed = %v, want pricing/front", got)
	}
}

// TestReconcile_NamespaceCheckErrorFails fails the reconcile of a new preview
// whose namespace cannot be checked, and writes nothing.
func TestReconcile_NamespaceCheckErrorFails(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{Metrics: metrics.New(), pr: fetchedPR("front", true, "preview:pricing")}
	res := reconcileWith(t, f, &checker{err: errors.New("namespaces \"pricing\" is forbidden")})

	if res.Outcome != lifecycle.OutcomeFailed {
		t.Errorf("result = %+v, want failed", res)
	}
	f.assertNothingPushed(t)
	if len(f.upserts) != 0 || len(f.fanOuts) != 0 {
		t.Errorf("upserts = %q, fanOuts = %q, want none", f.upserts, f.fanOuts)
	}
	logs := capture.find(slog.LevelError, "preview reconcile failed")
	if len(logs) != 1 ||
		logs[0]["err"].String() != `namespace check failed: namespaces "pricing" is forbidden` {
		t.Errorf("failed logs = %v", logs)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="failed"}`: 1,
	}, "badil_reconciles_total")
}

// deployWith deploys front to the preview pricing over f with the namespace
// check c.
func deployWith(t *testing.T, f *reconcileFake, c lifecycle.NamespaceChecker) error {
	t.Helper()
	r := f.api(t)
	api := lifecycle.DeployAPI{Store: r.Store, Forge: r.Forge, Recorder: r.Recorder, FanOut: r.FanOut, Namespaces: c}
	catalog, err := f.FetchServiceCatalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	inputs := preview.DeployInputs{
		ServiceName: "front", CommitSha: fetchedSHA, HeadRef: "pricing", PRAuthor: "octocat",
		PRURL: front42, PRNumber: "42", Timestamp: "2026-11-20T10:00:00Z",
	}
	_, _, deployErr := lifecycle.DeployPR(context.Background(), api, catalog, "pricing", inputs)
	return deployErr
}

func TestDeployPR_NamespaceNotCheckedForExistingPreview(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{
		Metrics:  metrics.New(),
		previews: map[string]*preview.Values{"pricing": values(preview.ServiceEntry{Name: "api"})},
	}
	if err := deployWith(t, f, &checker{t: t}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if got := f.lastCommit(t).Subject(); !strings.HasPrefix(got, "chore(preview): deploy front@fe7c4ed to pricing") {
		t.Errorf("commit = %q, want the deploy", got)
	}
}
