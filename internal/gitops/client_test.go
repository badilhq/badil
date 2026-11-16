package gitops_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	gh "github.com/google/go-github/v71/github"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// newClient wires a go-github client to a httptest server. The trailing slash
// on BaseURL is required by go-github's URL resolution.
func newClient(t *testing.T, srv *httptest.Server, opts ...gitops.Option) *gitops.Client {
	t.Helper()
	u, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c := gh.NewClient(nil)
	c.BaseURL = u
	c.UploadURL = u
	return gitops.NewClient(c, "acme", "gitops", opts...)
}

// encodeContent returns a Repository contents response body for a single file.
func encodeContent(path, sha, content string) string {
	payload := map[string]any{
		"name":     path[strings.LastIndex(path, "/")+1:],
		"path":     path,
		"sha":      sha,
		"size":     len(content),
		"type":     "file",
		"content":  base64.StdEncoding.EncodeToString([]byte(content)),
		"encoding": "base64",
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/gitops/contents/badil.yaml" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, encodeContent("badil.yaml", "sha-svc", badilYAML))
	}))
	defer srv.Close()

	c := newClient(t, srv)
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
		status   int
		content  string
		wantSHA  string
		wantErr  bool
		wantSvcs []string
	}{
		{
			name:     "parsed",
			status:   http.StatusOK,
			content:  "services:\n  front:\n    repo: https://github.com/acme/front.git\n",
			wantSHA:  "sha-badil",
			wantSvcs: []string{"front"},
		},
		{
			name:    "unparsable keeps the sha",
			status:  http.StatusOK,
			content: "services: [",
			wantSHA: "sha-badil",
			wantErr: true,
		},
		{name: "unreadable has no sha", status: http.StatusInternalServerError, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.status != http.StatusOK {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = io.WriteString(w, encodeContent("badil.yaml", "sha-badil", tc.content))
			}))
			defer srv.Close()

			got, sha, err := newClient(t, srv).FetchServiceCatalogSHA(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if sha != tc.wantSHA {
				t.Errorf("sha = %q, want %q", sha, tc.wantSHA)
			}
			if !tc.wantErr && !slices.Equal(got.Services, tc.wantSvcs) {
				t.Errorf("services = %v, want %v", got.Services, tc.wantSvcs)
			}
		})
	}
}

func TestFullName(t *testing.T) {
	if got := gitops.NewClient(gh.NewClient(nil), "acme", "gitops").FullName(); got != "acme/gitops" {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, encodeContent("previews/feature-add-pricing/values.yaml", "blob-sha-123", body))
	}))
	defer srv.Close()

	c := newClient(t, srv)
	vals, sha, err := c.GetPreview(context.Background(), "feature-add-pricing")
	if err != nil {
		t.Fatalf("GetPreview: %v", err)
	}
	if sha != "blob-sha-123" {
		t.Errorf("sha = %q, want blob-sha-123", sha)
	}
	if got := len(vals.Services); got != 3 {
		t.Fatalf("len(Services) = %d, want 3", got)
	}
	if got := vals.Services[0].Metadata.PRNumber; got != "42" {
		t.Errorf("metadata pr-number = %q, want 42", got)
	}
}

