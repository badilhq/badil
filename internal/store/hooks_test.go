package store_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/store"
)

// appliedRecorder records the results a store's WithApplied callback gets.
type appliedRecorder struct {
	mu      sync.Mutex
	results []lifecycle.Result
}

func (r *appliedRecorder) record(res lifecycle.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res)
}

func (r *appliedRecorder) got() []lifecycle.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.results)
}

// TestApply_AppliedReportsDeletedPreviews checks that a pushed commit
// deleting two previews' values.yaml reports both, sorted, in
// lifecycle.Result.Deleted and through WithApplied; a deleted file other than
// values.yaml and a delete of a missing preview are not reported.
func TestApply_AppliedReportsDeletedPreviews(t *testing.T) {
	rec := &appliedRecorder{}
	f := newGitopsFake(t, applyFiles)
	c := f.open(t, store.WithApplied(rec.record))

	out, err := apply(c, lifecycle.OpSweep, func(tree lifecycle.Tree) (lifecycle.Summary, error) {
		tree.DeletePreview("zz-last")
		tree.DeletePreview("a")
		tree.DeletePreview("missing")
		return lifecycle.Summary{Subject: "chore(preview): sweep"}, nil
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{"a", "zz-last"}
	if !slices.Equal(out.Deleted, want) {
		t.Errorf("Deleted = %v, want %v", out.Deleted, want)
	}
	got := rec.got()
	if len(got) != 1 || got[0].Commit != out.Commit || !slices.Equal(got[0].Deleted, want) {
		t.Errorf("WithApplied got %+v, want one call with %+v", got, out)
	}
}

// TestApply_AppliedNotCalledWithoutPush checks that neither a no-op nor a
// failed Apply calls WithApplied, and that a pushed commit deleting no
// preview reports none.
func TestApply_AppliedNotCalledWithoutPush(t *testing.T) {
	rec := &appliedRecorder{}
	f := newGitopsFake(t, applyFiles)
	c := f.open(t, store.WithApplied(rec.record))

	if _, err := apply(c, lifecycle.OpSync, func(lifecycle.Tree) (lifecycle.Summary, error) {
		return lifecycle.Summary{Subject: "nothing"}, nil
	}); err != nil {
		t.Fatalf("no-op Apply: %v", err)
	}
	if _, err := apply(c, lifecycle.OpSync, func(tree lifecycle.Tree) (lifecycle.Summary, error) {
		tree.DeletePreview("a")
		return lifecycle.Summary{}, errors.New("change failed")
	}); err == nil {
		t.Fatal("failed Apply: want an error")
	}
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("WithApplied called %d times after a no-op and a failed Apply, want 0", len(got))
	}

	out, err := apply(c, lifecycle.OpSync, rename("a", "renamed", "rename a"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(out.Deleted) != 0 {
		t.Errorf("Deleted = %v, want none", out.Deleted)
	}
	if got := rec.got(); len(got) != 1 || len(got[0].Deleted) != 0 {
		t.Errorf("WithApplied got %+v, want one call with no deleted preview", got)
	}
}

// TestFetch_HeadMoved checks that WithHeadMoved is called after a fetch
// moves the remote-tracking ref, from a read or from Apply, and not for a
// read or a fetch that finds the head unchanged, nor for the clone.
func TestFetch_HeadMoved(t *testing.T) {
	var moved atomic.Int32
	f := newGitopsFake(t, applyFiles)
	c := f.open(t, store.WithHeadMoved(func() { moved.Add(1) }))
	clock := &fakeClock{t: time.Now()}
	store.SetNow(c, clock.now)
	ctx := context.Background()

	if _, err := c.ListPreviews(ctx); err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	if n := moved.Load(); n != 0 {
		t.Fatalf("after the clone and a read of an unchanged head: %d calls, want 0", n)
	}

	f.remote.Set("previews/c/values.yaml", previewFile("c"))
	clock.advance(checkTTL)
	if _, err := c.ListPreviews(ctx); err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	if n := moved.Load(); n != 1 {
		t.Fatalf("after a read past a moved head: %d calls, want 1", n)
	}

	// Apply fetches on its own: the head it pushes is not a fetch.
	f.remote.Set("previews/d/values.yaml", previewFile("d"))
	if _, err := apply(c, lifecycle.OpSync, rename("a", "renamed", "rename a")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := moved.Load(); n != 2 {
		t.Fatalf("after an Apply fetching a moved head: %d calls, want 2", n)
	}
	if _, err := apply(c, lifecycle.OpSync, rename("a", "again", "rename a again")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := moved.Load(); n != 2 {
		t.Fatalf("after an Apply fetching an unchanged head: %d calls, want 2", n)
	}
}

// TestListPreviewFiles checks that only the previews holding a values.yaml
// are listed, sorted.
func TestListPreviewFiles(t *testing.T) {
	c, _ := readStore(t, map[string]string{
		"badil.yaml":              "services: {}\n",
		"previews/b/values.yaml":  previewFile("b"),
		"previews/a/values.yaml":  previewFile("a"),
		"previews/no-values/x.md": "x\n",
	})
	got, err := c.ListPreviewFiles(context.Background())
	if err != nil {
		t.Fatalf("ListPreviewFiles: %v", err)
	}
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("ListPreviewFiles = %v, want %v", got, want)
	}
}
