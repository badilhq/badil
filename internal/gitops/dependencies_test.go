package gitops_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/preview"
)

// withDependencies sets on c the dependencies postgres (an OCI chart with
// values and a values file) and mailpit (a chart of a Helm repository,
// exposed), in that order.
func withDependencies(c *gitops.ServiceCatalog) {
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
	written, _ := s.written.Load().(string)
	if !strings.HasPrefix(
		written,
		"apiVersion: badil.dev/v1beta1\ngitopsRepoURL: https://github.com/acme/gitops.git\n",
	) {
		t.Errorf("written content does not start with apiVersion then gitopsRepoURL:\n%s", written)
	}
	if !strings.HasSuffix(written, "\ndependencies:\n"+dependenciesYAML) {
		t.Errorf("written content does not end with the dependencies:\n%s", written)
	}
	if len(result.Values.Dependencies) != 2 || result.Values.Dependencies[1].Host != "mailpit-slug.127.0.0.1.nip.io" {
		t.Errorf("result dependencies = %+v", result.Values.Dependencies)
	}
}

// TestSyncPreviewWithRetry_ShadowedDependency checks that a dependency named
// like a kept pinned entry of a removed service is left out with a warning,
// and written once that entry is unpinned.
func TestSyncPreviewWithRetry_ShadowedDependency(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
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
		t.Errorf("Changed = true, want the file kept without the dependency:\n%s", s.written.Load())
	}
	if got := logs.String(); !strings.Contains(got, "level=WARN") ||
		!strings.Contains(got, "slug=slug name=backend-2") {
		t.Errorf("logs = %q, want a warning naming slug and backend-2", got)
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
