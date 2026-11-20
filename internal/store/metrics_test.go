package store_test

import (
	"errors"
	"maps"
	"testing"

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

	fail := func(*store.Tree) (store.Summary, error) { return store.Summary{}, errors.New("boom") }
	noop := func(*store.Tree) (store.Summary, error) { return store.Summary{Subject: "noop"}, nil }
	for _, op := range []struct {
		kind   string
		change func(*store.Tree) (store.Summary, error)
	}{
		{store.OpDeploy, rename("a", "deployed", "deploy")},
		{store.OpTeardown, fail},
		{store.OpTeardown, noop},
		{store.OpSweep, rename("a", "swept", "sweep")},
		{store.OpSync, fail},
		{store.OpSync, noop},
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
