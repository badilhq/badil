package main

import (
	"encoding/json"
	"reflect"
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

	entry := at(t, s, "properties", "services", "items")
	metadata := at(t, entry, "properties", "metadata")
	chart := at(t, s, "properties", "serviceCharts", "additionalProperties")
	ns := at(t, s, "properties", "previewNamespace")
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"no $id", s["$id"], nil},
		{"draft", s["$schema"], "https://json-schema.org/draft/2020-12/schema"},
		{"root required", s["required"], []any{"apiVersion", "services", "argocdNamespace"}},
		{"root open", s["additionalProperties"], true},
		{"apiVersion const", at(t, s, "properties", "apiVersion", "const"), "badil.dev/v1"},
		{
			"argocdNamespace pattern",
			at(t, s, "properties", "argocdNamespace", "pattern"),
			"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$",
		},
		{"service entry closed", at(t, entry, "additionalProperties"), false},
		{"service entry required", at(t, entry, "required"), []any{"name", "repoURL", "branch"}},
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
		{"service chart closed", at(t, chart, "additionalProperties"), false},
		{"service chart type", at(t, chart, "type"), "object"},
		{"service chart optional", at(t, chart, "required"), nil},
		{"service chart path non-empty", at(t, chart, "properties", "path", "minLength"), 1.0},
		{"valueFiles items non-empty", at(t, chart, "properties", "valueFiles", "items", "minLength"), 1.0},
		{"previewNamespace closed", at(t, ns, "additionalProperties"), false},
		{"previewNamespace type", at(t, ns, "type"), "object"},
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
				"app.kubernetes.io/part-of": false, "app.kubernetes.io/managed-by": false,
			},
		},
		{"labels no propertyNames", at(t, ns, "properties", "labels", "propertyNames"), nil},
		{
			"annotations reserved",
			at(t, ns, "properties", "annotations", "properties"),
			map[string]any{"argocd.argoproj.io/sync-wave": false},
		},
		{"annotations no propertyNames", at(t, ns, "properties", "annotations", "propertyNames"), nil},
	}
	for _, tt := range tests {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Errorf("%s = %#v, want %#v", tt.name, tt.got, tt.want)
		}
	}
}
