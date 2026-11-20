package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/forge"
	forgegithub "github.com/badilhq/badil/internal/forge/github"
)

// newForge wires a go-github client to a httptest server. The trailing slash
// on BaseURL is required by go-github's URL resolution.
func newForge(t *testing.T, srv *httptest.Server) *forgegithub.Forge {
	t.Helper()
	u, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c := gh.NewClient(nil)
	c.BaseURL = u
	c.UploadURL = u
	return forgegithub.New(c, nil, 0)
}

func TestGetPullRequest(t *testing.T) {
	const (
		head = `"head":{"ref":"feature/pricing","sha":"abc123","repo":{"full_name":"%s"}}`
		base = `"base":{"repo":{"full_name":"acme/backend-1"}}`
		rest = `"user":{"login":"octocat"},"html_url":"https://github.com/acme/backend-1/pull/42","changed_files":3`
	)
	pr := func(open bool, headFullName string, labels ...string) *forge.PullRequest {
		if labels == nil {
			labels = []string{}
		}
		return &forge.PullRequest{
			Open:         open,
			HeadRef:      "feature/pricing",
			HeadSHA:      "abc123",
			Author:       "octocat",
			URL:          "https://github.com/acme/backend-1/pull/42",
			Labels:       labels,
			FullName:     "acme/backend-1",
			HeadFullName: headFullName,
			ChangedFiles: 3,
		}
	}
	cases := []struct {
		name       string
		status     int
		body       string
		want       *forge.PullRequest
		wantErr    error
		wantAnyErr bool
	}{
		{
			name:   "open with labels",
			status: http.StatusOK,
			body: `{"state":"open",` + fmt.Sprintf(head, "acme/backend-1") + `,` + base + `,` + rest +
				`,"labels":[{"name":"preview"},{"name":"bug"}]}`,
			want: pr(true, "acme/backend-1", "preview", "bug"),
		},
		{
			name:   "closed",
			status: http.StatusOK,
			body:   `{"state":"closed",` + fmt.Sprintf(head, "acme/backend-1") + `,` + base + `,` + rest + `}`,
			want:   pr(false, "acme/backend-1"),
		},
		{
			name:   "fork head",
			status: http.StatusOK,
			body:   `{"state":"open",` + fmt.Sprintf(head, "octocat/backend-1") + `,` + base + `,` + rest + `}`,
			want:   pr(true, "octocat/backend-1"),
		},
		{name: "not found", status: http.StatusNotFound, body: `{"message":"Not Found"}`, wantErr: forge.ErrNotFound},
		{
			name: "unauthorized", status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`,
			wantErr: forge.ErrUnauthorized,
		},
		{
			name: "forbidden", status: http.StatusForbidden, body: `{"message":"Resource not accessible"}`,
			wantErr: forge.ErrUnauthorized,
		},
		{name: "server error", status: http.StatusInternalServerError, body: `{"message":"boom"}`, wantAnyErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := getPullRequest(t, tc.status, tc.body)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if !strings.HasPrefix(err.Error(), tc.wantErr.Error()+": ") {
					t.Errorf("err = %q, want it to start with %q", err, tc.wantErr.Error()+": ")
				}
			case tc.wantAnyErr:
				if err == nil {
					t.Fatalf("GetPullRequest = %+v, nil; want an error", got)
				}
			case err != nil:
				t.Fatalf("GetPullRequest: %v", err)
			case !reflect.DeepEqual(got, tc.want):
				t.Errorf("GetPullRequest = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// getPullRequest calls GetPullRequest on acme/backend-1#42 against a server
// answering status and body.
func getPullRequest(t *testing.T, status int, body string) (*forge.PullRequest, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/backend-1/pulls/42" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	return newForge(t, srv).GetPullRequest(context.Background(), "acme", "backend-1", 42)
}

// filesPages serves GET /repos/acme/platform/pulls/42/files, one page per
// entry of pages, linking each page to the next. A page that is "" answers
// 500.
func filesPages(t *testing.T, pages ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/platform/pulls/42/files" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			_, _ = fmt.Sscan(p, &page)
		}
		if page < 1 || page > len(pages) || pages[page-1] == "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"boom"}`)
			return
		}
		if page < len(pages) {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?per_page=100&page=%d>; rel="next"`,
				"http://"+r.Host, r.URL.Path, page+1))
		}
		_, _ = io.WriteString(w, pages[page-1])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListPullRequestFiles(t *testing.T) {
	srv := filesPages(t,
		`[{"filename":"web/src/app.tsx","status":"modified"},`+
			`{"filename":"tools/x.go","status":"renamed","previous_filename":"api/x.go"}]`,
		`[{"filename":"docs/README.md","status":"added"}]`,
	)
	got, err := newForge(t, srv).ListChangedFiles(context.Background(), "acme", "platform", 42)
	if err != nil {
		t.Fatalf("ListPullRequestFiles: %v", err)
	}
	want := []forge.ChangedFile{
		{Filename: "web/src/app.tsx"},
		{Filename: "tools/x.go", PreviousFilename: "api/x.go"},
		{Filename: "docs/README.md"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListPullRequestFiles = %+v, want %+v", got, want)
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"denied"}`)
			}))
			defer denied.Close()
			files, denyErr := newForge(t, denied).ListChangedFiles(context.Background(), "acme", "platform", 42)
			if !errors.Is(denyErr, forge.ErrUnauthorized) {
				t.Fatalf("ListChangedFiles = %+v, %v; want %v", files, denyErr, forge.ErrUnauthorized)
			}
			if want := "gitops: unauthorized: "; !strings.HasPrefix(denyErr.Error(), want) {
				t.Errorf("err = %q, want it to start with %q", denyErr, want)
			}
		})
	}
}

func TestListPullRequestFiles_FailingLaterPage(t *testing.T) {
	srv := filesPages(t, `[{"filename":"web/src/app.tsx"}]`, "")
	got, err := newForge(t, srv).ListChangedFiles(context.Background(), "acme", "platform", 42)
	if err == nil {
		t.Fatalf("ListPullRequestFiles = %+v, nil; want an error", got)
	}
	if got != nil {
		t.Errorf("files = %+v, want nil", got)
	}
}

func TestUpsertPRComment_CreatesWithMarker(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/backend-1/issues/42/comments":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/backend-1/issues/42/comments":
			var req struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			body = req.Body
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":1}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	if err := newForge(t, srv).UpsertComment(context.Background(), "acme", "backend-1", 42, "## gone"); err != nil {
		t.Fatalf("UpsertPRComment: %v", err)
	}
	if want := forgegithub.PRCommentMarker + "\n## gone"; body != want {
		t.Errorf("comment body = %q, want %q", body, want)
	}
}