func TestGetPreview_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	c := newClient(t, srv)
	_, _, err := c.GetPreview(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, gitops.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestWritePreview_Create(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if _, hasSHA := req["sha"]; hasSHA {
			t.Errorf("create request must not include sha; got body: %s", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(
			w,
			`{"content":{"sha":"new-blob"},"commit":{"html_url":"https://github.com/acme/gitops/commit/abc"}}`,
		)
	}))
	defer srv.Close()

	c := newClient(t, srv)
	vals := preview.BuildPreviewValues("backend-1", "abc1234567890",
		[]string{"backend-1", "backend-2"}, sampleInputs())

	commitURL, err := c.WritePreview(context.Background(), "slug", vals, "", "chore(preview): create slug preview")
	if err != nil {
		t.Fatalf("WritePreview: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if commitURL != "https://github.com/acme/gitops/commit/abc" {
		t.Errorf("commitURL = %q", commitURL)
	}
}

func TestWritePreview_Update(t *testing.T) {
	var sawSHA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if s, ok := req["sha"].(string); ok {
			sawSHA = s
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"commit":{"html_url":"https://github.com/x/y/commit/upd"}}`)
	}))
	defer srv.Close()

	c := newClient(t, srv)
	vals := preview.BuildPreviewValues("backend-1", "newsha", []string{"backend-1"}, sampleInputs())
	commitURL, err := c.WritePreview(
		context.Background(),
		"slug",
		vals,
		"old-blob",
		"chore(preview): update backend-1 in slug",
	)
	if err != nil {
		t.Fatalf("WritePreview: %v", err)
	}
	if sawSHA != "old-blob" {
		t.Errorf("server saw sha = %q, want old-blob", sawSHA)
	}
	if !strings.HasSuffix(commitURL, "/upd") {
		t.Errorf("commitURL = %q", commitURL)
	}
}

func TestWritePreview_Unauthorized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"forbidden"}`)
			}))
			defer srv.Close()

			c := newClient(t, srv)
			vals := preview.BuildPreviewValues("backend-1", "sha", []string{"backend-1"}, sampleInputs())
			_, err := c.WritePreview(context.Background(), "slug", vals, "", "msg")
			if !errors.Is(err, gitops.ErrUnauthorized) {
				t.Errorf("expected ErrUnauthorized, got %v", err)
			}
		})
	}
}

// hostURLs exposes backend-1 under the domain 127.0.0.1.nip.io.
func hostURLs() preview.URLConfig {
	return preview.URLConfig{Domain: "127.0.0.1.nip.io", Expose: map[string]string{"backend-1": "/"}}
}

// testCatalog is a catalog of services, each at https://github.com/acme/<name>.git
// and tracking main, with the hosts of hostURLs.
func testCatalog(services ...string) *gitops.ServiceCatalog {
	repos := make(map[string]string, len(services))
	for _, name := range services {
		repos[name] = "https://github.com/acme/" + name + ".git"
	}
	return &gitops.ServiceCatalog{Catalog: preview.Catalog{Services: services, Repos: repos}, URLs: hostURLs()}
}

