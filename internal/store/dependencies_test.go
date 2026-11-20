package store_test

import (
	"context"
	"log/slog"
	"maps"
	"testing"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// withDependencies sets on c the dependencies postgres (an OCI chart with
// values and a values file) and mailpit (a chart of a Helm repository,
// exposed), in that order.
func withDependencies(c *store.ServiceCatalog) {
	c.Dependencies = []preview.DependencyEntry{
		{
			Name: "postgres",
			Chart: preview.DependencyChart{
				RepoURL: "oci://registry-1.docker.io/bitnamicharts/postgresql", Version: "16.4.2",
			},
			Values:     map[string]any{"auth": map[string]any{"password": "preview"}, "replicas": 1.0},
			ValueFiles: []string{"deps/postgres.yaml"},
		},
		{
			Name: "mailpit",
			Chart: preview.DependencyChart{
				RepoURL: "https://jouve.github.io/charts",
				Name:    "mailpit",
				Version: "0.20.0",
			},
			HostParam: "ingress.hostname",
		},
	}
	c.URLs.Expose["mailpit"] = "/"
}

// dependenciesYAML is the dependencies list withDependencies writes in the
// preview of slug "slug".
const dependenciesYAML = "  - name: postgres\n" +
	"    chart:\n" +
	"      repoURL: oci://registry-1.docker.io/bitnamicharts/postgresql\n" +
	"      version: 16.4.2\n" +
	"    values:\n" +
	"      auth:\n" +
	"        password: preview\n" +
	"      replicas: 1\n" +
	"    valueFiles:\n" +
	"      - deps/postgres.yaml\n" +
	"  - name: mailpit\n" +
	"    chart:\n" +
	"      repoURL: https://jouve.github.io/charts\n" +
	"      name: mailpit\n" +
	"      version: 0.20.0\n" +
	"    hostParam: ingress.hostname\n" +
	"    host: mailpit-slug.127.0.0.1.nip.io\n"

// withDependenciesFile is pinnedFile with the dependencies of
// withDependencies.
func withDependenciesFile() string {
	return pinnedFile("dependencies: []\n", "dependencies:\n"+dependenciesYAML)
}

func TestWritePreviewWithRetry_Dependencies(t *testing.T) {
	s := &writeServer{}
	c := s.start(t)
	catalog := testCatalog("backend-1", "backend-2")
	withDependencies(catalog)

	result, err := c.WritePreviewWithRetry(
		context.Background(), "slug", catalog, "backend-1", "abc1234567890", sampleInputs(), "create", "update", false,
	)
	if err != nil {
		t.Fatalf("WritePreviewWithRetry: %v", err)
	}
	got := decode(t, s.written())
	want := got
	want.GitopsRepoURL = "https://github.com/acme/gitops.git"
	want.Dependencies = decode(t, withDependenciesFile()).Dependencies
	if !preview.Equal(got, want) {
		t.Errorf("written gitopsRepoURL, dependencies = %q, %+v; want the gitops repo and %+v",
			got.GitopsRepoURL, got.Dependencies, want.Dependencies)
	}
	if !preview.Equal(result.Values, got) {
		t.Errorf("result Values = %+v, want the written %+v", result.Values, got)
	}
}

// recordHandler is a slog.Handler keeping every record.
type recordHandler struct {
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordHandler) WithGroup(string) slog.Handler      { return h }

// TestSyncPreviewWithRetry_ShadowedDependency checks that a dependency named
// like a kept pinned entry of a removed service is left out with a warning,
// and written once that entry is unpinned.
func TestSyncPreviewWithRetry_ShadowedDependency(t *testing.T) {
	logs := &recordHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// backend-2 is pinned by a PR, then removed from the catalog and declared
	// as a dependency.
	file := pinnedFile(
		"    branch: main\n    imageTag: ${ARGOCD_APP_REVISION}\n",
		"    branch: main\n    commitSha: bbb\n    imageTag: bbb\n",
		"      hostParam: app.ingress.host\ndependencies: []\n",
		"      hostParam: app.ingress.host\n    metadata:\n      pr-url: https://github.com/acme/backend-2/pull/7\n"+
			"dependencies: []\n",
	)
	s := &writeServer{files: []string{file}}
	c := s.start(t)
	catalog := testCatalog("backend-1")
	catalog.Dependencies = []preview.DependencyEntry{
		{Name: "backend-2", Chart: preview.DependencyChart{RepoURL: "oci://r/backend-2", Version: "1.0.0"}},
	}

	result, err := c.SyncPreviewWithRetry(context.Background(), "slug", catalog)
	if err != nil {
		t.Fatalf("SyncPreviewWithRetry: %v", err)
	}
	if result.Changed {
		t.Errorf("Changed = true, want the file kept without the dependency:\n%s", s.written())
	}
	if len(logs.records) != 1 {
		t.Fatalf("logged %d records, want 1 warning", len(logs.records))
	}
	r := logs.records[0]
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	if r.Level != slog.LevelWarn || r.Message != "dependency left out: a pinned service has its name" ||
		!maps.Equal(attrs, map[string]string{"slug": "slug", "name": "backend-2"}) {
		t.Errorf("record = %v %q %v, want a warning naming slug and backend-2", r.Level, r.Message, attrs)
	}

	// Once the PR leaves, backend-2 is unpinned: the next sync drops the entry
	// and writes the dependency.
	s2 := &writeServer{files: []string{pinnedFile()}}
	c2 := s2.start(t)
	result, err = c2.SyncPreviewWithRetry(context.Background(), "slug", catalog)
	if err != nil {
		t.Fatalf("SyncPreviewWithRetry: %v", err)
	}
	if len(result.Values.Dependencies) != 1 || result.Values.Dependencies[0].Name != "backend-2" {
		t.Errorf("dependencies = %+v, want backend-2 once the pinned entry is gone", result.Values.Dependencies)
	}
	if !result.Notify {
		t.Error("Notify = false, want true: a dependency was added")
	}
}
