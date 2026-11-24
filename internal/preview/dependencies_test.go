package preview_test

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/preview"
)

// dependencyNames returns the names of deps, in order.
func dependencyNames(deps []preview.DependencyEntry) []string {
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		names = append(names, dep.Name)
	}
	return names
}

func TestApplyCatalog_Dependencies(t *testing.T) {
	const gitops = "https://github.com/acme/gitops.git"
	in := preview.Values{
		Services: []preview.ServiceEntry{{Name: "backend-1"}},
		Dependencies: []preview.DependencyEntry{
			{Name: "removed", Chart: preview.DependencyChart{RepoURL: "oci://r/removed", Version: "1"}},
			{Name: "mailpit", Chart: preview.DependencyChart{RepoURL: "oci://r/old", Version: "0"}},
		},
	}
	cat := preview.Catalog{
		Services: []string{"backend-1"}, Repos: catalogRepos(), Dependencies: sampleDependencies(),
		GitopsRepoURL: gitops,
	}

	got := preview.ApplyCatalog(in, cat).Values

	if got.GitopsRepoURL != gitops {
		t.Errorf("GitopsRepoURL = %q, want %q", got.GitopsRepoURL, gitops)
	}
	if !reflect.DeepEqual(got.Dependencies, sampleDependencies()) {
		t.Errorf("Dependencies = %#v, want the catalog's %#v", got.Dependencies, sampleDependencies())
	}
	got.Dependencies[0].ValueFiles[0] = "mutated"
	if cat.Dependencies[0].ValueFiles[0] != "deps/postgres.yaml" {
		t.Error("ApplyCatalog shares the catalog's valueFiles")
	}
	if names := dependencyNames(in.Dependencies); !reflect.DeepEqual(names, []string{"removed", "mailpit"}) {
		t.Errorf("input mutated: %v", names)
	}
}

func TestApplyCatalog_DependencyShadowedByPinnedEntry(t *testing.T) {
	md := &preview.Metadata{PRURL: "https://github.com/acme/postgres/pull/1"}
	pinnedPostgres := preview.ServiceEntry{
		Name: "postgres", RepoURL: "https://github.com/acme/postgres.git", CommitSha: "abc", Metadata: md,
	}
	in := preview.Values{Services: []preview.ServiceEntry{{Name: "backend-1"}, pinnedPostgres}}
	cat := preview.Catalog{Services: []string{"backend-1"}, Repos: catalogRepos(), Dependencies: sampleDependencies()}

	proj := preview.ApplyCatalog(in, cat)
	got := proj.Values
	if names := dependencyNames(got.Dependencies); !reflect.DeepEqual(names, []string{"mailpit"}) {
		t.Errorf("dependencies = %v, want [mailpit]: postgres is a kept pinned entry", names)
	}
	if !reflect.DeepEqual(proj.Shadowed, []string{"postgres"}) {
		t.Errorf("Shadowed = %v, want [postgres]", proj.Shadowed)
	}
	if len(got.Services) != 2 || got.Services[1].Name != "postgres" {
		t.Fatalf("services = %+v, want backend-1 then the kept postgres", got.Services)
	}

	unpinned, _, _ := preview.UnpinPR(got, parsePR(t), "acme", "postgres", 1)
	got = preview.ApplyCatalog(unpinned, cat).Values
	if names := dependencyNames(got.Dependencies); !reflect.DeepEqual(names, []string{"postgres", "mailpit"}) {
		t.Errorf("dependencies = %v, want [postgres mailpit] once the pinned entry is gone", names)
	}
}

