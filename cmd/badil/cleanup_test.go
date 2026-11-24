package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/store/storetest"
)

// cleanupForge is the forge of badil cleanup's catch-up: its open PRs are
// those of loopFake, GetPullRequest returns pr and UpsertComment records the
// bodies it posts.
type cleanupForge struct {
	*loopFake

	pr      forge.PullRequest
	upserts []string
}

func (f *cleanupForge) GetPullRequest(context.Context, string, string, int) (*forge.PullRequest, error) {
	pr := f.pr
	return &pr, nil
}

func (f *cleanupForge) UpsertComment(_ context.Context, _, _ string, _ int, body string) error {
	f.upserts = append(f.upserts, body)
	return nil
}

// foreignNamespaces is a lifecycle.NamespaceChecker where every namespace is
// foreign. checked logs the names checked.
type foreignNamespaces struct {
	checked []string
}

func (n *foreignNamespaces) Foreign(_ context.Context, name string) (bool, error) {
	n.checked = append(n.checked, name)
	return true, nil
}

// TestCleanupCatchUp_ChecksNamespaces runs badil cleanup's catch-up on a
// labelled PR whose new preview's namespace is foreign: it is refused, with
// the refusal comment and no gitops commit.
func TestCleanupCatchUp_ChecksNamespaces(t *testing.T) {
	logs := captureText(t)
	remote, st := storetest.Gitops(t, "services:\n  front: {repo: https://github.com/acme/front.git}\n", nil, nil, nil)
	commits := len(remote.Commits())
	pr := forge.PullRequest{
		Open: true, HeadRef: "pricing", HeadSHA: "abc", Author: "octocat",
		URL: "https://github.com/acme/front/pull/7", Labels: []string{"preview"},
		FullName: "acme/front", HeadFullName: "acme/front", Number: 7,
	}
	f := &cleanupForge{loopFake: &loopFake{openPRs: []forge.PullRequest{pr}}, pr: pr}
	ns := &foreignNamespaces{}

	cleanupCatchUp(st, f, ns).Run(context.Background())

	if !slices.Equal(ns.checked, []string{"pricing"}) {
		t.Errorf("checked = %v, want [pricing]", ns.checked)
	}
	if len(f.upserts) != 1 || !strings.Contains(f.upserts[0], "A namespace `pricing` already exists") {
		t.Errorf("upserts = %q, want the namespace in use comment", f.upserts)
	}
	if got := len(remote.Commits()); got != commits {
		t.Errorf("gitops commits = %d, want %d", got, commits)
	}
	if !strings.Contains(logs.String(), `msg="preview reconcile skipped"`) ||
		!strings.Contains(logs.String(), `reason="namespace in use"`) {
		t.Errorf("logs = %s, want the namespace in use skip", logs)
	}
}
