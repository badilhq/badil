package main

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"
)

// at returns the value at the keys of path in the decoded JSON v.
func at(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v: not an object at %q", path, k)
		}
		v = m[k]
	}
	return v
}

func TestGenerate_KeyConstraints(t *testing.T) {
	b, err := generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if b[len(b)-1] != '\n' {
		t.Error("schema does not end with a newline")
	}
	var s map[string]any
	if err = json.Unmarshal(b, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	props, ok := at(t, s, "properties").(map[string]any)
	if !ok {
		t.Fatal("properties is not an object")
	}
	keys := slices.Sorted(maps.Keys(props))
	entry := at(t, s, "properties", "services", "items")
	metadata := at(t, entry, "properties", "metadata")
	chart := at(t, entry, "properties", "chart")
	ns := at(t, s, "properties", "namespace")
	dep := at(t, s, "properties", "dependencies", "items")
	depChart := at(t, dep, "properties", "chart")
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"no $id", s["$id"], nil},
		{"draft", s["$schema"], "https://json-schema.org/draft/2020-12/schema"},
		{
			"root properties",
			keys,
			[]string{"apiVersion", "argocdNamespace", "dependencies", "gitopsRepoURL", "namespace", "services"},
		},
		{
			"root required",
			s["required"],
			[]any{"apiVersion", "gitopsRepoURL", "services", "dependencies", "argocdNamespace"},
		},
		{"gitopsRepoURL non-empty", at(t, s, "properties", "gitopsRepoURL", "minLength"), 1.0},
		{"root closed", s["additionalProperties"], false},
		{"apiVersion const", at(t, s, "properties", "apiVersion", "const"), "badil.dev/v1beta1"},
		{
			"argocdNamespace pattern",
			at(t, s, "properties", "argocdNamespace", "pattern"),
			"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$",
		},
		{"service entry closed", at(t, entry, "additionalProperties"), false},
		{
			"service entry required",
			at(t, entry, "required"),
			[]any{"name", "repoURL", "branch", "imageTag", "chart"},
		},
		{"imageTag non-empty", at(t, entry, "properties", "imageTag", "minLength"), 1.0},
		{
			"service entry dependentRequired",
			at(t, entry, "dependentRequired"),
			map[string]any{"commitSha": []any{"metadata"}, "metadata": []any{"commitSha"}},
		},
		{"metadata closed", at(t, metadata, "additionalProperties"), false},
		{
			"metadata required",
			at(t, metadata, "required"),
			[]any{
				"pr-author", "pr-url", "pr-number", "created-at", "updated-at", "vcs.ref.name",
				"cicd.pipeline.run.url",
			},
		},
		{"metadata not nullable", at(t, metadata, "type"), "object"},
		{"chart closed", at(t, chart, "additionalProperties"), false},
		{"chart type", at(t, chart, "type"), "object"},
		{"chart required", at(t, chart, "required"), []any{"path", "imageTagParam", "hostParam"}},
		{"chart path non-empty", at(t, chart, "properties", "path", "minLength"), 1.0},
		{"chart imageTagParam non-empty", at(t, chart, "properties", "imageTagParam", "minLength"), 1.0},
		{"chart hostParam non-empty", at(t, chart, "properties", "hostParam", "minLength"), 1.0},
		{"valueFiles non-empty", at(t, chart, "properties", "valueFiles", "minItems"), 1.0},
		{"valueFiles items non-empty", at(t, chart, "properties", "valueFiles", "items", "minLength"), 1.0},
		{"namespace closed", at(t, ns, "additionalProperties"), false},
		{"namespace type", at(t, ns, "type"), "object"},
		{"namespace optional", at(t, ns, "required"), nil},
		{
			"labels values",
			at(t, ns, "properties", "labels", "additionalProperties"),
			map[string]any{"type": "string"},
		},
		{
			"labels reserved",
			at(t, ns, "properties", "labels", "properties"),
			map[string]any{
				"app.kubernetes.io/name": false, "app.kubernetes.io/instance": false,
				"app.kubernetes.io/managed-by": false, "badil.dev/preview": false,
			},
		},
		{"labels no propertyNames", at(t, ns, "properties", "labels", "propertyNames"), nil},
		{
			"annotations reserved",
			at(t, ns, "properties", "annotations", "properties"),
			map[string]any{"argocd.argoproj.io/sync-wave": false},
		},
		{"annotations no propertyNames", at(t, ns, "properties", "annotations", "propertyNames"), nil},
		{"dependency closed", at(t, dep, "additionalProperties"), false},
		{"dependency required", at(t, dep, "required"), []any{"name", "chart"}},
		{
			"dependency dependentRequired",
			at(t, dep, "dependentRequired"),
			map[string]any{"host": []any{"hostParam"}, "hostParam": []any{"host"}},
		},
		{"dependency values free-form", at(t, dep, "properties", "values"), map[string]any{"type": "object"}},
		{"dependency valueFiles non-empty", at(t, dep, "properties", "valueFiles", "minItems"), 1.0},
		{"dependency chart closed", at(t, depChart, "additionalProperties"), false},
		{"dependency chart required", at(t, depChart, "required"), []any{"repoURL", "version"}},
		{"dependency chart repoURL pattern", at(t, depChart, "properties", "repoURL", "pattern"), "^(oci|https)://"},
		{
			"dependency chart name without oci",
			[]any{at(t, depChart, "if"), at(t, depChart, "then"), at(t, depChart, "else")},
			[]any{
				map[string]any{"properties": map[string]any{"repoURL": map[string]any{"pattern": "^oci://"}}},
				map[string]any{"properties": map[string]any{"name": false}},
				map[string]any{"required": []any{"name"}},
			},
		},
	}
	for _, tt := range tests {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Errorf("%s = %#v, want %#v", tt.name, tt.got, tt.want)
		}
	}
}

func TestGenerateContract_RootConstraints(t *testing.T) {
	b, err := generateContract()
	if err != nil {
		t.Fatalf("generateContract: %v", err)
	}
	if b[len(b)-1] != '\n' {
		t.Error("schema does not end with a newline")
	}
	var s map[string]any
	if err = json.Unmarshal(b, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props, ok := at(t, s, "properties").(map[string]any)
	if !ok {
		t.Fatal("properties is not an object")
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"no $id", s["$id"], nil},
		{"draft", s["$schema"], "https://json-schema.org/draft/2020-12/schema"},
		{"root properties", keys, []string{"apiVersion", "dependencies", "gitopsRepoURL", "namespace", "services"}},
		{"root required", s["required"], []any{"apiVersion", "gitopsRepoURL", "services", "dependencies"}},
		{"root closed", s["additionalProperties"], false},
		{"apiVersion const", at(t, s, "properties", "apiVersion", "const"), "badil.dev/v1beta1"},
		{"service entry closed", at(t, s, "properties", "services", "items", "additionalProperties"), false},
		{"dependency closed", at(t, s, "properties", "dependencies", "items", "additionalProperties"), false},
	}
	for _, tt := range tests {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Errorf("%s = %#v, want %#v", tt.name, tt.got, tt.want)
		}
	}
}