func TestApplyHosts_Dependencies(t *testing.T) {
	in := preview.Values{Dependencies: sampleDependencies()}
	in.Dependencies[1].Host = "stale.example.com"
	urls := hostURLs()
	urls.Expose["mailpit"] = "/"

	got := preview.ApplyHosts(in, "feature-pricing", urls)
	pg, mp := got.Dependencies[0], got.Dependencies[1]
	if pg.Host != "" || pg.HostParam != "" {
		t.Errorf("postgres host, hostParam = %q, %q; want both empty (not exposed)", pg.Host, pg.HostParam)
	}
	if mp.Host != "mailpit-feature-pricing.127.0.0.1.nip.io" || mp.HostParam != "ingress.hostname" {
		t.Errorf("mailpit host, hostParam = %q, %q", mp.Host, mp.HostParam)
	}
	if in.Dependencies[0].HostParam != "postgresql.host" || in.Dependencies[1].Host != "stale.example.com" {
		t.Errorf("input mutated: %+v", in.Dependencies)
	}

	off := preview.ApplyHosts(in, "feature-pricing", preview.URLConfig{Expose: urls.Expose})
	for _, dep := range off.Dependencies {
		if dep.Host != "" || dep.HostParam != "" {
			t.Errorf("%s host, hostParam = %q, %q with URLs off; want both empty", dep.Name, dep.Host, dep.HostParam)
		}
	}
}

func TestEqual_Dependencies(t *testing.T) {
	base := preview.Values{
		GitopsRepoURL: "https://github.com/acme/gitops.git",
		Dependencies:  sampleDependencies(),
	}
	with := func(change func(v *preview.Values)) preview.Values {
		v := base
		v.Dependencies = sampleDependencies()
		change(&v)
		return v
	}
	tests := []struct {
		name string
		b    preview.Values
		want bool
	}{
		{"same", with(func(*preview.Values) {}), true},
		{"other gitopsRepoURL", with(func(v *preview.Values) { v.GitopsRepoURL = "x" }), false},
		{"other version", with(func(v *preview.Values) { v.Dependencies[0].Chart.Version = "1" }), false},
		{"other chart name", with(func(v *preview.Values) { v.Dependencies[1].Chart.Name = "x" }), false},
		{"other host", with(func(v *preview.Values) { v.Dependencies[1].Host = "x" }), false},
		{"other hostParam", with(func(v *preview.Values) { v.Dependencies[1].HostParam = "x" }), false},
		{"other valueFiles", with(func(v *preview.Values) { v.Dependencies[0].ValueFiles = nil }), false},
		{
			"other values",
			with(func(v *preview.Values) { v.Dependencies[0].Values = map[string]any{"a": 1} }),
			false,
		},
		{"no values", with(func(v *preview.Values) { v.Dependencies[0].Values = nil }), false},
		{"reordered", with(func(v *preview.Values) {
			v.Dependencies[0], v.Dependencies[1] = v.Dependencies[1], v.Dependencies[0]
		}), false},
		{"removed", with(func(v *preview.Values) { v.Dependencies = v.Dependencies[:1] }), false},
		{
			"empty and nil values",
			with(func(v *preview.Values) { v.Dependencies[1].Values = map[string]any{} }),
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := preview.Equal(base, tt.b); got != tt.want {
				t.Errorf("Equal = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEqual_DependencyValuesReadBack checks that values parsed from
// badil.yaml equal the same values written to a preview file and read back,
// so that a sync does not rewrite an unchanged file.
func TestEqual_DependencyValuesReadBack(t *testing.T) {
	deps := sampleDependencies()
	deps[0].Values = map[string]any{"replicas": 1.0, "ratio": 0.5, "on": "yes", "list": []any{1, "2"}}
	vals := preview.Values{APIVersion: preview.APIVersion, Dependencies: deps}

	var back preview.Values
	if err := yaml.Unmarshal(encodeValues(t, vals), &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !preview.Equal(vals, back) {
		t.Errorf("values read back differ: %#v, want %#v", back.Dependencies[0].Values, deps[0].Values)
	}
}

func TestDependencyChart_ChartName(t *testing.T) {
	for chart, want := range map[preview.DependencyChart]string{
		{RepoURL: "oci://registry-1.docker.io/bitnamicharts/postgresql"}: "postgresql",
		{RepoURL: "oci://ghcr.io/acme/charts/redis/"}:                    "redis",
		{RepoURL: "https://jouve.github.io/charts", Name: "mailpit"}:     "mailpit",
	} {
		if got := chart.ChartName(); got != want {
			t.Errorf("ChartName(%+v) = %q, want %q", chart, got, want)
		}
	}
}
