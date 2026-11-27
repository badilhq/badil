package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// expiryRecord is a record of mapExpiry.
type expiryRecord struct {
	prURL, sha string
	at         time.Time
}

// mapExpiry is a map-backed lifecycle.ExpiryStore.
type mapExpiry struct {
	mu      sync.Mutex
	records map[string]expiryRecord
}

func newMapExpiry() *mapExpiry { return &mapExpiry{records: map[string]expiryRecord{}} }

func (m *mapExpiry) Mark(_ context.Context, key, prURL, headSHA string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[key] = expiryRecord{prURL: prURL, sha: headSHA, at: at}
	return nil
}

func (m *mapExpiry) Recorded(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[key]
	return r.sha, ok, nil
}

func (m *mapExpiry) Clear(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key)
	return nil
}

func (m *mapExpiry) Prune(_ context.Context, keep func(string) bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.records {
		if !keep(k) {
			delete(m.records, k)
		}
	}
	return nil
}

// record returns the record of key.
func (m *mapExpiry) record(key string) (expiryRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[key]
	return r, ok
}

// expiryFake is a reconcileFake that also lists the previews, so that it
// serves as the store of a sweep too.
type expiryFake struct{ *reconcileFake }

func (f expiryFake) ListPreviews(ctx context.Context) ([]string, error) {
	return f.g.st.ListPreviews(ctx)
}

// catchUpInt returns the attribute key of the last catch-up line logged to
// capture, failing t when it is missing.
func catchUpInt(t *testing.T, capture *logCapture, key string) int64 {
	t.Helper()
	logs := capture.find(slog.LevelInfo, "catch-up")
	if len(logs) == 0 {
		t.Fatal("no catch-up line logged")
	}
	v, ok := logs[len(logs)-1][key]
	if !ok {
		t.Fatalf("catch-up line has no %s: %v", key, logs[len(logs)-1])
	}
	return v.Int64()
}

func TestCatchUp_SkipsTTLExpiredPRAtSameHead(t *testing.T) {
	capture := captureLogs(t)
	pr := listed(1, "s1", "preview")
	expiry := newMapExpiry()
	key := lifecycle.PRKey(lifecycle.Target{Owner: "acme", Repo: "front", Number: 1})
	state := pr
	f := &reconcileFake{
		Metrics: metrics.New(),
		previews: map[string]*preview.Values{"feature-1": values(preview.ServiceEntry{
			Name:      "front",
			CommitSha: "s1",
			Metadata:  &preview.Metadata{PRURL: pr.URL, UpdatedAt: stale},
		})},
		openPRs: map[string][]forge.PullRequest{"acme/front": {pr}},
		pr:      &state,
	}
	f.open(t)
	sweepAPI := lifecycle.SweepAPI{
		Store: expiryFake{f}, Forge: f, Recorder: f, FanOut: f.fanOut, Expiry: expiry,
	}
	if err := lifecycle.Sweep(context.Background(), sweepAPI, now, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	newCatchUp := func(r *reconciles) *lifecycle.CatchUp {
		return lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f, Expiry: expiry}, r.reconcile)
	}

	// Case 1: right after the sweep, at the same head, catch-up leaves it.
	r := &reconciles{}
	newCatchUp(r).Run(context.Background())
	if got := r.take(""); len(got) != 0 {
		t.Fatalf("case 1: reconciled = %v, want none (the TTL-expired PR must stay down)", got)
	}
	if _, _, err := f.g.st.GetPreview(context.Background(), "feature-1"); !errors.Is(err, lifecycle.ErrNotFound) {
		t.Errorf("case 1: preview feature-1 read err = %v, want ErrNotFound", err)
	}
	if got := catchUpInt(t, capture, "reconciled"); got != 0 {
		t.Errorf("case 1: catch-up reconciled = %d, want 0", got)
	}
	if got := catchUpInt(t, capture, "skipped_expired"); got != 1 {
		t.Errorf("case 1: catch-up skipped_expired = %d, want 1", got)
	}
	if rec, ok := expiry.record(key); !ok || rec.sha != "s1" || rec.prURL != pr.URL || !rec.at.Equal(now) {
		t.Errorf("case 1: record of %s = %+v (ok %v), want s1, %s, %v", key, rec, ok, pr.URL, now)
	}

	// Case 3: a fresh CatchUp, as after a restart, still
	// skips it at the same head.
	r = &reconciles{}
	newCatchUp(r).Run(context.Background())
	if got := r.take(""); len(got) != 0 {
		t.Fatalf("case 3: reconciled = %v, want none", got)
	}
	if got := catchUpInt(t, capture, "skipped_expired"); got != 1 {
		t.Errorf("case 3: catch-up skipped_expired = %d, want 1", got)
	}

	// Case 2: listed at a new head, it is reconciled and its record cleared.
	f.openPRs["acme/front"] = []forge.PullRequest{listed(1, "s2", "preview")}
	r = &reconciles{}
	newCatchUp(r).Run(context.Background())
	if got := r.take(""); len(got) != 1 || got[0] != 1 {
		t.Fatalf("case 2: reconciled = %v, want [1]", got)
	}
	if _, ok := expiry.record(key); ok {
		t.Errorf("case 2: record of %s kept, want cleared", key)
	}
}
