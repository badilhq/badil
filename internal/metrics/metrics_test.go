package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/badilhq/badil/internal/metrics"
)

// assertGathered fails t unless the metrics of m named names are exactly
// expected, in the text exposition format.
func assertGathered(t *testing.T, m *metrics.Metrics, expected string, names ...string) {
	t.Helper()
	if err := testutil.GatherAndCompare(m.Gatherer(), strings.NewReader(expected), names...); err != nil {
		t.Error(err)
	}
}

func TestMetrics_Counters(t *testing.T) {
	m := metrics.New()
	m.WebhookDelivery("pull_request", "200")
	m.WebhookDelivery("unverified", "401")
	m.Reconcile("deployed", "")
	m.Reconcile("skipped", "fork pr")
	m.Reconcile("skipped", "fork pr")
	m.Reconcile("failed", "")
	m.Teardown("webhook", "unpinned")
	m.Teardown("sweep", "noop")
	m.SweepRun(false, time.Unix(1, 0))

	assertGathered(t, m, `
# HELP badil_webhook_deliveries_total GitHub webhook deliveries, by verified event and HTTP status.
# TYPE badil_webhook_deliveries_total counter
badil_webhook_deliveries_total{event="pull_request",status="200"} 1
badil_webhook_deliveries_total{event="unverified",status="401"} 1
# HELP badil_reconciles_total Pull request reconciles, by outcome.
# TYPE badil_reconciles_total counter
badil_reconciles_total{outcome="deployed"} 1
badil_reconciles_total{outcome="skipped"} 2
badil_reconciles_total{outcome="failed"} 1
# HELP badil_reconcile_skips_total Skipped pull request reconciles, by reason.
# TYPE badil_reconcile_skips_total counter
badil_reconcile_skips_total{reason="fork pr"} 2
# HELP badil_teardowns_total Preview teardowns of a pull request, by trigger and result.
# TYPE badil_teardowns_total counter
badil_teardowns_total{result="unpinned",trigger="webhook"} 1
badil_teardowns_total{result="noop",trigger="sweep"} 1
# HELP badil_sweep_runs_total In-server cleanup sweep runs, by result.
# TYPE badil_sweep_runs_total counter
badil_sweep_runs_total{result="error"} 1
`,
		"badil_webhook_deliveries_total",
		"badil_reconciles_total",
		"badil_reconcile_skips_total",
		"badil_teardowns_total",
		"badil_sweep_runs_total",
	)
	if got := testutil.ToFloat64(m.SweepLastSuccessGauge()); got != 0 {
		t.Errorf("sweep last success = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.PreviewsGauge()); got != 0 {
		t.Errorf("previews = %v, want 0", got)
	}
}

func TestMetrics_OnlySkipsCountReasons(t *testing.T) {
	m := metrics.New()
	m.Reconcile("deployed", "ignored")
	if n, err := testutil.GatherAndCount(m.Gatherer(), "badil_reconcile_skips_total"); err != nil || n != 0 {
		t.Errorf("reconcile skips = %d (err %v), want 0", n, err)
	}
}

func TestMetrics_PreviewsGauge(t *testing.T) {
	m := metrics.New()
	m.SweepListed(5)
	m.SweepDeleted("no_pin")
	m.SweepDeleted("ttl")
	m.Teardown("sweep", "deleted")
	m.Teardown("webhook", "deleted")
	m.Teardown("sweep", "unpinned")
	at := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	m.SweepRun(true, at)

	if got := testutil.ToFloat64(m.PreviewsGauge()); got != 1 {
		t.Errorf("previews = %v, want 1", got)
	}
	if got, want := testutil.ToFloat64(m.SweepLastSuccessGauge()), float64(at.Unix()); got != want {
		t.Errorf("sweep last success = %v, want %v", got, want)
	}
	assertGathered(t, m, `
# HELP badil_sweep_previews_deleted_total Previews deleted by the cleanup sweep, by rule.
# TYPE badil_sweep_previews_deleted_total counter
badil_sweep_previews_deleted_total{rule="no_pin"} 1
badil_sweep_previews_deleted_total{rule="ttl"} 1
# HELP badil_sweep_runs_total In-server cleanup sweep runs, by result.
# TYPE badil_sweep_runs_total counter
badil_sweep_runs_total{result="ok"} 1
`, "badil_sweep_previews_deleted_total", "badil_sweep_runs_total")
}

func TestMetrics_PreviewsCountedLive(t *testing.T) {
	m := metrics.New()
	m.PreviewsListed(2)
	m.PreviewCreated()
	m.Teardown("webhook", "deleted")
	m.Teardown("webhook", "unpinned")
	m.Teardown("webhook", "noop")

	assertGathered(t, m, `
# HELP badil_previews Previews in the gitops repo: listed at startup, counted on each create and delete by this server, and reset by each in-server cleanup sweep.
# TYPE badil_previews gauge
badil_previews 2
`, "badil_previews")
}

func TestMetrics_PreviewsNeverNegative(t *testing.T) {
	m := metrics.New()
	m.Teardown("webhook", "deleted")
	m.SweepDeleted("ttl")
	if got := testutil.ToFloat64(m.PreviewsGauge()); got != 0 {
		t.Errorf("previews = %v, want 0", got)
	}
	m.PreviewCreated()
	if got := testutil.ToFloat64(m.PreviewsGauge()); got != 1 {
		t.Errorf("previews = %v after a create from 0, want 1", got)
	}
}

func TestMetrics_GitopsOp(t *testing.T) {
	m := metrics.New()
	m.GitopsOp("write", "ok", 30*time.Millisecond)
	m.GitopsOp("teardown", "error", 2*time.Second)

	assertGathered(t, m, `
# HELP badil_gitops_operation_duration_seconds Duration of gitops repo writes and teardowns, retries included.
# TYPE badil_gitops_operation_duration_seconds histogram
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="0.005"} 0
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="0.01"} 0
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="0.025"} 0
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="0.05"} 0
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="0.1"} 0
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="0.25"} 0
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="0.5"} 0
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="1"} 0
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="2.5"} 1
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="5"} 1
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="10"} 1
badil_gitops_operation_duration_seconds_bucket{op="teardown",result="error",le="+Inf"} 1
badil_gitops_operation_duration_seconds_sum{op="teardown",result="error"} 2
badil_gitops_operation_duration_seconds_count{op="teardown",result="error"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="0.005"} 0
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="0.01"} 0
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="0.025"} 0
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="0.05"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="0.1"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="0.25"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="0.5"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="1"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="2.5"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="5"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="10"} 1
badil_gitops_operation_duration_seconds_bucket{op="write",result="ok",le="+Inf"} 1
badil_gitops_operation_duration_seconds_sum{op="write",result="ok"} 0.03
badil_gitops_operation_duration_seconds_count{op="write",result="ok"} 1
`, "badil_gitops_operation_duration_seconds")
}

func TestMetrics_SpinUp(t *testing.T) {
	m := metrics.New()
	m.SpinUp("backend-1", 45*time.Second)
	m.ArgoCDEventIgnored()
	m.ArgoCDEventIgnored()

	assertGathered(t, m, `
# HELP badil_preview_spinup_seconds Time from badil's gitops write to the service's Argo CD Application being synced and Healthy.
# TYPE badil_preview_spinup_seconds histogram
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="15"} 0
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="30"} 0
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="60"} 1
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="120"} 1
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="180"} 1
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="300"} 1
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="600"} 1
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="900"} 1
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="1800"} 1
badil_preview_spinup_seconds_bucket{catalog_service="backend-1",le="+Inf"} 1
badil_preview_spinup_seconds_sum{catalog_service="backend-1"} 45
badil_preview_spinup_seconds_count{catalog_service="backend-1"} 1
# HELP badil_argocd_events_total Argo CD notification events, by whether they were recorded as a spin-up.
# TYPE badil_argocd_events_total counter
badil_argocd_events_total{result="ignored"} 2
badil_argocd_events_total{result="recorded"} 1
`, "badil_preview_spinup_seconds", "badil_argocd_events_total")
}

func TestMetrics_ProcessAndGoCollectors(t *testing.T) {
	m := metrics.New()
	for _, name := range []string{"go_goroutines", "process_start_time_seconds"} {
		if n, err := testutil.GatherAndCount(m.Gatherer(), name); err != nil || n != 1 {
			t.Errorf("%s: %d samples (err %v), want 1", name, n, err)
		}
	}
}

func TestMetrics_NilIsSafe(t *testing.T) {
	var m *metrics.Metrics
	m.WebhookDelivery("ping", "200")
	m.Reconcile("skipped", "fork pr")
	m.Teardown("sweep", "deleted")
	m.SweepListed(3)
	m.PreviewsListed(3)
	m.PreviewCreated()
	m.SweepDeleted("ttl")
	m.GitopsOp("write", "ok", time.Second)
	m.SweepRun(true, time.Now())
	m.SpinUp("backend-1", time.Minute)
	m.ArgoCDEventIgnored()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("nil Handler = %d, want 404", rec.Code)
	}
	if n, err := testutil.GatherAndCount(m.Gatherer()); err != nil || n != 0 {
		t.Errorf("nil Gatherer: %d samples (err %v), want 0", n, err)
	}
}
