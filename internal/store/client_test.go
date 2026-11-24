package store_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

func TestFetchServiceCatalog(t *testing.T) {
	const badilYAML = `services:
  backend-1:
    repo: https://github.com/acme/backend-1.git
  backend-2:
    repo: https://github.com/acme/backend-2.git
  front:
    repo: https://github.com/acme/front.git
`
	c, _ := readStore(t, map[string]string{"badil.yaml": badilYAML})
	got, err := c.FetchServiceCatalog(context.Background())
	if err != nil {
		t.Fatalf("FetchServiceCatalog: %v", err)
	}
	if got.URLs.Enabled() {
		t.Errorf("URLs = %+v, want off without a preview block", got.URLs)
	}
	catalog := got.Services
	want := []string{"backend-1", "backend-2", "front"}
	if len(catalog) != len(want) {
		t.Fatalf("catalog = %v, want %v", catalog, want)
	}
	for i, name := range want {
		if catalog[i] != name {
			t.Errorf("catalog[%d] = %q, want %q", i, catalog[i], name)
		}
	}
}

func TestFetchServiceCatalogSHA(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantSHA  bool
		wantErr  error
		wantSvcs []string
	}{
		{
			name:     "parsed",
			content:  "services:\n  front:\n    repo: https://github.com/acme/front.git\n",
			wantSHA:  true,
			wantSvcs: []string{"front"},
		},
		{name: "unparsable keeps the sha", content: "services: [", wantSHA: true},
		{name: "missing has no sha", wantErr: lifecycle.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			if tc.content != "" {
				files["badil.yaml"] = tc.content
			}
			c, _ := readStore(t, files)
			got, sha, err := c.FetchServiceCatalogSHA(context.Background())
			wantErr := tc.wantErr != nil || tc.wantSvcs == nil
			if (err != nil) != wantErr || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("err = %v, want error %v (%v)", err, wantErr, tc.wantErr)
			}
			wantSHA := ""
			if tc.wantSHA {
				wantSHA = blobSHA(tc.content)
			}
			if sha != wantSHA {
				t.Errorf("sha = %q, want %q", sha, wantSHA)
			}
			if !wantErr && !slices.Equal(got.Services, tc.wantSvcs) {
				t.Errorf("services = %v, want %v", got.Services, tc.wantSvcs)
			}
		})
	}
}

// commitSHA is a gitops commit of the commit link tests.
const commitSHA = "0123456789abcdef0123456789abcdef01234567"

// TestRepoNames checks the full name, commit link and preview files'
// gitopsRepoURL a store derives from its repo URL, today's strings for
// https://github.com/acme/gitops.git.
func TestRepoNames(t *testing.T) {
	cases := []struct {
		url, fullName, commitURL string
	}{
		{"https://github.com/acme/gitops.git", "acme/gitops", "https://github.com/acme/gitops/commit/" + commitSHA},
		{"https://github.com/acme/gitops", "acme/gitops", "https://github.com/acme/gitops/commit/" + commitSHA},
		{"https://github.com/acme/gitops/", "acme/gitops", "https://github.com/acme/gitops/commit/" + commitSHA},
		{"https://github.com/acme/gitops.git/", "acme/gitops", "https://github.com/acme/gitops/commit/" + commitSHA},
		{"https://GitHub.com/Acme/GitOps.git", "Acme/GitOps", "https://GitHub.com/Acme/GitOps/commit/" + commitSHA},
		{
			"http://gitea.gitea.svc:3000/acme/gitops.git", "acme/gitops",
			"http://gitea.gitea.svc:3000/acme/gitops/commit/" + commitSHA,
		},
	}
	remote := newRemote(t, nil)
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			c := openStore(t, remote.URL(), store.WithRepoURL(tc.url))
			if got := c.FullName(); got != tc.fullName {
				t.Errorf("FullName = %q, want %q", got, tc.fullName)
			}
			if got := store.CommitURL(c, commitSHA); got != tc.commitURL {
				t.Errorf("commit URL = %q, want %q", got, tc.commitURL)
			}
			if got := store.GitopsRepoURL(c); got != tc.url {
				t.Errorf("gitopsRepoURL = %q, want %q", got, tc.url)
			}
		})
	}
}

// TestRepoNames_FromCloneURL checks that without WithRepoURL the names derive
// from the clone URL.
func TestRepoNames_FromCloneURL(t *testing.T) {
	remote := newRemote(t, nil)
	srv := httptest.NewServer(remote.HTTPHandler(http.StatusForbidden))
	t.Cleanup(srv.Close)
	repoURL := srv.URL + "/acme/gitops.git"
	st, err := store.Open(
		context.Background(),
		store.CloneConfig{URL: repoURL},
		storetest.Identity,
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if st.FullName() != "acme/gitops" || store.CommitURL(st, commitSHA) != srv.URL+"/acme/gitops/commit/"+commitSHA ||
		store.GitopsRepoURL(st) != repoURL {
		t.Errorf("FullName, commit URL, gitopsRepoURL = %q, %q, %q; want those of %s",
			st.FullName(), store.CommitURL(st, commitSHA), store.GitopsRepoURL(st), repoURL)
	}
}

func TestGetPreview_Success(t *testing.T) {
	const body = `services:
  - name: backend-1
    commitSha: abc1234567890
    metadata:
      pr-author: octocat
      pr-url: https://github.com/acme/backend-1/pull/42
      pr-number: '42'
      created-at: '2026-05-26T10:00:00Z'
      updated-at: '2026-05-26T10:00:00Z'
      vcs.ref.name: feature/add-pricing
      cicd.pipeline.run.url: https://github.com/acme/backend-1/actions/runs/1
  - name: backend-2
  - name: front
`
	c, _ := readStore(t, map[string]string{"previews/feature-add-pricing/values.yaml": body})
	vals, sha, err := c.GetPreview(context.Background(), "feature-add-pricing")
	if err != nil {
		t.Fatalf("GetPreview: %v", err)
	}
	if want := blobSHA(body); sha != want {
		t.Errorf("sha = %q, want %q", sha, want)
	}
	if got := len(vals.Services); got != 3 {
		t.Fatalf("len(Services) = %d, want 3", got)
	}
	if got := vals.Services[0].Metadata.PRNumber; got != "42" {
		t.Errorf("metadata pr-number = %q, want 42", got)
	}
}

func TestGetPreview_NotFound(t *testing.T) {
	c, _ := readStore(t, map[string]string{"previews/other/values.yaml": "services: []\n"})
	_, _, err := c.GetPreview(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, lifecycle.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
