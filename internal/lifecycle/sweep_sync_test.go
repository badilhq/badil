package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

const (
	syncCommitURL     = "https://github.com/acme/gitops/commit/5ync"
	syncCommitMessage = "chore(preview): sync feature-x with badil.yaml"
)

// syncedResult is a sync of feature-x that rewrote it to vals, with notify.
func syncedResult(vals *preview.PreviewValues, notify bool) *gitops.SyncResult {
	return &gitops.SyncResult{
		Values:        *vals,
		CommitURL:     syncCommitURL,
		CommitMessage: syncCommitMessage,
		Changed:       true,
		Notify:        notify,
	}
}

func TestSweep_SyncsAfterRule3(t *testing.T) {
	captureLogs(t)
	f := &fakeAPI{
		label:    "preview",
		previews: []string{"unpinned", "expired", "feature-x"},
		files: map[string]*preview.PreviewValues{
			"unpinned": values(preview.ServiceEntry{Name: "backend-1"}),
			"expired":  values(pin("backend-1", pr42, stale)),
			"feature-x": values(
				pin("backend-1", pr42, recent),
				pin("backend-2", pr7, recent),
			),
		},
		prOpen:    map[string]bool{"acme/backend-2#7": true},
		prHeadRef: map[string]string{"acme/backend-2#7": "feature-x"},
		prLabels:  map[string][]string{"acme/backend-2#7": {"preview"}},
	}
	if err := lifecycle.Sweep(context.Background(), f, now, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	// Rules 1 and 2 deleted their previews: only feature-x is synced, after
	// rule 3 tore down the closed backend-1#42.
	if want := []syncCall{{slug: "feature-x", teardownsBefore: 1}}; !slices.Equal(f.syncs, want) {
		t.Errorf("syncs = %+v, want %+v", f.syncs, want)
	}
}

func TestSweep_NoSyncWithoutCatalog(t *testing.T) {
	captureLogs(t)
	f := &fakeAPI{
		catalogErr: errors.New("catalog boom"),
		previews:   []string{"feature-x"},
		files:      map[string]*preview.PreviewValues{"feature-x": values(pin("backend-1", pr42, recent))},
		prOpen:     map[string]bool{"acme/backend-1#42": true},
	}
	if err := lifecycle.Sweep(context.Background(), f, now, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(f.syncs) != 0 {
		t.Errorf("syncs = %+v, want none", f.syncs)
	}
}

func TestSweep_SyncErrorCounted(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		label:    "preview",
		previews: []string{"a", "b"},
		files: map[string]*preview.PreviewValues{
			"a": values(pin("backend-1", pr42, recent)),
			"b": values(pin("backend-2", pr7, recent)),
		},
		prOpen:    map[string]bool{"acme/backend-1#42": true, "acme/backend-2#7": true},
		prHeadRef: map[string]string{"acme/backend-1#42": "a", "acme/backend-2#7": "b"},
		syncErr:   map[string]error{"a": errors.New("sync boom")},
	}
	err := lifecycle.Sweep(context.Background(), f, now, ttl)
	if err == nil || err.Error() != "cleanup: 1 preview(s) failed" {
		t.Fatalf("err = %v, want cleanup: 1 preview(s) failed", err)
	}
	if want := []syncCall{{slug: "a"}, {slug: "b"}}; !slices.Equal(f.syncs, want) {
		t.Errorf("syncs = %+v, want %+v (the sweep goes on)", f.syncs, want)
	}
	failed := capture.find(slog.LevelError, "cleanup preview failed")
	if len(failed) != 1 || failed[0]["slug"].String() != "a" || failed[0]["err"].String() != "sync boom" {
		t.Errorf("cleanup preview failed logs = %v", failed)
	}
}

func TestSweep_SyncComments(t *testing.T) {
	synced := values(pin("backend-1", pr42, recent), preview.ServiceEntry{Name: "front"})
	cases := []struct {
		name        string
		sync        *gitops.SyncResult
		fanErr      error
		wantSynced  bool
		wantFanOuts int
	}{
		{name: "unchanged", sync: &gitops.SyncResult{}},
		{name: "changed without notify", sync: syncedResult(synced, false), wantSynced: true},
		{name: "changed with notify", sync: syncedResult(synced, true), wantSynced: true, wantFanOuts: 1},
		{
			name: "comment failure", sync: syncedResult(synced, true), fanErr: errors.New("comment boom"),
			wantSynced: true, wantFanOuts: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &fakeAPI{
				label:     "preview",
				previews:  []string{"feature-x"},
				files:     map[string]*preview.PreviewValues{"feature-x": values(pin("backend-1", pr42, recent))},
				prOpen:    map[string]bool{"acme/backend-1#42": true},
				prHeadRef: map[string]string{"acme/backend-1#42": "feature-x"},
				prLabels:  map[string][]string{"acme/backend-1#42": {"preview"}},
				sync:      map[string]*gitops.SyncResult{"feature-x": tc.sync},
				fanErr:    tc.fanErr,
			}
			if err := lifecycle.Sweep(context.Background(), f, now, ttl); err != nil {
				t.Fatalf("Sweep: %v (a comment failure is not a failure)", err)
			}
			assertSyncLogged(t, capture, tc.wantSynced, tc.fanErr != nil)
			if len(f.fanOuts) != tc.wantFanOuts {
				t.Fatalf("fanOuts = %d, want %d", len(f.fanOuts), tc.wantFanOuts)
			}
			if tc.wantFanOuts == 1 {
				want := preview.BuildSummary("feature-x", *synced, preview.URLConfig{}, nil,
					syncCommitMessage, syncCommitURL)
				if f.fanOuts[0].body != want || len(f.fanOuts[0].vals.Services) != 2 {
					t.Errorf("fanOut = %+v, want the summary of the written file", f.fanOuts[0])
				}
			}
		})
	}
}

// assertSyncLogged checks that a sync of feature-x was logged as preview
// synced exactly when synced, and a failed comment refresh exactly when
// commentFailed.
func assertSyncLogged(t *testing.T, capture *logCapture, synced, commentFailed bool) {
	t.Helper()
	logged := capture.find(slog.LevelInfo, "preview synced")
	if synced != (len(logged) == 1) || len(logged) > 1 {
		t.Fatalf("preview synced logs = %v, want synced %v", logged, synced)
	}
	if synced && (logged[0]["slug"].String() != "feature-x" ||
		logged[0]["gitops_commit_url"].String() != syncCommitURL) {
		t.Errorf("preview synced log = %v", logged[0])
	}
	warns := capture.find(slog.LevelWarn, "preview sync comment failed")
	if commentFailed != (len(warns) == 1) || len(warns) > 1 {
		t.Errorf("preview sync comment failed logs = %v", warns)
	}
}
