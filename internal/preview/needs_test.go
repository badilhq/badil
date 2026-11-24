package preview_test

import (
	"reflect"
	"testing"

	"github.com/badilhq/badil/internal/preview"
)

// needsCatalog is the catalog of the spec example: front needs backend,
// backend needs postgres and redis, payments needs backend, kafka and
// postgres, search needs elasticsearch.
func needsCatalog() preview.Catalog {
	dep := func(name string) preview.DependencyEntry {
		return preview.DependencyEntry{
			Name:  name,
			Chart: preview.DependencyChart{RepoURL: "oci://r/" + name, Version: "1.0.0"},
		}
	}
	return preview.Catalog{
		Services: []string{"front", "backend", "payments", "search"},
		Repos: map[string]string{
			"front":    "https://github.com/acme/front.git",
			"backend":  "https://github.com/acme/backend.git",
			"payments": "https://github.com/acme/payments.git",
			"search":   "https://github.com/acme/search.git",
		},
		Dependencies: []preview.DependencyEntry{dep("postgres"), dep("redis"), dep("kafka"), dep("elasticsearch")},
		Needs: map[string][]string{
			"front":    {"backend"},
			"backend":  {"postgres", "redis"},
			"payments": {"backend", "kafka", "postgres"},
			"search":   {"elasticsearch"},
		},
		GitopsRepoURL: "https://github.com/acme/gitops.git",
	}
}

// pinnedEntry returns a pinned entry of name.
func pinnedEntry(name string) preview.ServiceEntry {
	return preview.ServiceEntry{
		Name: name, CommitSha: "abc1234567890",
		Metadata: &preview.Metadata{PRURL: "https://github.com/acme/" + name + "/pull/1"},
	}
}

// names returns the service names then the dependency names of vals.
func names(vals preview.Values) ([]string, []string) {
	var services, deps []string
	for _, s := range vals.Services {
		services = append(services, s.Name)
	}
	for _, d := range vals.Dependencies {
		deps = append(deps, d.Name)
	}
	return services, deps
}

func TestApplyCatalog_Needs(t *testing.T) {
	removed := pinnedEntry("legacy")
	removed.RepoURL = "https://github.com/acme/legacy.git"
	tests := []struct {
		name         string
		catalog      func() preview.Catalog
		services     []preview.ServiceEntry
		wantServices []string
		wantDeps     []string
		wantExcluded []string
	}{
		{
			name: "no needs declared runs everything",
			catalog: func() preview.Catalog {
				c := needsCatalog()
				c.Needs = nil
				return c
			},
			services:     []preview.ServiceEntry{pinnedEntry("front")},
			wantServices: []string{"front", "backend", "payments", "search"},
			wantDeps:     []string{"postgres", "redis", "kafka", "elasticsearch"},
		},
		{
			name:         "single pin",
			catalog:      needsCatalog,
			services:     []preview.ServiceEntry{pinnedEntry("front")},
			wantServices: []string{"front", "backend"},
			wantDeps:     []string{"postgres", "redis"},
			wantExcluded: []string{"payments", "search", "kafka", "elasticsearch"},
		},
		{
			name:         "shared preview",
			catalog:      needsCatalog,
			services:     []preview.ServiceEntry{pinnedEntry("payments"), {Name: "search"}, pinnedEntry("front")},
			wantServices: []string{"front", "backend", "payments"},
			wantDeps:     []string{"postgres", "redis", "kafka"},
			wantExcluded: []string{"search", "elasticsearch"},
		},
		{
			name: "transitive through a dependency",
			catalog: func() preview.Catalog {
				c := needsCatalog()
				c.Needs["kafka"] = []string{"search"}
				return c
			},
			services:     []preview.ServiceEntry{pinnedEntry("payments")},
			wantServices: []string{"backend", "payments", "search"},
			wantDeps:     []string{"postgres", "redis", "kafka", "elasticsearch"},
			wantExcluded: []string{"front"},
		},
		{
			name: "cycle",
			catalog: func() preview.Catalog {
				c := needsCatalog()
				c.Needs["backend"] = []string{"front", "postgres"}
				return c
			},
			services:     []preview.ServiceEntry{pinnedEntry("backend")},
			wantServices: []string{"front", "backend"},
			wantDeps:     []string{"postgres"},
			wantExcluded: []string{"payments", "search", "redis", "kafka", "elasticsearch"},
		},
		{
			name:         "kept pinned entry of a removed service",
			catalog:      needsCatalog,
			services:     []preview.ServiceEntry{removed, {Name: "front"}},
			wantServices: []string{"legacy"},
			wantExcluded: []string{
				"front",
				"backend",
				"payments",
				"search",
				"postgres",
				"redis",
				"kafka",
				"elasticsearch",
			},
		},
		{
			name:     "unpinned preview",
			catalog:  needsCatalog,
			services: []preview.ServiceEntry{{Name: "front"}},
			wantExcluded: []string{
				"front",
				"backend",
				"payments",
				"search",
				"postgres",
				"redis",
				"kafka",
				"elasticsearch",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := tt.catalog()
			got := preview.ApplyCatalog(preview.Values{Services: tt.services}, cat)
			services, deps := names(got.Values)
			if !reflect.DeepEqual(services, tt.wantServices) || !reflect.DeepEqual(deps, tt.wantDeps) {
				t.Errorf("services/dependencies = %v/%v, want %v/%v", services, deps, tt.wantServices, tt.wantDeps)
			}
			if !reflect.DeepEqual(got.Excluded, tt.wantExcluded) {
				t.Errorf("Excluded = %v, want %v", got.Excluded, tt.wantExcluded)
			}
		})
	}
}

func TestNeedsClosure(t *testing.T) {
	cyclic := needsCatalog()
	cyclic.Needs = map[string][]string{"front": {"backend"}, "backend": {"front", "postgres"}}
	noNeeds := needsCatalog()
	noNeeds.Needs = nil
	tests := []struct {
		name    string
		catalog preview.Catalog
		roots   []string
		want    map[string]bool
	}{
		{
			name:    "transitive needs",
			catalog: needsCatalog(),
			roots:   []string{"front"},
			want:    map[string]bool{"front": true, "backend": true, "postgres": true, "redis": true},
		},
		{
			name:    "several roots",
			catalog: needsCatalog(),
			roots:   []string{"search", "payments"},
			want: map[string]bool{
				"search": true, "elasticsearch": true, "payments": true, "backend": true,
				"kafka": true, "postgres": true, "redis": true,
			},
		},
		{
			name:    "cycle followed once",
			catalog: cyclic,
			roots:   []string{"front"},
			want:    map[string]bool{"front": true, "backend": true, "postgres": true},
		},
		{
			name:    "no needs declared",
			catalog: noNeeds,
			roots:   []string{"front", "search"},
			want:    map[string]bool{"front": true, "search": true},
		},
		{name: "no roots", catalog: needsCatalog(), want: map[string]bool{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.catalog.NeedsClosure(tc.roots); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NeedsClosure(%v) = %v, want %v", tc.roots, got, tc.want)
			}
		})
	}
}
