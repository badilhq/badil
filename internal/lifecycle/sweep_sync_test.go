package lifecycle_test

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store/storetest"
)

// TestSweep_SyncsKeptPreviews checks rules 1 and 2 delete their previews and
// rule 3 tears down the closed backend-1#42, projecting feature-x, which is
// then left alone by the sync, while kept is synced.
func TestSweep_SyncsKeptPreviews(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		previews: map[string]*preview.Values{
			"unpinned": values(preview.ServiceEntry{Name: "backend-1"}),
			"expired":  values(pin("backend-1", pr42, stale)),
		},
		raw: map[string]string{
			"previews/feature-x/values.yaml": marshal(t, *values(
				pin("backend-1", pr42, recent), pin("backend-2", pr7, recent),
			)),
			"previews/kept/values.yaml": marshal(t, *values(pin("backend-2", pr7, recent))),
		},
		prOpen:    map[string]bool{"acme/backend-2#7": true},
		prHeadRef: map[string]string{"acme/backend-2#7": "feature-x"},
		prLabels:  map[string][]string{"acme/backend-2#7": {"preview"}},
	}
	// kept's pin names no PR: rule 3 leaves it.
	f.raw["previews/kept/values.yaml"] = marshal(t, *values(pin("backend-2", "not-a-pr-url", recent)))
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	f.assertOneCommit(t, "chore(preview): sweep: delete 2, unpin 1, sync 1",
		"expired: delete", "feature-x: unpin backend-1", "kept: sync", "unpinned: delete")
	synced := capture.find(slog.LevelInfo, "preview synced")
	if len(synced) != 1 || synced[0]["slug"].String() != "kept" {
		t.Errorf("preview synced logs = %v, want kept only", synced)
	}
	if vals := f.g.file("feature-x"); vals.APIVersion != preview.APIVersion {
		t.Errorf("feature-x = %+v, want it projected by the teardown", vals)
	}
}

func TestSweep_NoSyncWithoutCatalog(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		noCatalog: true,
		raw: map[string]string{
			"previews/feature-x/values.yaml": marshal(t, *values(pin("backend-1", pr42, recent))),
		},
		prOpen: map[string]bool{"acme/backend-1#42": true},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := f.g.subjects(); len(got) != 0 || len(capture.find(slog.LevelInfo, "preview synced")) != 0 {
		t.Errorf("pushed = %q, want no sync", got)
	}
}

func TestSweep_UnreadablePreviewCounted(t *testing.T) {
	capture := captureLogs(t)
	f := &fakeAPI{
		raw: map[string]string{
			"previews/a/values.yaml": "services: [\n",
			"previews/b/values.yaml": marshal(t, *values(pin("backend-2", pr7, recent))),
		},
		prOpen:    map[string]bool{"acme/backend-2#7": true},
		prHeadRef: map[string]string{"acme/backend-2#7": "b"},
	}
	err := f.sweep(t, ttl)
	if err == nil || err.Error() != "cleanup: 1 preview(s) failed" {
		t.Fatalf("err = %v, want cleanup: 1 preview(s) failed", err)
	}
	f.assertOneCommit(t, "chore(preview): sweep: delete 0, unpin 0, sync 1", "b: sync")
	failed := capture.find(slog.LevelError, "cleanup preview failed")
	if len(failed) != 1 || failed[0]["slug"].String() != "a" {
		t.Errorf("cleanup preview failed logs = %v", failed)
	}
}

