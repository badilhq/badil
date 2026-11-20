package store_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// TestListPreviews checks that the previews are listed in lexical order, which
// differs from git's tree order for "feature" and "feature-x": git sorts a
// directory as "feature/", after "feature-x/".
func TestListPreviews(t *testing.T) {
	c, _ := readStore(t, map[string]string{
		"previews/feature-b/values.yaml": "services: []\n",
		"previews/README.md":             "previews\n",
		"previews/feature-a/values.yaml": "services: []\n",
		"previews/feature-x/values.yaml": "services: []\n",
		"previews/feature/values.yaml":   "services: []\n",
	})
	got, err := c.ListPreviews(context.Background())
	if err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	if want := []string{"feature", "feature-a", "feature-b", "feature-x"}; !slices.Equal(got, want) {
		t.Errorf("ListPreviews = %v, want %v", got, want)
	}
}

// TestListPreviews_NoLimit checks that every preview is listed, past the
// contents API's 1,000 entries.
func TestListPreviews_NoLimit(t *testing.T) {
	files := map[string]string{}
	want := make([]string, 0, 1500)
	for i := range 1500 {
		slug := fmt.Sprintf("preview-%04d", i)
		files["previews/"+slug+"/values.yaml"] = "services: []\n"
		want = append(want, slug)
	}
	c, _ := readStore(t, files)
	got, err := c.ListPreviews(context.Background())
	if err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListPreviews returned %d previews, want the %d sorted", len(got), len(want))
	}
}

func TestListPreviews_NotFound(t *testing.T) {
	c, _ := readStore(t, map[string]string{"badil.yaml": "services: {}\n"})
	got, err := c.ListPreviews(context.Background())
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
	return g.open(t).DeletePreviewWithRetry(
		context.Background(), "feature-x", "chore(preview): expire feature-x preview", stillDelete,
	)
}

func TestDeletePreviewWithRetry_Deletes(t *testing.T) {
	g := &teardownGitHub{gets: []string{onlyPR42}}
	res, err := runDelete(t, g, singlePin)
	if err != nil {
		t.Fatalf("DeletePreviewWithRetry: %v", err)
	}
	if !res.Deleted || res.CommitURL != fakeCommitURL {
		t.Errorf("result = %+v, want deleted with the commit url", res)
	}
	if len(res.Values.Services) != 2 || res.Values.Services[0].CommitSha != "aaa" {
		t.Errorf("Values = %+v, want the deleted file", res.Values)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodDelete || reqs[0].sha != blobSHA(onlyPR42) ||
		reqs[0].message != "chore(preview): expire feature-x preview" {
		t.Errorf("requests = %+v, want one DELETE at the file's blob", reqs)
	}
}

func TestDeletePreviewWithRetry_StillDeleteFalse(t *testing.T) {
	g := &teardownGitHub{gets: []string{pr42AndPR7}}
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
		gets:   []string{onlyPR42, pr42AndPR7},
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
	gets := slices.Repeat([]string{onlyPR42}, store.MaxWriteRetries)
	g := &teardownGitHub{
		gets:   append(gets, pr42AndPR7),
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
		gets    []string
		writes  []int
		deletes int
	}{
		{name: "on read", gets: []string{""}},
		{
			name:    "on re-fetch",
			gets:    []string{onlyPR42, ""},
			writes:  []int{http.StatusConflict},
			deletes: 1,
		},
		{
			name:    "on delete",
			gets:    []string{onlyPR42},
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
		gets:   []string{onlyPR42},
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
