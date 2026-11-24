package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
)

// teardownFile is the values file of the preview feature-x.
const teardownFile = "previews/feature-x/values.yaml"

// onlyPR42 is a preview pinned by acme/backend-1#42 alone.
const onlyPR42 = `apiVersion: badil.dev/v1beta1
gitopsRepoURL: https://github.com/acme/gitops.git
services:
  - name: backend-1
    repoURL: https://github.com/acme/backend-1.git
    branch: main
    commitSha: aaa
    metadata:
      pr-url: https://github.com/acme/backend-1/pull/42
  - name: front
    repoURL: https://github.com/acme/front.git
    branch: main
dependencies: []
`

// pr42AndPR7 is a preview pinned by acme/backend-1#42 and acme/backend-2#7.
const pr42AndPR7 = `apiVersion: badil.dev/v1beta1
gitopsRepoURL: https://github.com/acme/gitops.git
services:
  - name: backend-1
    repoURL: https://github.com/acme/backend-1.git
    branch: release
    commitSha: aaaaaaaaaa
    imageTag: sha-aaaaaaa
    chart: {path: k8s/, imageTagParam: app.image.tag, hostParam: app.ingress.host}
    host: backend-1-feature-x.127.0.0.1.nip.io
    metadata:
      pr-url: https://github.com/acme/backend-1/pull/42
  - name: backend-2
    repoURL: https://github.com/acme/backend-2.git
    branch: main
    commitSha: bbbbbbbbbb
    imageTag: sha-bbbbbbb
    chart: {path: k8s/, imageTagParam: app.image.tag, hostParam: app.ingress.host}
    metadata:
      pr-url: https://github.com/acme/backend-2/pull/7
  - name: front
    repoURL: https://github.com/acme/front.git
    branch: main
    imageTag: sha-${ARGOCD_APP_REVISION_SHORT}
    chart: {path: k8s/, imageTagParam: app.image.tag, hostParam: app.ingress.host}
dependencies: []
`

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

func TestTree_DeletePreview(t *testing.T) {
	f := newGitopsFake(t, map[string]string{
		teardownFile:                  onlyPR42,
		"previews/other/values.yaml":  pr42AndPR7,
		"previews/other/README.md":    "kept\n",
		"previews/kept/values.yaml":   pr42AndPR7,
		"previews/kept/notes/a.txt":   "a\n",
		"previews/single/values.yaml": onlyPR42,
	})
	c := f.open(t)
	var listed []string
	_, err := apply(c, lifecycle.OpSweep, func(tree lifecycle.Tree) (lifecycle.Summary, error) {
		for _, slug := range []string{"feature-x", "other", "single"} {
			tree.DeletePreview(slug)
		}
		if _, getErr := tree.GetPreview("feature-x"); !errors.Is(getErr, lifecycle.ErrNotFound) {
			t.Errorf("GetPreview after the delete = %v, want ErrNotFound", getErr)
		}
		var listErr error
		listed, listErr = tree.ListPreviews()
		return lifecycle.Summary{Subject: "delete"}, listErr
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// A preview keeps its directory while it holds another file.
	if want := []string{"kept", "other"}; !slices.Equal(listed, want) {
		t.Errorf("ListPreviews after the deletes = %v, want %v", listed, want)
	}
	head := f.remote.Commits()[0]
	if want := []string{"feature-x: delete", "other: delete", "single: delete"}; !slices.Equal(head.Body(), want) {
		t.Errorf("body = %q, want %q", head.Body(), want)
	}
	got, err := c.ListPreviews(context.Background())
	if err != nil || !slices.Equal(got, listed) {
		t.Errorf("store ListPreviews = %v, %v; want %v", got, err, listed)
	}
}
