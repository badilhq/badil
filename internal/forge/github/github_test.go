package github_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
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
		rest = `"number":42,"user":{"login":"octocat"},"html_url":"https://github.com/acme/backend-1/pull/42","changed_files":3`
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
			Number:       42,
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

// pagedServer serves GET path, one page per entry of pages, linking each
// page to the next. Every request must carry per_page=100 and the values of
// query. A page that is "" answers 500.
func pagedServer(t *testing.T, path string, query url.Values, pages ...string) *httptest.Server {
	t.Helper()
	want := url.Values{"per_page": {"100"}}
	maps.Copy(want, query)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != path {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		for k := range want {
			if got := q.Get(k); got != want.Get(k) {
				t.Errorf("%s = %q, want %q", k, got, want.Get(k))
			}
		}
		page := 1
		if p := q.Get("page"); p != "" {
			_, _ = fmt.Sscan(p, &page)
		}
		if page < 1 || page > len(pages) || pages[page-1] == "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"boom"}`)
			return
		}
		if page < len(pages) {
			link := maps.Clone(want)
			link.Set("page", strconv.Itoa(page+1))
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?%s>; rel="next"`, r.Host, r.URL.Path, link.Encode()))
		}
		_, _ = io.WriteString(w, pages[page-1])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListPullRequestFiles(t *testing.T) {
	srv := pagedServer(t, "/repos/acme/platform/pulls/42/files", nil,
		`[{"filename":"web/src/app.tsx","status":"modified"},`+
			`{"filename":"tools/x.go","status":"renamed","previous_filename":"api/x.go"}]`,
		`[{"filename":"docs/README.md","status":"added"}]`,
	)
	got, err := newForge(t, srv).ListChangedFiles(context.Background(), "acme", "platform", 42)
	if err != nil {
		t.Fatalf("ListChangedFiles: %v", err)
	}
	want := []forge.ChangedFile{
		{Filename: "web/src/app.tsx"},
		{Filename: "tools/x.go", PreviousFilename: "api/x.go"},
		{Filename: "docs/README.md"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListChangedFiles = %+v, want %+v", got, want)
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
	srv := pagedServer(t, "/repos/acme/platform/pulls/42/files", nil, `[{"filename":"web/src/app.tsx"}]`, "")
	got, err := newForge(t, srv).ListChangedFiles(context.Background(), "acme", "platform", 42)
	if err == nil {
		t.Fatalf("ListChangedFiles = %+v, nil; want an error", got)
	}
	if got != nil {
		t.Errorf("files = %+v, want nil", got)
	}
}

// listedPR is a listed pull request of acme/platform, its head in headRepo.
func listedPR(number int, sha, headRepo, labels string) string {
	return fmt.Sprintf(`{"number":%d,"state":"open","html_url":"https://github.com/acme/platform/pull/%d",`+
		`"user":{"login":"octocat"},"labels":[%s],`+
		`"head":{"ref":"feature/%d","sha":"%s","repo":{"full_name":"%s"}},`+
		`"base":{"repo":{"full_name":"acme/platform"}}}`, number, number, labels, number, sha, headRepo)
}

func TestListOpenPullRequests(t *testing.T) {
	srv := pagedServer(t, "/repos/acme/platform/pulls", url.Values{"state": {"open"}},
		"["+listedPR(7, "aaa", "acme/platform", `{"name":"preview"}`)+","+
			listedPR(8, "bbb", "octocat/platform", "")+"]",
		"["+listedPR(9, "ccc", "acme/platform", `{"name":"preview:demo"},{"name":"bug"}`)+"]",
	)
	got, err := newForge(t, srv).ListOpenPullRequests(context.Background(), "acme", "platform")
	if err != nil {
		t.Fatalf("ListOpenPullRequests: %v", err)
	}
	pr := func(number int, sha, headRepo string, labels ...string) forge.PullRequest {
		if labels == nil {
			labels = []string{}
		}
		return forge.PullRequest{
			Open:         true,
			HeadRef:      "feature/" + strconv.Itoa(number),
			HeadSHA:      sha,
			Author:       "octocat",
			URL:          "https://github.com/acme/platform/pull/" + strconv.Itoa(number),
			Labels:       labels,
			FullName:     "acme/platform",
			HeadFullName: headRepo,
			Number:       number,
		}
	}
	want := []forge.PullRequest{
		pr(7, "aaa", "acme/platform", "preview"),
		pr(8, "bbb", "octocat/platform"),
		pr(9, "ccc", "acme/platform", "preview:demo", "bug"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListOpenPullRequests = %+v, want %+v", got, want)
	}
}

func TestListOpenPullRequests_FailingLaterPage(t *testing.T) {
	srv := pagedServer(t, "/repos/acme/platform/pulls", url.Values{"state": {"open"}},
		"["+listedPR(7, "aaa", "acme/platform", "")+"]", "")
	got, err := newForge(t, srv).ListOpenPullRequests(context.Background(), "acme", "platform")
	if err == nil {
		t.Fatalf("ListOpenPullRequests = %+v, nil; want an error", got)
	}
	if got != nil {
		t.Errorf("pull requests = %+v, want nil", got)
	}
}
