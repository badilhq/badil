package gitops_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/preview"
)

// newClient wires a go-github client to a httptest server. The trailing slash
// on BaseURL is required by go-github's URL resolution.
func newClient(t *testing.T, srv *httptest.Server) *gitops.Client {
	t.Helper()
	u, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c := gh.NewClient(nil)
	c.BaseURL = u
	c.UploadURL = u
	return gitops.NewClient(c, "acme", "gitops")
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
	const servicesYAML = `serviceRepos:
  backend-1: https://github.com/acme/backend-1.git
  backend-2: https://github.com/acme/backend-2.git
  front: https://github.com/acme/front.git
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/gitops/contents/services.yaml" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, encodeContent("services.yaml", "sha-svc", servicesYAML))
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
		_, _ = io.WriteString(w, `{"content":{"sha":"new-blob"},"commit":{"html_url":"https://github.com/acme/gitops/commit/abc"}}`)
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
	commitURL, err := c.WritePreview(context.Background(), "slug", vals, "old-blob", "chore(preview): update backend-1 in slug")
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

	result, err := c.WritePreviewWithRetry(
		context.Background(), "slug",
		[]string{"backend-1", "backend-2"},
		hostURLs(),
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
	assertWrittenHosts(t, result.Values, &capturedBody, "slug")
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
	if !strings.Contains(written, "    host: "+want+"\n") {
		t.Errorf("written body missing host %q:\n%s", want, written)
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
	result, err := c.WritePreviewWithRetry(
		context.Background(), "fresh-slug",
		[]string{"backend-1", "backend-2", "front"},
		hostURLs(),
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
	for _, expect := range []string{"name: backend-1", "name: backend-2", "name: front"} {
		if !strings.Contains(written, expect) {
			t.Errorf("written body missing %q:\n%s", expect, written)
		}
	}
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

// pinnedFile is previews/slug/values.yaml as a deploy of sampleInputs with
// hostURLs writes it, except updated-at, which is older. Its replacer
// arguments are applied to it.
func pinnedFile(replace ...string) string {
	const file = `services:
  - name: backend-1
    commitSha: abc1234567890
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
`
	return strings.NewReplacer(replace...).Replace(file)
}

// writeServer serves files to successive GETs of previews/slug/values.yaml
// (the last one repeats; blob-<n> is the n-th file's sha; an empty file or no
// file is a 404) and answers PUTs with conflicts first, then success. It
// counts the requests.
type writeServer struct {
	files     []string
	conflicts int32
	gets      atomic.Int32
	puts      atomic.Int32
}

func (s *writeServer) start(t *testing.T) *gitops.Client {
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
			_, _ = io.WriteString(w, `{"commit":{"html_url":"https://github.com/x/y/commit/written"}}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(t, srv)
}

func TestWritePreviewWithRetry_SkipUnchanged(t *testing.T) {
	tests := []struct {
		name          string
		files         []string
		conflicts     int32
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

			result, err := c.WritePreviewWithRetry(
				context.Background(), "slug", []string{"backend-1", "backend-2"}, hostURLs(),
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &writeServer{files: tt.files, conflicts: tt.conflicts}
			c := s.start(t)

			result, err := c.WritePreviewWithRetry(
				context.Background(), "slug", []string{"backend-1", "backend-2"}, hostURLs(),
				"backend-1", "abc1234567890", sampleInputs(), "create", "update", false,
			)
			if err != nil {
				t.Fatalf("WritePreviewWithRetry: %v", err)
			}
			if result.Created != tt.want {
				t.Errorf("Created = %v, want %v", result.Created, tt.want)
			}
		})
	}
}