func TestSweep_SyncComments(t *testing.T) {
	cases := []struct {
		name        string
		file        func(t *testing.T) string
		fanErr      error
		wantSynced  bool
		wantFanOuts int
	}{
		{
			name: "unchanged",
			file: func(t *testing.T) string {
				return marshal(t, storetest.Project("feature-x", *values(pin("backend-1", pr42, recent)),
					sweepCatalog(t)))
			},
		},
		{
			name: "changed without notify",
			file: func(t *testing.T) string {
				vals := storetest.Project("feature-x", *values(pin("backend-1", pr42, recent)), sweepCatalog(t))
				vals.Services[1].Branch = "old"
				return marshal(t, vals)
			},
			wantSynced: true,
		},
		{
			name:       "changed with notify",
			file:       func(t *testing.T) string { return marshal(t, *values(pin("backend-1", pr42, recent))) },
			wantSynced: true, wantFanOuts: 1,
		},
		{
			name:       "comment failure",
			file:       func(t *testing.T) string { return marshal(t, *values(pin("backend-1", pr42, recent))) },
			fanErr:     errors.New("comment boom"),
			wantSynced: true, wantFanOuts: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &fakeAPI{
				raw:       map[string]string{"previews/feature-x/values.yaml": tc.file(t)},
				prOpen:    map[string]bool{"acme/backend-1#42": true},
				prHeadRef: map[string]string{"acme/backend-1#42": "feature-x"},
				prLabels:  map[string][]string{"acme/backend-1#42": {"preview"}},
				fanErr:    tc.fanErr,
			}
			if err := f.sweep(t, ttl); err != nil {
				t.Fatalf("Sweep: %v (a comment failure is not a failure)", err)
			}
			assertSyncLogged(t, f, capture, tc.wantSynced, tc.fanErr != nil)
			if len(f.fanOuts) != tc.wantFanOuts {
				t.Fatalf("fanOuts = %d, want %d", len(f.fanOuts), tc.wantFanOuts)
			}
			if tc.wantFanOuts == 1 {
				c := f.g.pushed()[0]
				written := *f.g.file("feature-x")
				want := preview.BuildSummary(
					storetest.GitHubForge.ParsePRURL,
					"feature-x",
					written,
					preview.URLConfig{},
					"",
					nil,
					c.Subject(),
					commitURL(c),
				)
				if f.fanOuts[0].body != want || !preview.Equal(f.fanOuts[0].vals, written) {
					t.Errorf("fanOut = %+v, want the summary of the written file", f.fanOuts[0])
				}
			}
		})
	}
}

// assertSyncLogged checks that a sync of feature-x was logged as preview
// synced, with f's commit, exactly when synced, and a failed comment refresh
// exactly when commentFailed.
func assertSyncLogged(t *testing.T, f *fakeAPI, capture *logCapture, synced, commentFailed bool) {
	t.Helper()
	logged := capture.find(slog.LevelInfo, "preview synced")
	if synced != (len(logged) == 1) || len(logged) > 1 {
		t.Fatalf("preview synced logs = %v, want synced %v", logged, synced)
	}
	if synced {
		c := f.assertOneCommit(t, "chore(preview): sweep: delete 0, unpin 0, sync 1", "feature-x: sync")
		if logged[0]["slug"].String() != "feature-x" || logged[0]["gitops_commit_url"].String() != commitURL(c) {
			t.Errorf("preview synced log = %v", logged[0])
		}
	}
	warns := capture.find(slog.LevelWarn, "preview sync comment failed")
	if commentFailed != (len(warns) == 1) || len(warns) > 1 {
		t.Errorf("preview sync comment failed logs = %v", warns)
	}
}

func TestSweep_SyncCommentLinksArgoCD(t *testing.T) {
	captureLogs(t)
	f := &fakeAPI{
		argocdURL: "https://argocd.example.com",
		raw: map[string]string{
			"previews/feature-x/values.yaml": marshal(t, *values(pin("backend-1", pr42, recent))),
		},
		prOpen:    map[string]bool{"acme/backend-1#42": true},
		prHeadRef: map[string]string{"acme/backend-1#42": "feature-x"},
		prLabels:  map[string][]string{"acme/backend-1#42": {"preview"}},
	}
	if err := f.sweep(t, ttl); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	link := preview.ArgoCDAppsURL("https://argocd.example.com", "feature-x")
	if len(f.fanOuts) != 1 || !strings.Contains(f.fanOuts[0].body, link) {
		t.Errorf("fanOuts = %+v, want one whose body links Argo CD", f.fanOuts)
	}
	if !slices.Equal(f.g.subjects(), []string{"chore(preview): sweep: delete 0, unpin 0, sync 1"}) {
		t.Errorf("pushed = %q", f.g.subjects())
	}
}
