package store_test

import (
	"context"
	"maps"
	"testing"

	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/store"
)

func TestClientMetrics_TimesRetryOperations(t *testing.T) {
	m := metrics.New()
	f := newGitopsFake(t, map[string]string{"previews/broken/values.yaml": "services: [\n"})
	c := f.open(t, store.WithMetrics(m))
	// The clone made when the store opened is timed too.
	want := map[string]uint64{"clone/ok": 1}

	ctx := context.Background()
	if _, err := c.WritePreviewWithRetry(ctx, "fresh", testCatalog("backend-1"),
		"backend-1", "abc", sampleInputs(), "create", "update", false); err != nil {
		t.Fatalf("WritePreviewWithRetry: %v", err)
	}
	if _, err := c.TeardownPreviewWithRetry(ctx, "broken", "acme", "backend-1", 42); err == nil {
		t.Fatal("TeardownPreviewWithRetry: want an error")
	}
	if _, err := c.TeardownPreviewWithRetry(ctx, "gone", "acme", "backend-1", 42); err != nil {
		t.Fatalf("TeardownPreviewWithRetry: %v", err)
	}
	if _, err := c.UnpinServicesWithRetry(ctx, "broken", "acme", "backend-1", 42, []string{"backend-1"}); err == nil {
		t.Fatal("UnpinServicesWithRetry: want an error")
	}
	if _, err := c.UnpinServicesWithRetry(ctx, "gone", "acme", "backend-1", 42, []string{"backend-1"}); err != nil {
		t.Fatalf("UnpinServicesWithRetry: %v", err)
	}
	if _, err := c.SyncPreviewWithRetry(ctx, "broken", testCatalog("backend-1")); err == nil {
		t.Fatal("SyncPreviewWithRetry: want an error")
	}
	if _, err := c.SyncPreviewWithRetry(ctx, "gone", testCatalog("backend-1")); err != nil {
		t.Fatalf("SyncPreviewWithRetry: %v", err)
	}

	// Durations vary, so only each operation's observation count is compared.
	families, err := m.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	got := map[string]uint64{}
	for _, mf := range families {
		if mf.GetName() != "badil_gitops_operation_duration_seconds" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range metric.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			got[labels["op"]+"/"+labels["result"]] = metric.GetHistogram().GetSampleCount()
		}
	}
	want["write/ok"], want["teardown/error"], want["teardown/ok"], want["sync/error"], want["sync/ok"] = 1, 2, 2, 1, 1
	// The first read after the write checks the remote, whose head the write
	// moved.
	want["ls_remote/ok"], want["fetch/ok"] = 1, 1
	if !maps.Equal(got, want) {
		t.Errorf("gitops operation counts = %v, want %v", got, want)
	}
}
