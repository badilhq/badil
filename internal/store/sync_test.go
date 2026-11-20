package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// staleBranchFile is pinnedFile with backend-2 tracking the branch old.
func staleBranchFile() string {
	return pinnedFile(
		"    repoURL: https://github.com/acme/backend-2.git\n    branch: main\n",
		"    repoURL: https://github.com/acme/backend-2.git\n    branch: old\n",
	)
}

func TestSyncPreviewWithRetry(t *testing.T) {
	tests := []struct {
		name         string
		files        []string
		conflicts    int
		catalog      func(*store.ServiceCatalog)
		wantChanged  bool
		wantNotify   bool
		wantNotFound bool
		wantPuts     int
		// want turns the last file read into the file written.
		want func(*preview.PreviewValues)
	}{
		{name: "matches", files: []string{pinnedFile()}},
		{
			name: "added service", files: []string{pinnedFile()},
			catalog: func(c *store.ServiceCatalog) {
				c.Services = []string{"backend-1", "front", "backend-2"}
				c.Repos["front"] = "https://github.com/acme/front.git"
			},
			wantChanged: true, wantNotify: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) {
				v.Services = slices.Insert(v.Services, 1, preview.ServiceEntry{
					Name: "front", RepoURL: "https://github.com/acme/front.git", Branch: "main",
					ImageTag: "${ARGOCD_APP_REVISION}", Chart: preview.ChartSettings{
						Path: preview.DefaultChartPath, ImageTagParam: preview.DefaultImageTagParam,
						HostParam: preview.DefaultHostParam,
					},
				})
			},
		},
		{
			name: "chart settings change", files: []string{pinnedFile()},
			catalog: func(c *store.ServiceCatalog) {
				c.Charts = map[string]preview.ChartSettings{
					"backend-2": {Path: "deploy/helm", ValueFiles: []string{"values-preview.yaml"}},
				}
			},
			wantChanged: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) {
				v.Services[1].Chart.Path = "deploy/helm"
				v.Services[1].Chart.ValueFiles = []string{"values-preview.yaml"}
			},
		},
		{
			name: "added dependencies", files: []string{pinnedFile()},
			catalog:     withDependencies,
			wantChanged: true, wantNotify: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) {
				v.Dependencies = decode(t, withDependenciesFile()).Dependencies
			},
		},
		{
			name: "dependencies match", files: []string{withDependenciesFile()},
			catalog: withDependencies,
		},
		{
			name: "removed dependency", files: []string{withDependenciesFile()},
			catalog: func(c *store.ServiceCatalog) {
				withDependencies(c)
				c.Dependencies = c.Dependencies[1:]
			},
			wantChanged: true, wantNotify: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) { v.Dependencies = v.Dependencies[1:] },
		},
		{
			name: "dependency no longer exposed", files: []string{withDependenciesFile()},
			catalog: func(c *store.ServiceCatalog) {
				withDependencies(c)
				delete(c.URLs.Expose, "mailpit")
			},
			wantChanged: true, wantNotify: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) {
				v.Dependencies[1].Host, v.Dependencies[1].HostParam = "", ""
			},
		},
		{
			name: "host change", files: []string{pinnedFile("    host: backend-1-slug.127.0.0.1.nip.io\n", "")},
			wantChanged: true, wantNotify: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) { v.Services[0].Host = "backend-1-slug.127.0.0.1.nip.io" },
		},
		{
			name: "branch only", files: []string{staleBranchFile()},
			wantChanged: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) { v.Services[1].Branch = "main" },
		},
		{
			name: "stale repo url", files: []string{pinnedFile("acme/backend-2.git", "acme/old.git")},
			wantChanged: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) {
				v.Services[1].RepoURL = "https://github.com/acme/backend-2.git"
			},
		},
		{
			name: "file without apiVersion", files: []string{pinnedFile("apiVersion: badil.dev/v1beta1\n", "")},
			wantChanged: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) { v.APIVersion = preview.APIVersion },
		},
		{
			name:        "badil.dev/v1 file",
			files:       []string{pinnedFile("apiVersion: badil.dev/v1beta1\n", "apiVersion: badil.dev/v1\n")},
			wantChanged: true, wantPuts: 1,
			want: func(v *preview.PreviewValues) { v.APIVersion = preview.APIVersion },
		},
		{
			name:      "changed after a 409 re-fetch",
			files:     []string{staleBranchFile(), pinnedFile("acme/backend-2.git", "acme/x.git")},
			conflicts: 1, wantChanged: true, wantPuts: 2,
			want: func(v *preview.PreviewValues) {
				v.Services[1].RepoURL = "https://github.com/acme/backend-2.git"
			},
		},
		{
			name: "matches after a 409 re-fetch", files: []string{staleBranchFile(), pinnedFile()},
			conflicts: 1, wantPuts: 1,
		},
		{name: "missing file", wantNotFound: true},
		{
			name:         "deleted before the re-fetch",
			files:        []string{staleBranchFile(), ""},
			conflicts:    1,
			wantNotFound: true,
			wantPuts:     1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &writeServer{files: tt.files, conflicts: tt.conflicts}
			c := s.start(t)
			catalog := testCatalog("backend-1", "backend-2")
			if tt.catalog != nil {
				tt.catalog(catalog)
			}

			result, err := c.SyncPreviewWithRetry(context.Background(), "slug", catalog)
			if err != nil {
				t.Fatalf("SyncPreviewWithRetry: %v", err)
			}
			if puts := s.puts(); puts != tt.wantPuts {
				t.Errorf("puts = %d, want %d", puts, tt.wantPuts)
			}
			got := [3]bool{result.Changed, result.Notify, result.NotFound}
			if want := [3]bool{tt.wantChanged, tt.wantNotify, tt.wantNotFound}; got != want {
				t.Errorf("Changed, Notify, NotFound = %v, want %v", got, want)
			}
			assertSyncWrite(t, s, result, tt.files, tt.want)
		})
	}
}

// assertSyncWrite checks that an unchanged result is empty, and that a
// changed one describes the write s received, whose content parses to the
// last of files turned by edit.
func assertSyncWrite(
	t *testing.T, s *writeServer, result *store.SyncResult, files []string, edit func(*preview.PreviewValues),
) {
	t.Helper()
	if !result.Changed {
		if result.CommitURL != "" || result.CommitMessage != "" || len(result.Values.Services) != 0 {
			t.Errorf("unchanged result = %+v, want empty", result)
		}
		return
	}
	want := decode(t, files[len(files)-1])
	edit(&want)
	const wantMessage = "chore(preview): sync slug with badil.yaml"
	if result.CommitMessage != wantMessage || result.CommitURL != fakeCommitURL {
		t.Errorf("CommitMessage, CommitURL = %q, %q", result.CommitMessage, result.CommitURL)
	}
	if reqs := s.fake.requests(); reqs[len(reqs)-1].message != wantMessage {
		t.Errorf("commit message = %q, want %q", reqs[len(reqs)-1].message, wantMessage)
	}
	if !preview.Equal(result.Values, want) {
		t.Errorf("result Values = %+v, want %+v", result.Values, want)
	}
	assertWritten(t, s, want)
}