func TestWritePreviewWithRetry_ConflictThenSuccess(t *testing.T) {
	var (
		writeAttempts atomic.Int32
		getAttempts   atomic.Int32
		capturedBody  atomic.Value
	)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			attempt := getAttempts.Add(1)
			body := initialFile
			sha := "blob-1"
			if attempt > 1 {
				body = refreshedFile
				sha = "blob-2"
			}
			_, _ = io.WriteString(w, encodeContent("previews/slug/values.yaml", sha, body))
		case http.MethodPut:
			attempt := writeAttempts.Add(1)
			if attempt == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"message":"sha does not match"}`)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if got := req["sha"]; got != "blob-2" {
				t.Errorf("retry sha = %v, want blob-2", got)
			}
			content, _ := req["content"].(string)
			decoded, _ := base64.StdEncoding.DecodeString(content)
			capturedBody.Store(string(decoded))
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"commit":{"html_url":"https://github.com/x/y/commit/winner"}}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv)
	in := sampleInputs()
	in.CommitSha = "newsha"
	in.Timestamp = "2026-05-26T10:00:00Z"
	catalog := testCatalog("backend-1", "backend-2")
	catalog.Branches = map[string]string{"backend-2": "develop"}

	result, err := c.WritePreviewWithRetry(
		context.Background(), "slug",
		catalog,
		"backend-1", "newsha", in,
		"chore(preview): create slug preview",
		"chore(preview): update backend-1 in slug",
		false,
	)
	if err != nil {
		t.Fatalf("WritePreviewWithRetry: %v", err)
	}
	if writeAttempts.Load() != 2 {
		t.Errorf("write attempts = %d, want 2", writeAttempts.Load())
	}
	if getAttempts.Load() != 2 {
		t.Errorf("get attempts = %d, want 2 (initial + post-conflict)", getAttempts.Load())
	}
	if !strings.HasSuffix(result.CommitURL, "/winner") {
		t.Errorf("commitURL = %q", result.CommitURL)
	}
	if result.CommitMessage != "chore(preview): update backend-1 in slug" {
		t.Errorf("commitMessage = %q, want update form", result.CommitMessage)
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
	written, _ := capturedBody.Load().(string)
	assertCatalogFacts(t, decode(t, written), []string{
		"backend-1 https://github.com/acme/backend-1.git main",
		"backend-2 https://github.com/acme/backend-2.git develop",
	})
	assertWrittenHosts(t, result.Values, &capturedBody, "slug")
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
func assertWrittenHosts(t *testing.T, vals preview.PreviewValues, body *atomic.Value, slug string) {
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
	written, _ := body.Load().(string)
	if got := decode(t, written); !preview.Equal(got, vals) {
		t.Errorf("written = %+v, want the returned %+v", got, vals)
	}
	if strings.Count(written, "host:") != 1 {
		t.Errorf("written body has %d hosts, want 1:\n%s", strings.Count(written, "host:"), written)
	}
}

func TestWritePreviewWithRetry_FreshCreateUsesCatalog(t *testing.T) {
	var capturedBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if _, hasSHA := req["sha"]; hasSHA {
				t.Errorf("fresh create must not include sha; body: %s", body)
			}
			decoded, _ := base64.StdEncoding.DecodeString(req["content"].(string))
			capturedBody.Store(string(decoded))
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"commit":{"html_url":"https://github.com/x/y/commit/fresh"}}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv)
	catalog := testCatalog("backend-1", "backend-2", "front")
	catalog.Repos["front"] = "https://github.com/acme/front"
	catalog.Branches = map[string]string{"backend-2": "develop"}
	result, err := c.WritePreviewWithRetry(
		context.Background(), "fresh-slug",
		catalog,
		"backend-1", "abc1234567890", sampleInputs(),
		"chore(preview): create fresh-slug preview",
		"chore(preview): update backend-1 in fresh-slug",
		false,
	)
	if err != nil {
		t.Fatalf("WritePreviewWithRetry: %v", err)
	}
	if !strings.HasSuffix(result.CommitURL, "/fresh") {
		t.Errorf("commitURL = %q", result.CommitURL)
	}
	if result.CommitMessage != "chore(preview): create fresh-slug preview" {
		t.Errorf("commitMessage = %q, want create form", result.CommitMessage)
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
	written, _ := capturedBody.Load().(string)
	assertCatalogFacts(t, decode(t, written), []string{
		"backend-1 https://github.com/acme/backend-1.git main",
		"backend-2 https://github.com/acme/backend-2.git develop",
		"front https://github.com/acme/front main",
	})
	assertWrittenHosts(t, result.Values, &capturedBody, "fresh-slug")
}

func TestFanOutPRComments_UpdateAndCreate(t *testing.T) {
	// Two services with PR URLs:
	//   backend-1: PR #42 → existing marker comment → expect PATCH
	//   backend-2: PR #7  → no existing comment    → expect POST
	var sawEdit, sawCreate atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/"):
			_, _ = io.WriteString(w, `{"state":"open"}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/42/comments"):
			_, _ = io.WriteString(w, `[{"id": 999, "body": "<!-- badil:preview -->\n old body"}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/7/comments"):
			_, _ = io.WriteString(w, `[{"id": 111, "body": "unrelated"}]`)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/issues/comments/999"):
			sawEdit.Store(true)
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "<!-- badil:preview -->") {
				t.Errorf("PATCH body missing marker: %s", body)
			}
			_, _ = io.WriteString(w, `{"id":999}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/7/comments"):
			sawCreate.Store(true)
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "<!-- badil:preview -->") {
				t.Errorf("POST body missing marker: %s", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":222}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv)

	in1 := sampleInputs()
	in2 := sampleInputs()
	in2.ServiceName = "backend-2"
	in2.PRURL = "https://github.com/acme/backend-2/pull/7"
	in2.PRNumber = "7"

	vals := preview.PreviewValues{Services: []preview.ServiceEntry{
		{Name: "backend-1", CommitSha: "abc", Metadata: preview.BuildServiceMetadata(in1)},
		{Name: "backend-2", CommitSha: "def", Metadata: preview.BuildServiceMetadata(in2)},
	}}

	if err := c.FanOutPRComments(context.Background(), vals, "## body"); err != nil {
		t.Fatalf("FanOutPRComments: %v", err)
	}
	if !sawEdit.Load() {
		t.Error("expected PATCH on existing comment for PR #42")
	}
	if !sawCreate.Load() {
		t.Error("expected POST for PR #7")
	}
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

// assertWritten fails t unless s received one write whose content parses to
// want (preview.Equal).
func assertWritten(t *testing.T, s *writeServer, want preview.PreviewValues) {
	t.Helper()
	written, _ := s.written.Load().(string)
	if got := decode(t, written); !preview.Equal(got, want) {
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

// writeServer serves files to successive GETs of previews/slug/values.yaml
// (the last one repeats; blob-<n> is the n-th file's sha; an empty file or no
// file is a 404) and answers PUTs with conflicts first, then success. It
// counts the requests and keeps the content and message of the last
// successful PUT.
type writeServer struct {
	files     []string
	conflicts int32
	gets      atomic.Int32
	puts      atomic.Int32
	written   atomic.Value
	message   atomic.Value
}

func (s *writeServer) start(t *testing.T, opts ...gitops.Option) *gitops.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			n := int(s.gets.Add(1))
			i := min(n, len(s.files)) - 1
			if i < 0 || s.files[i] == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"Not Found"}`)
				return
			}
			_, _ = io.WriteString(
				w,
				encodeContent("previews/slug/values.yaml", fmt.Sprintf("blob-%d", i+1), s.files[i]),
			)
		case http.MethodPut:
			if s.puts.Add(1) <= s.conflicts {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"message":"sha does not match"}`)
				return
			}
			var req struct {
				Content []byte `json:"content"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode PUT body: %v", err)
			}
			s.written.Store(string(req.Content))
			s.message.Store(req.Message)
			_, _ = io.WriteString(w, `{"commit":{"html_url":"https://github.com/x/y/commit/written"}}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(t, srv, opts...)
}

func TestWritePreviewWithRetry_SkipUnchanged(t *testing.T) {
	tests := []struct {
		name          string
		files         []string
		conflicts     int32
		catalog       func(*gitops.ServiceCatalog)
		skipUnchanged bool
		wantUnchanged bool
		wantGets      int32
		wantPuts      int32
	}{
		{
			name:  "unchanged",
			files: []string{pinnedFile()}, skipUnchanged: true, wantUnchanged: true, wantGets: 1,
		},
		{
			name:  "unchanged without skipUnchanged",
			files: []string{pinnedFile()}, wantGets: 1, wantPuts: 1,
		},
		{
			name:  "new head sha",
			files: []string{pinnedFile("abc1234567890", "0000000old")}, skipUnchanged: true, wantGets: 1, wantPuts: 1,
		},
		{
			name:  "changed host",
			files: []string{pinnedFile("    host: backend-1-slug.127.0.0.1.nip.io\n", "")}, skipUnchanged: true,
			wantGets: 1, wantPuts: 1,
		},
		{
			name:  "changed pr metadata",
			files: []string{pinnedFile("pr-author: octocat", "pr-author: someone")}, skipUnchanged: true,
			wantGets: 1, wantPuts: 1,
		},
		{
			name:  "changed head ref",
			files: []string{pinnedFile("vcs.ref.name: feature/add-pricing", "vcs.ref.name: old")}, skipUnchanged: true,
			wantGets: 1, wantPuts: 1,
		},
		{
			name:  "catalog branch change",
			files: []string{pinnedFile()}, skipUnchanged: true, wantGets: 1, wantPuts: 1,
			catalog: func(c *gitops.ServiceCatalog) { c.Branches = map[string]string{"backend-2": "develop"} },
		},
		{
			name:  "catalog repo url change",
			files: []string{pinnedFile()}, skipUnchanged: true, wantGets: 1, wantPuts: 1,
			catalog: func(c *gitops.ServiceCatalog) { c.Repos["backend-2"] = "https://github.com/acme/b2.git" },
		},
		{
			name:  "service removed from the catalog",
			files: []string{pinnedFile()}, skipUnchanged: true, wantGets: 1, wantPuts: 1,
			catalog: func(c *gitops.ServiceCatalog) { delete(c.Repos, "backend-2"); c.Services = c.Services[:1] },
		},
		{
			name: "file without apiVersion",
			files: []string{
				pinnedFile("apiVersion: badil.dev/v1beta1\n", ""),
			},
			skipUnchanged: true,
			wantGets:      1,
			wantPuts:      1,
		},
		{
			name:  "unchanged after a 409 re-fetch",
			files: []string{pinnedFile("abc1234567890", "0000000old"), pinnedFile()}, conflicts: 1, skipUnchanged: true,
			wantUnchanged: true, wantGets: 2, wantPuts: 1,
		},
		{
			name: "changed after a 409 re-fetch",
			files: []string{
				pinnedFile("abc1234567890", "0000000old"),
				pinnedFile("abc1234567890", "0000000other"),
			},
			conflicts: 1, skipUnchanged: true, wantGets: 2, wantPuts: 2,
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

			result, err := c.WritePreviewWithRetry(
				context.Background(), "slug", catalog,
				"backend-1", "abc1234567890", sampleInputs(), "create", "update", tt.skipUnchanged,
			)
			if err != nil {
				t.Fatalf("WritePreviewWithRetry: %v", err)
			}
			if gets, puts := s.gets.Load(), s.puts.Load(); gets != tt.wantGets || puts != tt.wantPuts {
				t.Errorf("gets, puts = %d, %d; want %d, %d", gets, puts, tt.wantGets, tt.wantPuts)
			}
			assertSkipResult(t, result, tt.wantUnchanged)
		})
	}
}

