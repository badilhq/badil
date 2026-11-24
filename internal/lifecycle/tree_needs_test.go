package lifecycle_test

import (
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// partNames returns the service names then the dependency names of vals.
func partNames(vals preview.Values) []string {
	var out []string
	for _, s := range vals.Services {
		out = append(out, s.Name)
	}
	for _, d := range vals.Dependencies {
		out = append(out, d.Name)
	}
	return out
}

// TestDeploy_Needs checks that a deploy of front on needsBadil
// writes front and what it needs, and that a deploy of payments to the same
// preview adds payments and kafka.
func TestDeploy_Needs(t *testing.T) {
	catalog, err := store.ParseCatalog([]byte(needsBadil))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	deployOn := func(files []string, service string) (*lifecycle.WriteResult, string) {
		t.Helper()
		s := &writeServer{files: files}
		in := sampleInputs()
		in.ServiceName = service
		res, _, writeErr := deploy(s.start(t), "slug", catalog, service, "abc1234567890", in, false)
		if writeErr != nil {
			t.Fatalf("Apply %s: %v", service, writeErr)
		}
		return res, s.written()
	}

	first, written := deployOn([]string{""}, "front")
	want := []string{"front", "backend", "postgres", "redis"}
	if got := partNames(first.Values); !slices.Equal(got, want) {
		t.Errorf("front preview = %v, want %v", got, want)
	}
	want = []string{"payments", "search", "kafka", "elasticsearch"}
	if !slices.Equal(first.Excluded, want) {
		t.Errorf("front Excluded = %v, want %v", first.Excluded, want)
	}

	second, _ := deployOn([]string{written}, "payments")
	want = []string{"front", "backend", "payments", "postgres", "redis", "kafka"}
	if got := partNames(second.Values); !slices.Equal(got, want) {
		t.Errorf("front and payments preview = %v, want %v", got, want)
	}
}

// needsBadil is a badil.yaml whose services declare needs.
const needsBadil = `services:
  front: {repo: https://github.com/acme/front.git, needs: [backend]}
  backend: {repo: https://github.com/acme/backend.git, needs: [postgres, redis]}
  payments: {repo: https://github.com/acme/payments.git, needs: [backend, kafka, postgres]}
  search: {repo: https://github.com/acme/search.git, needs: [elasticsearch]}
dependencies:
  postgres: {chart: oci://r/postgresql, version: 1.0.0}
  redis: {chart: oci://r/redis, version: 1.0.0}
  kafka: {chart: oci://r/kafka, version: 1.0.0}
  elasticsearch: {chart: oci://r/elasticsearch, version: 1.0.0}
`
