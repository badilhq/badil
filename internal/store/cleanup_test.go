package store_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

func TestListPreviews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/gitops/contents/previews" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `[
			{"type":"dir","name":"feature-b","path":"previews/feature-b"},
			{"type":"file","name":"README.md","path":"previews/README.md"},
			{"type":"dir","name":"feature-a","path":"previews/feature-a"}
		]`)
	}))
	defer srv.Close()

	got, err := newClient(t, srv).ListPreviews(context.Background())
	if err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	if want := []string{"feature-a", "feature-b"}; !slices.Equal(got, want) {
		t.Errorf("ListPreviews = %v, want %v", got, want)
	}
}

func TestListPreviews_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	got, err := newClient(t, srv).ListPreviews(context.Background())
	if err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("ListPreviews = %#v, want an empty slice", got)
	}
}

// singlePin reports whether exactly one entry of vals is pinned; it turns
// false once a second PR deploys to the preview.
func singlePin(vals preview.PreviewValues) bool {
	n := 0
	for _, svc := range vals.Services {
		if svc.CommitSha != "" {
			n++
		}
	}
	return n == 1
}

func runDelete(
	t *testing.T,
	g *teardownGitHub,
	stillDelete func(preview.PreviewValues) bool,
) (*store.DeleteResult, error) {
	t.Helper()
	g.t = t
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return newClient(t, srv).DeletePreviewWithRetry(
		context.Background(), "feature-x", "chore(preview): expire feature-x preview", stillDelete,
	)
}

func TestDeletePreviewWithRetry_Deletes(t *testing.T) {
	g := &teardownGitHub{gets: []fileState{{sha: "blob-1", body: onlyPR42}}}
	res, err := runDelete(t, g, singlePin)
	if err != nil {
		t.Fatalf("DeletePreviewWithRetry: %v", err)
	}
	if !res.Deleted || res.CommitURL != "https://github.com/acme/gitops/commit/c0ffee" {
		t.Errorf("result = %+v, want deleted with the commit url", res)
	}
	if len(res.Values.Services) != 2 || res.Values.Services[0].CommitSha != "aaa" {
		t.Errorf("Values = %+v, want the deleted file", res.Values)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodDelete || reqs[0].sha != "blob-1" ||
		reqs[0].message != "chore(preview): expire feature-x preview" {
		t.Errorf("requests = %+v, want one DELETE at blob-1", reqs)
	}
}

func TestDeletePreviewWithRetry_StillDeleteFalse(t *testing.T) {
	g := &teardownGitHub{gets: []fileState{{sha: "blob-1", body: pr42AndPR7}}}
	res, err := runDelete(t, g, singlePin)
	if err != nil {
		t.Fatalf("DeletePreviewWithRetry: %v", err)
	}
	if res.Deleted {
		t.Errorf("result = %+v, want nothing deleted", res)
	}
	if reqs := g.requests(); len(reqs) != 0 {
		t.Errorf("requests = %+v, want no DELETE", reqs)
	}
}

func TestDeletePreviewWithRetry_ConflictThenRecheckFalse(t *testing.T) {
	g := &teardownGitHub{
		gets:   []fileState{{sha: "blob-1", body: onlyPR42}, {sha: "blob-2", body: pr42AndPR7}},
		writes: []int{http.StatusConflict},
	}
	res, err := runDelete(t, g, singlePin)
	if err != nil {
		t.Fatalf("DeletePreviewWithRetry: %v", err)
	}
	if res.Deleted {
		t.Errorf("result = %+v, want nothing deleted", res)
	}
	if reqs := g.requests(); len(reqs) != 1 {
		t.Errorf("requests = %+v, want a single DELETE", reqs)
	}
}

func TestDeletePreviewWithRetry_LastConflictThenRecheckFalse(t *testing.T) {
	gets := slices.Repeat([]fileState{{sha: "blob-1", body: onlyPR42}}, store.MaxWriteRetries)
	g := &teardownGitHub{
		gets:   append(gets, fileState{sha: "blob-2", body: pr42AndPR7}),
		writes: slices.Repeat([]int{http.StatusConflict}, store.MaxWriteRetries),
	}
	res, err := runDelete(t, g, singlePin)
	if err != nil {
		t.Fatalf("DeletePreviewWithRetry: %v", err)
	}
	if res.Deleted {
		t.Errorf("result = %+v, want nothing deleted", res)
	}
	if got := len(g.requests()); got != store.MaxWriteRetries {
		t.Errorf("deletes = %d, want %d", got, store.MaxWriteRetries)
	}
}

func TestDeletePreviewWithRetry_NotFound(t *testing.T) {
	cases := []struct {
		name    string
		gets    []fileState
		writes  []int
		deletes int
	}{
		{name: "on read", gets: []fileState{{}}},
		{
			name:    "on re-fetch",
			gets:    []fileState{{sha: "blob-1", body: onlyPR42}, {}},
			writes:  []int{http.StatusConflict},
			deletes: 1,
		},
		{
			name:    "on delete",
			gets:    []fileState{{sha: "blob-1", body: onlyPR42}},
			writes:  []int{http.StatusNotFound},
			deletes: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &teardownGitHub{gets: tc.gets, writes: tc.writes}
			res, err := runDelete(t, g, singlePin)
			if err != nil {
				t.Fatalf("DeletePreviewWithRetry: %v", err)
			}
			if res.Deleted {
				t.Errorf("result = %+v, want nothing deleted", res)
			}
			if got := len(g.requests()); got != tc.deletes {
				t.Errorf("deletes = %d, want %d", got, tc.deletes)
			}
		})
	}
}

func TestDeletePreviewWithRetry_ConflictsExhausted(t *testing.T) {
	g := &teardownGitHub{
		gets:   []fileState{{sha: "blob-1", body: onlyPR42}},
		writes: slices.Repeat([]int{http.StatusConflict}, store.MaxWriteRetries),
	}
	_, err := runDelete(t, g, singlePin)
	const want = "delete previews/feature-x/values.yaml: 3 attempts exhausted"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if got := len(g.requests()); got != store.MaxWriteRetries {
		t.Errorf("deletes = %d, want %d", got, store.MaxWriteRetries)
	}
}