// assertSkipResult checks result is an update written as "update", or, when
// unchanged, the fetched file with no commit.
func assertSkipResult(t *testing.T, result *gitops.WriteResult, unchanged bool) {
	t.Helper()
	if result.Unchanged != unchanged || result.Created {
		t.Errorf("Unchanged, Created = %v, %v; want %v, false", result.Unchanged, result.Created, unchanged)
	}
	if !unchanged {
		if result.CommitURL != "https://github.com/x/y/commit/written" || result.CommitMessage != "update" {
			t.Errorf("CommitURL, CommitMessage = %q, %q", result.CommitURL, result.CommitMessage)
		}
		return
	}
	if result.CommitURL != "" || result.CommitMessage != "" {
		t.Errorf("CommitURL, CommitMessage = %q, %q; want empty", result.CommitURL, result.CommitMessage)
	}
	if got := result.Values.Services[0].Metadata.UpdatedAt; got != "2026-05-26T09:00:00Z" {
		t.Errorf("Values updated-at = %q, want the fetched file's", got)
	}
}

// TestWritePreviewWithRetry_Created checks Created and that only a create
// increments the previews gauge, from 1.
func TestWritePreviewWithRetry_Created(t *testing.T) {
	tests := []struct {
		name      string
		files     []string
		conflicts int32
		want      bool
	}{
		{name: "create", want: true},
		{name: "update", files: []string{pinnedFile("abc1234567890", "0000000old")}},
		{name: "create lost to a 409", files: []string{"", pinnedFile("abc1234567890", "0000000old")}, conflicts: 1},
		{name: "unchanged", files: []string{pinnedFile()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &writeServer{files: tt.files, conflicts: tt.conflicts}
			m := metrics.New()
			m.PreviewsListed(1)
			c := s.start(t, gitops.WithMetrics(m))

			result, err := c.WritePreviewWithRetry(
				context.Background(), "slug", testCatalog("backend-1", "backend-2"),
				"backend-1", "abc1234567890", sampleInputs(), "create", "update", tt.name == "unchanged",
			)
			if err != nil {
				t.Fatalf("WritePreviewWithRetry: %v", err)
			}
			if result.Created != tt.want {
				t.Errorf("Created = %v, want %v", result.Created, tt.want)
			}
			want := "1"
			if tt.want {
				want = "2"
			}
			expected := "# HELP badil_previews Previews in the gitops repo: listed at startup, " +
				"counted on each create and delete by this server, and reset by each in-server cleanup sweep.\n" +
				"# TYPE badil_previews gauge\nbadil_previews " + want + "\n"
			if cmpErr := testutil.GatherAndCompare(
				m.Gatherer(),
				strings.NewReader(expected),
				"badil_previews",
			); cmpErr != nil {
				t.Error(cmpErr)
			}
		})
	}
}

