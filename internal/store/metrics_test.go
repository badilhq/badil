package store_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/store"
)

// TestClientMetrics_TimesApplyByKind checks each Apply is timed once, as its
// operation kind, with its result.
func TestClientMetrics_TimesApplyByKind(t *testing.T) {
	m := metrics.New()
	f := newGitopsFake(t, map[string]string{"previews/a/values.yaml": previewFile("a")})
	c := f.open(t, store.WithMetrics(m))
	// The clone made when the store opened is timed too.
	want := map[string]uint64{"clone/ok": 1}

	fail := func(lifecycle.Tree) (lifecycle.Summary, error) { return lifecycle.Summary{}, errors.New("boom") }
	noop := func(lifecycle.Tree) (lifecycle.Summary, error) { return lifecycle.Summary{Subject: "noop"}, nil }
	for _, op := range []struct {
		kind   string
		change func(lifecycle.Tree) (lifecycle.Summary, error)
	}{
		{lifecycle.OpDeploy, rename("a", "deployed", "deploy")},
		{lifecycle.OpTeardown, fail},
		{lifecycle.OpTeardown, noop},
		{lifecycle.OpSweep, rename("a", "swept", "sweep")},
		{lifecycle.OpSync, fail},
		{lifecycle.OpSync, noop},
	} {
		_, _ = apply(c, op.kind, op.change)
	}

	// Durations vary, so only each operation's observation count is compared.
	// The fetches of every attempt are timed as fetch.
	got := opCounts(t, m)
	if got["fetch/ok"] == 0 {
		t.Errorf("fetches = 0, want Apply's fetches timed")
	}
	delete(got, "ls_remote/ok")
	delete(got, "fetch/ok")
	maps.Copy(want, map[string]uint64{
		"deploy/ok": 1, "teardown/error": 1, "teardown/ok": 1, "sweep/ok": 1, "sync/error": 1, "sync/ok": 1,
	})
	if !maps.Equal(got, want) {
		t.Errorf("gitops operation counts = %v, want %v", got, want)
	}
}

// TestWithMetrics_NilRecordsNothing checks a store given an untyped nil or a
// nil *metrics.Metrics applies a deploy without recording or panicking.
func TestWithMetrics_NilRecordsNothing(t *testing.T) {
	for name, opt := range map[string]store.Option{
		"untyped nil":          store.WithMetrics(nil),
		"nil *metrics.Metrics": store.WithMetrics((*metrics.Metrics)(nil)),
	} {
		t.Run(name, func(t *testing.T) {
			f := newGitopsFake(t, map[string]string{"previews/a/values.yaml": previewFile("a")})
			c := f.open(t, opt)
			if _, err := apply(c, lifecycle.OpDeploy, rename("a", "deployed", "deploy")); err != nil {
				t.Fatalf("deploy Apply: %v", err)
			}
		})
	}
}
