package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// partNames returns the service names then the dependency names of vals.
func partNames(vals preview.PreviewValues) []string {
	var out []string
	for _, s := range vals.Services {
		out = append(out, s.Name)
	}
	for _, d := range vals.Dependencies {
		out = append(out, d.Name)
	}
	return out
}

// TestWritePreviewWithRetry_Needs checks that a deploy of front on needsBadil
// writes front and what it needs, and that a deploy of payments to the same
// preview adds payments and kafka.
func TestWritePreviewWithRetry_Needs(t *testing.T) {
	catalog, err := store.ParseCatalog([]byte(needsBadil))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	deploy := func(files []string, service string) (*store.WriteResult, string) {
		t.Helper()
		s := &writeServer{files: files}
		in := sampleInputs()
		in.ServiceName = service
		res, writeErr := s.start(t).WritePreviewWithRetry(
			context.Background(), "slug", catalog, service, "abc1234567890", in, "create", "update", false,
		)
		if writeErr != nil {
			t.Fatalf("WritePreviewWithRetry %s: %v", service, writeErr)
		}
		written, _ := s.written.Load().(string)
		return res, written
	}

	first, written := deploy([]string{""}, "front")
	want := []string{"front", "backend", "postgres", "redis"}
	if got := partNames(first.Values); !slices.Equal(got, want) {
		t.Errorf("front preview = %v, want %v", got, want)
	}
	want = []string{"payments", "search", "kafka", "elasticsearch"}
	if !slices.Equal(first.Excluded, want) {
		t.Errorf("front Excluded = %v, want %v", first.Excluded, want)
	}

	second, _ := deploy([]string{written}, "payments")
	want = []string{"front", "backend", "payments", "postgres", "redis", "kafka"}
	if got := partNames(second.Values); !slices.Equal(got, want) {
		t.Errorf("front and payments preview = %v, want %v", got, want)
	}
}
