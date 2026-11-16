package preview_test

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

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
func names(vals preview.PreviewValues) ([]string, []string) {
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
			got := preview.ApplyCatalog(preview.PreviewValues{Services: tt.services}, cat)
			services, deps := names(got)
			if !reflect.DeepEqual(services, tt.wantServices) || !reflect.DeepEqual(deps, tt.wantDeps) {
				t.Errorf("services/dependencies = %v/%v, want %v/%v", services, deps, tt.wantServices, tt.wantDeps)
			}
			if excluded := cat.Excluded(got); !reflect.DeepEqual(excluded, tt.wantExcluded) {
				t.Errorf("Excluded = %v, want %v", excluded, tt.wantExcluded)
			}
		})
	}
}

// TestGoldenYAML_Needs pins the YAML emission of a deploy of front on the
// needs catalog to testdata/values-needs.golden.yaml (rewritten with -update).
func TestGoldenYAML_Needs(t *testing.T) {
	in := sampleInputs()
	in.ServiceName = "front"
	in.PRURL = "https://github.com/acme/front/pull/42"
	cat := needsCatalog()
	vals := preview.ApplyCatalog(preview.BuildPreviewValues(in.ServiceName, in.CommitSha, cat.Services, in), cat)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(vals); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_ = enc.Close()

	const path = "testdata/values-needs.golden.yaml"
	if *updateGolden {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("yaml output drift\n--- want (golden) ---\n%s\n--- got ---\n%s", want, buf.Bytes())
	}
}

func TestBuildSummary_Excluded(t *testing.T) {
	const line = "Not in this preview: `payments`, `search`, `kafka`, `elasticsearch`\n"
	excluded := []string{"payments", "search", "kafka", "elasticsearch"}
	deps := []preview.DependencyEntry{
		{Name: "postgres", Chart: preview.DependencyChart{RepoURL: "oci://r/postgresql", Version: "1.0.0"}},
	}
	tests := []struct {
		name     string
		services []preview.ServiceEntry
		excluded []string
		want     string
	}{
		{
			name:     "with the pinned-image note",
			services: []preview.ServiceEntry{{Name: "front", CommitSha: "abc1234567890"}, {Name: "backend"}},
			excluded: excluded,
			want: "## 🚀 Preview: `slug`\n\n" +
				"- **front** · 📌 `abc12345`\n" +
				"- **backend** · 🔄 `main`\n" +
				"- **postgres** · 📦 `postgresql` `1.0.0`\n" +
				"\n" + line +
				"\n" + pinnedNote + "\n" +
				"\n---\n🔗 [m](u)",
		},
		{
			name:     "without the pinned-image note",
			services: []preview.ServiceEntry{{Name: "front"}, {Name: "backend"}},
			excluded: excluded,
			want: "## 🚀 Preview: `slug`\n\n" +
				"- **front** · 🔄 `main`\n" +
				"- **backend** · 🔄 `main`\n" +
				"- **postgres** · 📦 `postgresql` `1.0.0`\n" +
				"\n" + line +
				"\n---\n🔗 [m](u)",
		},
		{
			name:     "nothing excluded",
			services: []preview.ServiceEntry{{Name: "front"}, {Name: "backend"}},
			want: "## 🚀 Preview: `slug`\n\n" +
				"- **front** · 🔄 `main`\n" +
				"- **backend** · 🔄 `main`\n" +
				"- **postgres** · 📦 `postgresql` `1.0.0`\n" +
				"\n---\n🔗 [m](u)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := preview.PreviewValues{Services: tt.services, Dependencies: deps}
			got := preview.BuildSummary("slug", vals, preview.URLConfig{}, "", nil, "m", "u", tt.excluded...)
			if got != tt.want {
				t.Errorf("summary mismatch\n got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
