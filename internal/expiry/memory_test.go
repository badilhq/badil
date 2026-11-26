package expiry_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/expiry"
)

func TestMemory(t *testing.T) {
	ctx := context.Background()
	m := expiry.NewMemory()
	if _, ok, err := m.Recorded(ctx, "acme/front#1"); ok || err != nil {
		t.Fatalf("Recorded on empty = ok %v, err %v", ok, err)
	}
	if err := m.Clear(ctx, "acme/front#1"); err != nil {
		t.Fatalf("Clear of a missing record: %v", err)
	}
	_ = m.Mark(ctx, "acme/front#1", "https://github.com/acme/front/pull/1", "s1", time.Now())
	_ = m.Mark(ctx, "acme/front#1", "https://github.com/acme/front/pull/1", "s2", time.Now())
	_ = m.Mark(ctx, "acme/api#2", "https://github.com/acme/api/pull/2", "a1", time.Now())
	if sha, ok, _ := m.Recorded(ctx, "acme/front#1"); !ok || sha != "s2" {
		t.Errorf("Recorded = %q, %v, want s2 (the last Mark)", sha, ok)
	}
	_ = m.Prune(ctx, func(key string) bool { return key != "acme/api#2" })
	if _, ok, _ := m.Recorded(ctx, "acme/api#2"); ok {
		t.Error("Prune kept a record keep rejected")
	}
	_ = m.Clear(ctx, "acme/front#1")
	if _, ok, _ := m.Recorded(ctx, "acme/front#1"); ok {
		t.Error("Clear kept the record")
	}
}

func TestMemory_Concurrent(_ *testing.T) {
	ctx := context.Background()
	m := expiry.NewMemory()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			key := "acme/front#" + string(rune('a'+i))
			_ = m.Mark(ctx, key, "", "s", time.Now())
			_, _, _ = m.Recorded(ctx, key)
			_ = m.Prune(ctx, func(string) bool { return true })
			_ = m.Clear(ctx, key)
		})
	}
	wg.Wait()
}
