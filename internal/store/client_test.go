package store_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

func sampleInputs() preview.DeployInputs {
	return preview.DeployInputs{
		ServiceName:    "backend-1",
		HeadRef:        "feature/add-pricing",
		CommitSha:      "abc1234567890",
		PRAuthor:       "octocat",
		PRURL:          "https://github.com/acme/backend-1/pull/42",
		PRNumber:       "42",
		Timestamp:      "2026-05-26T10:00:00Z",
		WorkflowRunURL: "https://github.com/acme/backend-1/actions/runs/1",
	}
}

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
		{name: "missing has no sha", wantErr: store.ErrNotFound},
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

func TestFullName(t *testing.T) {
	c, _ := readStore(t, nil)
	if got := c.FullName(); got != "acme/gitops" {
		t.Errorf("FullName = %q, want acme/gitops", got)
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
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// hostURLs exposes backend-1 under the domain 127.0.0.1.nip.io.
func hostURLs() preview.URLConfig {
	return preview.URLConfig{Domain: "127.0.0.1.nip.io", Expose: map[string]string{"backend-1": "/"}}
}

// testCatalog is a catalog of services, each at https://github.com/acme/<name>.git
// and tracking main, with the hosts of hostURLs.
func testCatalog(services ...string) *store.ServiceCatalog {
	repos := make(map[string]string, len(services))
	for _, name := range services {
		repos[name] = "https://github.com/acme/" + name + ".git"
	}
	return &store.ServiceCatalog{Catalog: preview.Catalog{Services: services, Repos: repos}, URLs: hostURLs()}
}

func TestDeploy_ConflictThenSuccess(t *testing.T) {
	const initialFile = `services:
  - name: backend-1
    commitSha: oldsha
    metadata:
      pr-author: octocat
      pr-url: https://github.com/acme/backend-1/pull/42
      pr-number: '42'
      created-at: '2026-05-26T09:00:00Z'
      updated-at: '2026-05-26T09:00:00Z'
      vcs.ref.name: feature/add-pricing
      cicd.pipeline.run.url: https://github.com/x/y/actions/runs/0
  - name: backend-2
`
	const refreshedFile = `services:
  - name: backend-1
    commitSha: oldsha
    metadata:
      pr-author: octocat
      pr-url: https://github.com/acme/backend-1/pull/42
      pr-number: '42'
      created-at: '2026-05-26T09:00:00Z'
      updated-at: '2026-05-26T09:30:00Z'
      vcs.ref.name: feature/add-pricing
      cicd.pipeline.run.url: https://github.com/x/y/actions/runs/0
  - name: backend-2
`
	f := newFileFake(t, slugPath, []string{initialFile, refreshedFile}, 1, nil)
	c := f.open(t)
	in := sampleInputs()
	in.CommitSha = "newsha"
	in.Timestamp = "2026-05-26T10:00:00Z"
	catalog := testCatalog("backend-1", "backend-2")
	catalog.Branches = map[string]string{"backend-2": "develop"}

	result, out, err := deploy(c, "slug", catalog, "backend-1", "newsha", in, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := f.pushCount(); n != 2 {
		t.Fatalf("pushes = %d, want 2", n)
	}
	// The retry merged the deploy into the file the lost race committed.
	if got := result.Values.Services[0].Metadata.CreatedAt; got != "2026-05-26T09:00:00Z" {
		t.Errorf("created-at = %q, want the re-read file's", got)
	}
	if commits := f.remote.Commits(); out.Commit != commits[0].Hash || commits[1].Tree[slugPath] != refreshedFile {
		t.Errorf("result commit = %s, want the head %s on the file the lost race committed",
			out.Commit, commits[0].Hash)
	}
	if len(result.Values.Services) != 2 {
		t.Fatalf("returned vals = %+v, want 2 services", result.Values)
	}
	if result.Values.Services[0].CommitSha != "newsha" {
		t.Errorf("backend-1 commitSha = %q, want newsha", result.Values.Services[0].CommitSha)
	}
	if result.Values.Services[1].CommitSha != "" {
		t.Errorf("backend-2 commitSha = %q, want empty (main-tracking)", result.Values.Services[1].CommitSha)
	}
	// The re-fetched file has no apiVersion, repoURL or branch: the retry
	// write must carry the catalog's facts.
	written := f.written()
	assertCatalogFacts(t, decode(t, written), []string{
		"backend-1 https://github.com/acme/backend-1.git main",
		"backend-2 https://github.com/acme/backend-2.git develop",
	})
	assertWrittenHosts(t, result.Values, written, "slug")
}

// assertCatalogFacts checks that vals has the apiVersion and gitops repo of
// testCatalog and, in order, the services of want, each "name repoURL branch".
func assertCatalogFacts(t *testing.T, vals preview.PreviewValues, want []string) {
	t.Helper()
	got := make([]string, 0, len(vals.Services))
	for _, svc := range vals.Services {
		got = append(got, svc.Name+" "+svc.RepoURL+" "+svc.Branch)
	}
	if vals.APIVersion != preview.APIVersion || vals.GitopsRepoURL != "https://github.com/acme/gitops.git" ||
		!slices.Equal(got, want) {
		t.Errorf("apiVersion, gitopsRepoURL, services = %q, %q, %v; want %q, the gitops repo, %v",
			vals.APIVersion, vals.GitopsRepoURL, got, preview.APIVersion, want)
	}
}

// assertWrittenHosts checks that the exposed backend-1 carries its host and
// backend-2 none, both in the returned values and in the written file.
func assertWrittenHosts(t *testing.T, vals preview.PreviewValues, written, slug string) {
	t.Helper()
	want := "backend-1-" + slug + ".127.0.0.1.nip.io"
	for _, svc := range vals.Services {
		switch svc.Name {
		case "backend-1":
			if svc.Host != want {
				t.Errorf("backend-1 host = %q, want %q", svc.Host, want)
			}
		default:
			if svc.Host != "" {
				t.Errorf("%s host = %q, want empty", svc.Name, svc.Host)
			}
		}
	}
	if got := decode(t, written); !preview.Equal(got, vals) {
		t.Errorf("written = %+v, want the returned %+v", got, vals)
	}
	if strings.Count(written, "host:") != 1 {
		t.Errorf("written body has %d hosts, want 1:\n%s", strings.Count(written, "host:"), written)
	}
}

func TestDeploy_FreshCreateUsesCatalog(t *testing.T) {
	f := newFileFake(t, "previews/fresh-slug/values.yaml", nil, 0, nil)
	c := f.open(t)
	catalog := testCatalog("backend-1", "backend-2", "front")
	catalog.Repos["front"] = "https://github.com/acme/front"
	catalog.Branches = map[string]string{"backend-2": "develop"}
	result, _, err := deploy(c, "fresh-slug", catalog, "backend-1", "abc1234567890", sampleInputs(), false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := f.pushCount(); n != 1 || !result.Created {
		t.Fatalf("pushes, Created = %d, %v; want one create", n, result.Created)
	}
	if len(result.Values.Services) != 3 {
		t.Fatalf("vals = %+v, want 3 services (full catalog)", result.Values)
	}
	if result.Values.Services[0].Name != "backend-1" || result.Values.Services[0].CommitSha != "abc1234567890" {
		t.Errorf("triggering service = %+v, want backend-1/abc1234567890", result.Values.Services[0])
	}
	for _, svc := range result.Values.Services[1:] {
		if svc.CommitSha != "" || svc.Metadata != nil {
			t.Errorf("main-tracking entry %q polluted: %+v", svc.Name, svc)
		}
	}
	written := f.written()
	assertCatalogFacts(t, decode(t, written), []string{
		"backend-1 https://github.com/acme/backend-1.git main",
		"backend-2 https://github.com/acme/backend-2.git develop",
		"front https://github.com/acme/front main",
	})
	assertWrittenHosts(t, result.Values, written, "fresh-slug")
}

// defaultChartYAML is the chart block of an entry whose service sets no chart
// settings in the catalog.
const defaultChartYAML = "    chart:\n" +
	"      path: k8s/\n" +
	"      imageTagParam: app.image.tag\n" +
	"      hostParam: app.ingress.host\n"

// decode returns the preview file content parsed.
func decode(t *testing.T, content string) preview.PreviewValues {
	t.Helper()
	var vals preview.PreviewValues
	if err := yaml.Unmarshal([]byte(content), &vals); err != nil {
		t.Fatalf("parse preview file: %v\n%s", err, content)
	}
	return vals
}

// assertWritten fails t unless the file s wrote parses to want
// (preview.Equal).
func assertWritten(t *testing.T, s *writeServer, want preview.PreviewValues) {
	t.Helper()
	if got := decode(t, s.written()); !preview.Equal(got, want) {
		t.Errorf("written = %+v, want %+v", got, want)
	}
}

// pinnedFile is previews/slug/values.yaml as a deploy of sampleInputs with
// hostURLs writes it, except updated-at, which is older. Its replacer
// arguments are applied to it.
func pinnedFile(replace ...string) string {
	const file = `apiVersion: badil.dev/v1beta1
gitopsRepoURL: https://github.com/acme/gitops.git
services:
  - name: backend-1
    repoURL: https://github.com/acme/backend-1.git
    branch: main
    commitSha: abc1234567890
    imageTag: abc1234567890
    chart:
      path: k8s/
      imageTagParam: app.image.tag
      hostParam: app.ingress.host
    host: backend-1-slug.127.0.0.1.nip.io
    metadata:
      pr-author: octocat
      pr-url: https://github.com/acme/backend-1/pull/42
      pr-number: "42"
      created-at: "2026-05-26T09:00:00Z"
      updated-at: "2026-05-26T09:00:00Z"
      vcs.ref.name: feature/add-pricing
      cicd.pipeline.run.url: https://github.com/acme/backend-1/actions/runs/1
  - name: backend-2
    repoURL: https://github.com/acme/backend-2.git
    branch: main
    imageTag: ${ARGOCD_APP_REVISION}
    chart:
      path: k8s/
      imageTagParam: app.image.tag
      hostParam: app.ingress.host
dependencies: []
`
	return strings.NewReplacer(replace...).Replace(file)
}

// slugPath is the values file of the preview slug.
const slugPath = "previews/slug/values.yaml"

// writeServer is the gitops repo of a deploy to the preview slug: its file is
// files[0] (none when files is empty or files[0] is ""). The first conflicts
// pushes lose a race to a commit of the next file of files (the last one
// repeats; "" removes the file); the next pushes succeed.
type writeServer struct {
	files     []string
	conflicts int
	fake      *gitopsFake
}

func (s *writeServer) start(t *testing.T, opts ...store.Option) *store.GitHubStore {
	t.Helper()
	s.fake = newFileFake(t, slugPath, s.files, s.conflicts, nil)
	return s.fake.open(t, opts...)
}

// puts returns the number of pushes.
func (s *writeServer) puts() int {
	return s.fake.pushCount()
}

// written returns the file on the remote, "" when there is none.
func (s *writeServer) written() string {
	return s.fake.written()
}

func TestDeploy_SkipUnchanged(t *testing.T) {
	tests := []struct {
		name          string
		files         []string
		conflicts     int
		catalog       func(*store.ServiceCatalog)
		skipUnchanged bool
		wantUnchanged bool
		wantPuts      int
	}{
		{
			name:  "unchanged",
			files: []string{pinnedFile()}, skipUnchanged: true, wantUnchanged: true,
		},
		{
			name:  "unchanged without skipUnchanged",
			files: []string{pinnedFile()}, wantPuts: 1,
		},
		{
			name:  "new head sha",
			files: []string{pinnedFile("abc1234567890", "0000000old")}, skipUnchanged: true, wantPuts: 1,
		},
		{
			name:  "changed host",
			files: []string{pinnedFile("    host: backend-1-slug.127.0.0.1.nip.io\n", "")}, skipUnchanged: true,
			wantPuts: 1,
		},
		{
			name:  "changed pr metadata",
			files: []string{pinnedFile("pr-author: octocat", "pr-author: someone")}, skipUnchanged: true,
			wantPuts: 1,
		},
		{
			name:  "changed head ref",
			files: []string{pinnedFile("vcs.ref.name: feature/add-pricing", "vcs.ref.name: old")}, skipUnchanged: true,
			wantPuts: 1,
		},
		{
			name:  "catalog branch change",
			files: []string{pinnedFile()}, skipUnchanged: true, wantPuts: 1,
			catalog: func(c *store.ServiceCatalog) { c.Branches = map[string]string{"backend-2": "develop"} },
		},
		{
			name:  "catalog repo url change",
			files: []string{pinnedFile()}, skipUnchanged: true, wantPuts: 1,
			catalog: func(c *store.ServiceCatalog) { c.Repos["backend-2"] = "https://github.com/acme/b2.git" },
		},
		{
			name:  "service removed from the catalog",
			files: []string{pinnedFile()}, skipUnchanged: true, wantPuts: 1,
			catalog: func(c *store.ServiceCatalog) { delete(c.Repos, "backend-2"); c.Services = c.Services[:1] },
		},
		{
			name: "file without apiVersion",
			files: []string{
				pinnedFile("apiVersion: badil.dev/v1beta1\n", ""),
			},
			skipUnchanged: true,
			wantPuts:      1,
		},
		{
			name:  "unchanged after a lost push",
			files: []string{pinnedFile("abc1234567890", "0000000old"), pinnedFile()}, conflicts: 1, skipUnchanged: true,
			wantUnchanged: true, wantPuts: 1,
		},
		{
			name: "changed after a lost push",
			files: []string{
				pinnedFile("abc1234567890", "0000000old"),
				pinnedFile("abc1234567890", "0000000other"),
			},
			conflicts: 1, skipUnchanged: true, wantPuts: 2,
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

			result, out, err := deploy(
				c, "slug", catalog, "backend-1", "abc1234567890", sampleInputs(), tt.skipUnchanged,
			)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if puts := s.puts(); puts != tt.wantPuts {
				t.Errorf("puts = %d, want %d", puts, tt.wantPuts)
			}
			assertSkipResult(t, result, out, tt.wantUnchanged)
		})
	}
}

// assertSkipResult checks result is an update pushed as one commit, or, when
// unchanged, the fetched file with no commit.
func assertSkipResult(t *testing.T, result *store.WriteResult, out store.Result, unchanged bool) {
	t.Helper()
	if result.Unchanged != unchanged || result.Created {
		t.Errorf("Unchanged, Created = %v, %v; want %v, false", result.Unchanged, result.Created, unchanged)
	}
	if out.NoOp != unchanged {
		t.Errorf("NoOp = %v, want %v", out.NoOp, unchanged)
	}
	if !unchanged {
		if out.Subject != "deploy backend-1 to slug" ||
			out.URL != "https://github.com/acme/gitops/commit/"+out.Commit {
			t.Errorf("Subject, URL = %q, %q", out.Subject, out.URL)
		}
		return
	}
	if got := result.Values.Services[0].Metadata.UpdatedAt; got != "2026-05-26T09:00:00Z" {
		t.Errorf("Values updated-at = %q, want the fetched file's", got)
	}
}

// TestDeploy_Created checks Created.
func TestDeploy_Created(t *testing.T) {
	tests := []struct {
		name      string
		files     []string
		conflicts int
		want      bool
	}{
		{name: "create", want: true},
		{name: "update", files: []string{pinnedFile("abc1234567890", "0000000old")}},
		{name: "create lost to a push race", files: []string{"", pinnedFile("abc1234567890", "0000000old")}, conflicts: 1},
		{name: "unchanged", files: []string{pinnedFile()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &writeServer{files: tt.files, conflicts: tt.conflicts}
			c := s.start(t)

			result, _, err := deploy(
				c, "slug", testCatalog("backend-1", "backend-2"), "backend-1", "abc1234567890", sampleInputs(),
				tt.name == "unchanged",
			)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if result.Created != tt.want {
				t.Errorf("Created = %v, want %v", result.Created, tt.want)
			}
		})
	}
}

// TestDeploy_FollowsCatalog checks that a deploy update writes
// apiVersion and every catalog service in catalog order with its repoURL and
// branch from the catalog, then keeps a pinned entry of a removed service as
// it is (its imageTag and chart as last written), and drops an unpinned one
// and a pinned one without repoURL.
func TestDeploy_FollowsCatalog(t *testing.T) {
	const file = `services:
  - name: gone-pinned
    repoURL: https://github.com/acme/gone-pinned.git
    branch: release
    commitSha: fff
    imageTag: v-fff
    chart:
      path: deploy/
      imageTagParam: image.tag
      hostParam: ingress.host
    metadata:
      pr-author: octocat
      pr-url: https://github.com/acme/gone-pinned/pull/9
      pr-number: "9"
      created-at: "2026-05-25T10:00:00Z"
      updated-at: "2026-05-25T11:00:00Z"
      vcs.ref.name: feature/gone
      cicd.pipeline.run.url: https://github.com/acme/gone-pinned/actions/runs/9
  - name: backend-2
    repoURL: https://github.com/acme/old.git
    branch: old
  - name: gone
  - name: gone-pinned-no-repo
    commitSha: eee
    metadata:
      pr-url: https://github.com/acme/gone-pinned-no-repo/pull/3
  - name: backend-1
`
	s := &writeServer{files: []string{file}}
	c := s.start(t)
	catalog := testCatalog("backend-1", "backend-2")
	catalog.Branches = map[string]string{"backend-2": "develop"}

	result, _, err := deploy(c, "slug", catalog, "backend-1", "abc1234567890", sampleInputs(), true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Unchanged {
		t.Fatal("Unchanged = true, want a write")
	}
	want := "apiVersion: badil.dev/v1beta1\n" +
		"gitopsRepoURL: https://github.com/acme/gitops.git\n" +
		"services:\n" +
		"  - name: backend-1\n" +
		"    repoURL: https://github.com/acme/backend-1.git\n" +
		"    branch: main\n" +
		"    commitSha: abc1234567890\n" +
		"    imageTag: abc1234567890\n" +
		defaultChartYAML +
		"    host: backend-1-slug.127.0.0.1.nip.io\n" +
		"    metadata:\n" +
		"      pr-author: octocat\n" +
		"      pr-url: https://github.com/acme/backend-1/pull/42\n" +
		"      pr-number: \"42\"\n" +
		"      created-at: \"2026-05-26T10:00:00Z\"\n" +
		"      updated-at: \"2026-05-26T10:00:00Z\"\n" +
		"      vcs.ref.name: feature/add-pricing\n" +
		"      cicd.pipeline.run.url: https://github.com/acme/backend-1/actions/runs/1\n" +
		"  - name: backend-2\n" +
		"    repoURL: https://github.com/acme/backend-2.git\n" +
		"    branch: develop\n" +
		"    imageTag: ${ARGOCD_APP_REVISION}\n" +
		defaultChartYAML +
		"  - name: gone-pinned\n" +
		"    repoURL: https://github.com/acme/gone-pinned.git\n" +
		"    branch: release\n" +
		"    commitSha: fff\n" +
		"    imageTag: v-fff\n" +
		"    chart:\n" +
		"      path: deploy/\n" +
		"      imageTagParam: image.tag\n" +
		"      hostParam: ingress.host\n" +
		"    metadata:\n" +
		"      pr-author: octocat\n" +
		"      pr-url: https://github.com/acme/gone-pinned/pull/9\n" +
		"      pr-number: \"9\"\n" +
		"      created-at: \"2026-05-25T10:00:00Z\"\n" +
		"      updated-at: \"2026-05-25T11:00:00Z\"\n" +
		"      vcs.ref.name: feature/gone\n" +
		"      cicd.pipeline.run.url: https://github.com/acme/gone-pinned/actions/runs/9\n" +
		"dependencies: []\n"
	assertWritten(t, s, decode(t, want))
}

// TestDeploy_AddsMissingService checks that a deploy to an
// existing preview writes a catalog service the file lacks as a
// branch-tracking entry, in catalog order.
func TestDeploy_AddsMissingService(t *testing.T) {
	s := &writeServer{files: []string{pinnedFile()}}
	c := s.start(t)
	catalog := testCatalog("backend-1", "front", "backend-2")

	result, _, err := deploy(c, "slug", catalog, "backend-1", "abc1234567890", sampleInputs(), true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Unchanged {
		t.Fatal("Unchanged = true, want a write")
	}
	names := make([]string, 0, len(result.Values.Services))
	for _, svc := range result.Values.Services {
		names = append(names, svc.Name)
	}
	if want := []string{"backend-1", "front", "backend-2"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	got := decode(t, s.written())
	front := preview.ServiceEntry{
		Name: "front", RepoURL: "https://github.com/acme/front.git", Branch: "main",
		ImageTag: "${ARGOCD_APP_REVISION}", Chart: preview.ChartSettings{
			Path: "k8s/", ImageTagParam: "app.image.tag", HostParam: "app.ingress.host",
		},
	}
	if !preview.Equal(got, result.Values) || !reflect.DeepEqual(got.Services[1], front) {
		t.Errorf("written = %+v, want the returned values, front between backend-1 and backend-2", got)
	}
}

// TestDeploy_BranchChangeWritten checks that a catalog branch
// change is written even when the commit is unchanged.
func TestDeploy_BranchChangeWritten(t *testing.T) {
	s := &writeServer{files: []string{pinnedFile()}}
	c := s.start(t)
	catalog := testCatalog("backend-1", "backend-2")
	catalog.Branches = map[string]string{"backend-2": "develop"}

	result, _, err := deploy(c, "slug", catalog, "backend-1", "abc1234567890", sampleInputs(), true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Unchanged || result.Values.Services[1].Branch != "develop" {
		t.Fatalf("result = %+v, want a write with backend-2 on develop", result)
	}
	if got := s.written(); !strings.Contains(
		got,
		"  - name: backend-2\n    repoURL: https://github.com/acme/backend-2.git\n    branch: develop\n",
	) {
		t.Errorf("written content missing backend-2 on develop:\n%s", got)
	}
}

// TestDeploy_RendererSettings checks that a deploy writes the
// catalog's namespace metadata and, on every entry, its chart settings with
// their defaults applied and its image tag format expanded.
func TestDeploy_RendererSettings(t *testing.T) {
	const sha = "f913b6cb1a2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f"
	s := &writeServer{}
	c := s.start(t)
	catalog := testCatalog("backend-1", "backend-2")
	catalog.ImageTag = "sha-{sha7}"
	catalog.Charts = map[string]preview.ChartSettings{
		"backend-1": {Path: "deploy/helm", ImageTagParam: "image.tag", ValueFiles: []string{"values-preview.yaml"}},
	}
	catalog.Namespace = &preview.NamespaceMetadata{Labels: map[string]string{"team": "platform"}}

	if _, _, err := deploy(c, "slug", catalog, "backend-1", sha, sampleInputs(), false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := decode(t, s.written())
	wantNS := preview.NamespaceMetadata{Labels: map[string]string{"team": "platform"}}
	if got.Namespace == nil || !maps.Equal(got.Namespace.Labels, wantNS.Labels) || len(got.Namespace.Annotations) != 0 {
		t.Errorf("written namespace = %+v, want %+v", got.Namespace, wantNS)
	}
	want := []preview.ServiceEntry{
		{ImageTag: "sha-f913b6c", Chart: preview.ChartSettings{
			Path: "deploy/helm", ImageTagParam: "image.tag", HostParam: "app.ingress.host",
			ValueFiles: []string{"values-preview.yaml"},
		}},
		{ImageTag: "sha-${ARGOCD_APP_REVISION_SHORT}", Chart: preview.ChartSettings{
			Path: "k8s/", ImageTagParam: "app.image.tag", HostParam: "app.ingress.host",
		}},
	}
	if len(got.Services) != len(want) {
		t.Fatalf("written %d services, want %d", len(got.Services), len(want))
	}
	for i, w := range want {
		if svc := got.Services[i]; svc.ImageTag != w.ImageTag || !reflect.DeepEqual(svc.Chart, w.Chart) {
			t.Errorf(
				"%s imageTag, chart = %q, %+v; want %q, %+v",
				svc.Name,
				svc.ImageTag,
				svc.Chart,
				w.ImageTag,
				w.Chart,
			)
		}
	}
}