// TestWritePreviewWithRetry_FollowsCatalog checks that a deploy update writes
// apiVersion and every catalog service in catalog order with its repoURL and
// branch from the catalog, then keeps a pinned entry of a removed service as
// it is (its imageTag and chart as last written), and drops an unpinned one
// and a pinned one without repoURL.
func TestWritePreviewWithRetry_FollowsCatalog(t *testing.T) {
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

	result, err := c.WritePreviewWithRetry(
		context.Background(), "slug", catalog,
		"backend-1", "abc1234567890", sampleInputs(), "create", "update", true,
	)
	if err != nil {
		t.Fatalf("WritePreviewWithRetry: %v", err)
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

// TestWritePreviewWithRetry_AddsMissingService checks that a deploy to an
// existing preview writes a catalog service the file lacks as a
// branch-tracking entry, in catalog order.
func TestWritePreviewWithRetry_AddsMissingService(t *testing.T) {
	s := &writeServer{files: []string{pinnedFile()}}
	c := s.start(t)
	catalog := testCatalog("backend-1", "front", "backend-2")

	result, err := c.WritePreviewWithRetry(
		context.Background(), "slug", catalog,
		"backend-1", "abc1234567890", sampleInputs(), "create", "update", true,
	)
	if err != nil {
		t.Fatalf("WritePreviewWithRetry: %v", err)
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
	written, _ := s.written.Load().(string)
	got := decode(t, written)
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

// TestWritePreviewWithRetry_BranchChangeWritten checks that a catalog branch
// change is written even when the commit is unchanged.
func TestWritePreviewWithRetry_BranchChangeWritten(t *testing.T) {
	s := &writeServer{files: []string{pinnedFile()}}
	c := s.start(t)
	catalog := testCatalog("backend-1", "backend-2")
	catalog.Branches = map[string]string{"backend-2": "develop"}

	result, err := c.WritePreviewWithRetry(
		context.Background(), "slug", catalog,
		"backend-1", "abc1234567890", sampleInputs(), "create", "update", true,
	)
	if err != nil {
		t.Fatalf("WritePreviewWithRetry: %v", err)
	}
	if result.Unchanged || result.Values.Services[1].Branch != "develop" {
		t.Fatalf("result = %+v, want a write with backend-2 on develop", result)
	}
	if got, _ := s.written.Load().(string); !strings.Contains(
		got,
		"  - name: backend-2\n    repoURL: https://github.com/acme/backend-2.git\n    branch: develop\n",
	) {
		t.Errorf("written content missing backend-2 on develop:\n%s", got)
	}
}

// TestWritePreviewWithRetry_RendererSettings checks that a deploy writes the
// catalog's namespace metadata and, on every entry, its chart settings with
// their defaults applied and its image tag format expanded.
func TestWritePreviewWithRetry_RendererSettings(t *testing.T) {
	const sha = "f913b6cb1a2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f"
	s := &writeServer{}
	c := s.start(t)
	catalog := testCatalog("backend-1", "backend-2")
	catalog.ImageTag = "sha-{sha7}"
	catalog.Charts = map[string]preview.ChartSettings{
		"backend-1": {Path: "deploy/helm", ImageTagParam: "image.tag", ValueFiles: []string{"values-preview.yaml"}},
	}
	catalog.Namespace = &preview.NamespaceMetadata{Labels: map[string]string{"team": "platform"}}

	if _, err := c.WritePreviewWithRetry(
		context.Background(), "slug", catalog, "backend-1", sha, sampleInputs(), "create", "update", false,
	); err != nil {
		t.Fatalf("WritePreviewWithRetry: %v", err)
	}
	written, _ := s.written.Load().(string)
	got := decode(t, written)
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
