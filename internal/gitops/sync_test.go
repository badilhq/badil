package gitops_test

import (
	"context"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/gitops"
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
		name        string
		files       []string
		conflicts   int32
		catalog     func(*gitops.ServiceCatalog)
		wantChanged bool
		wantNotify  bool
		wantGets    int32
		wantPuts    int32
		wantWritten []string
	}{
		{name: "matches", files: []string{pinnedFile()}, wantGets: 1},
		{
			name: "added service", files: []string{pinnedFile()},
			catalog: func(c *gitops.ServiceCatalog) {
				c.Services = []string{"backend-1", "front", "backend-2"}
				c.Repos["front"] = "https://github.com/acme/front.git"
			},
			wantChanged: true, wantNotify: true, wantGets: 1, wantPuts: 1,
			wantWritten: []string{
				"  - name: front\n    repoURL: https://github.com/acme/front.git\n    branch: main\n" +
					"  - name: backend-2\n",
			},
		},
		{
			name: "host change", files: []string{pinnedFile("    host: backend-1-slug.127.0.0.1.nip.io\n", "")},
			wantChanged: true, wantNotify: true, wantGets: 1, wantPuts: 1,
			wantWritten: []string{"    host: backend-1-slug.127.0.0.1.nip.io\n"},
		},
		{
			name: "branch only", files: []string{staleBranchFile()},
			wantChanged: true, wantGets: 1, wantPuts: 1,
			wantWritten: []string{"    repoURL: https://github.com/acme/backend-2.git\n    branch: main\n"},
		},
		{
			name: "stale repo url", files: []string{pinnedFile("acme/backend-2.git", "acme/old.git")},
			wantChanged: true, wantGets: 1, wantPuts: 1,
			wantWritten: []string{"    repoURL: https://github.com/acme/backend-2.git\n"},
		},
		{
			name: "file without apiVersion", files: []string{pinnedFile("apiVersion: badil.dev/v1\n", "")},
			wantChanged: true, wantGets: 1, wantPuts: 1,
			wantWritten: []string{"apiVersion: badil.dev/v1\nservices:\n"},
		},
		{
			name:      "changed after a 409 re-fetch",
			files:     []string{staleBranchFile(), pinnedFile("acme/backend-2.git", "acme/x.git")},
			conflicts: 1, wantChanged: true, wantGets: 2, wantPuts: 2,
			wantWritten: []string{"    repoURL: https://github.com/acme/backend-2.git\n"},
		},
		{
			name: "matches after a 409 re-fetch", files: []string{staleBranchFile(), pinnedFile()},
			conflicts: 1, wantGets: 2, wantPuts: 1,
		},
		{name: "missing file", wantGets: 1},
		{
			name:      "deleted before the re-fetch",
			files:     []string{staleBranchFile(), ""},
			conflicts: 1,
			wantGets:  2,
			wantPuts:  1,
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
			if gets, puts := s.gets.Load(), s.puts.Load(); gets != tt.wantGets || puts != tt.wantPuts {
				t.Errorf("gets, puts = %d, %d; want %d, %d", gets, puts, tt.wantGets, tt.wantPuts)
			}
			if result.Changed != tt.wantChanged || result.Notify != tt.wantNotify {
				t.Errorf("Changed, Notify = %v, %v; want %v, %v",
					result.Changed, result.Notify, tt.wantChanged, tt.wantNotify)
			}
			assertSyncWrite(t, s, result, tt.wantWritten)
		})
	}
}

// assertSyncWrite checks that a changed result describes the write s received,
// whose content holds every part of want, and that an unchanged one is empty.
func assertSyncWrite(t *testing.T, s *writeServer, result *gitops.SyncResult, want []string) {
	t.Helper()
	if !result.Changed {
		if result.CommitURL != "" || result.CommitMessage != "" || len(result.Values.Services) != 0 {
			t.Errorf("unchanged result = %+v, want empty", result)
		}
		return
	}
	const wantMessage = "chore(preview): sync slug with badil.yaml"
	if result.CommitMessage != wantMessage || result.CommitURL != "https://github.com/x/y/commit/written" {
		t.Errorf("CommitMessage, CommitURL = %q, %q", result.CommitMessage, result.CommitURL)
	}
	if got, _ := s.message.Load().(string); got != wantMessage {
		t.Errorf("commit message = %q, want %q", got, wantMessage)
	}
	written, _ := s.written.Load().(string)
	if !strings.HasPrefix(written, "apiVersion: badil.dev/v1\n") {
		t.Errorf("written content has no apiVersion:\n%s", written)
	}
	for _, part := range want {
		if !strings.Contains(written, part) {
			t.Errorf("written content missing %q:\n%s", part, written)
		}
	}
}
