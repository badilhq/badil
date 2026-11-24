package lifecycle_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/forge/github/githubtest"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

// teardownFile is the values file of the preview feature-x.
const teardownFile = "previews/feature-x/values.yaml"

// teardownGitHub is the gitops repo of a teardown in the preview feature-x:
// its file is gets[0] (none when "") and its badil.yaml is badil (none when
// ""). The first conflicts pushes lose a race to a commit of the next file
// of gets (the last one repeats; "" removes the file), after which
// onConflict runs when set. out is the result of the last Apply.
type teardownGitHub struct {
	gets       []string
	conflicts  int
	badil      string
	onConflict func(*storetest.Remote)
	fake       *gitopsFake
	out        lifecycle.Result
}

// open returns a store over g.
func (g *teardownGitHub) open(t *testing.T) *store.GitHubStore {
	t.Helper()
	var extra map[string]string
	if g.badil != "" {
		extra = map[string]string{"badil.yaml": g.badil}
	}
	g.fake = newFileFake(t, teardownFile, g.gets, g.conflicts, extra)
	g.fake.onConflict = g.onConflict
	return g.fake.open(t)
}

// pushes returns the pushes so far.
func (g *teardownGitHub) pushes() int {
	return g.fake.pushCount()
}

// commit returns the head commit of the remote.
func (g *teardownGitHub) commit(t *testing.T) storetest.Commit {
	t.Helper()
	return g.fake.remote.Commits()[0]
}

// run applies unpin on the preview feature-x as one teardown operation.
func (g *teardownGitHub) run(
	t *testing.T, unpin func(lifecycle.Tree) (*lifecycle.TeardownResult, error),
) (*lifecycle.TeardownResult, error) {
	t.Helper()
	var res *lifecycle.TeardownResult
	var err error
	g.out, err = apply(g.open(t), lifecycle.OpTeardown, func(tree lifecycle.Tree) (lifecycle.Summary, error) {
		var unpinErr error
		res, unpinErr = unpin(tree)
		return lifecycle.Summary{Subject: "teardown"}, unpinErr
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func runTeardown(t *testing.T, g *teardownGitHub) (*lifecycle.TeardownResult, error) {
	t.Helper()
	return g.run(t, func(tree lifecycle.Tree) (*lifecycle.TeardownResult, error) {
		return lifecycle.TeardownPreview(tree, githubtest.Forge.ParsePRURL, "feature-x", "acme", "backend-1", 42)
	})
}

// assertPushed checks g pushed one commit whose body is lines and whose file
// feature-x is content ("" when deleted), and returns that file.
func assertPushed(t *testing.T, g *teardownGitHub, lines ...string) string {
	t.Helper()
	c := g.commit(t)
	if g.pushes() != 1 || c.Hash != g.out.Commit || !slices.Equal(c.Body(), lines) {
		t.Fatalf("pushes = %d, head %s body %q; want one push of %s with body %q",
			g.pushes(), c.Hash, c.Body(), g.out.Commit, lines)
	}
	return c.Tree[teardownFile]
}

const pr7URL = "https://github.com/acme/backend-2/pull/7"

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

// teardownBadil is the badil.yaml of the previews of these tests: image tags
// formatted sha-{sha7}, backend-1 on branch release and exposed.
const teardownBadil = `imageTag: "sha-{sha7}"
services:
  backend-1:
    repo: https://github.com/acme/backend-1.git
    branch: release
  backend-2:
    repo: https://github.com/acme/backend-2.git
  front:
    repo: https://github.com/acme/front.git
preview:
  domain: 127.0.0.1.nip.io
  expose:
    backend-1: {}
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

func TestTeardown_UpdateWhenPinsRemain(t *testing.T) {
	g := &teardownGitHub{gets: []string{pr42AndPR7}, badil: teardownBadil}
	result, err := runTeardown(t, g)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	written := assertPushed(t, g, "feature-x: unpin backend-1")
	// The unpinned backend-1 is projected on badil.yaml in the same commit:
	// its branch-tracking imageTag, chart and host.
	wantBody := "apiVersion: badil.dev/v1beta1\n" +
		"gitopsRepoURL: https://github.com/acme/gitops.git\n" +
		"services:\n" +
		"  - name: backend-1\n" +
		"    repoURL: https://github.com/acme/backend-1.git\n" +
		"    branch: release\n" +
		"    imageTag: sha-${ARGOCD_APP_REVISION_SHORT}\n" +
		defaultChartYAML +
		"    host: backend-1-feature-x.127.0.0.1.nip.io\n" +
		"  - name: backend-2\n" +
		"    repoURL: https://github.com/acme/backend-2.git\n" +
		"    branch: main\n" +
		"    commitSha: bbbbbbbbbb\n" +
		"    imageTag: sha-bbbbbbb\n" +
		defaultChartYAML +
		"    metadata:\n" +
		"      pr-author: \"\"\n" +
		"      pr-url: " + pr7URL + "\n" +
		"      pr-number: \"\"\n" +
		"      created-at: \"\"\n" +
		"      updated-at: \"\"\n" +
		"      vcs.ref.name: \"\"\n" +
		"      cicd.pipeline.run.url: \"\"\n" +
		"  - name: front\n" +
		"    repoURL: https://github.com/acme/front.git\n" +
		"    branch: main\n" +
		"    imageTag: sha-${ARGOCD_APP_REVISION_SHORT}\n" +
		defaultChartYAML +
		"dependencies: []\n"
	if got := decode(t, written); !preview.Equal(got, decode(t, wantBody)) {
		t.Errorf("written = %+v, want %s", got, wantBody)
	}
	if result.Deleted || !slices.Equal(result.Removed, []string{"backend-1"}) {
		t.Errorf("result = %+v, want Removed [backend-1], Deleted false", result)
	}
	if result.Catalog == nil || !slices.Equal(result.Catalog.Services, []string{"backend-1", "backend-2", "front"}) {
		t.Errorf("Catalog = %+v, want the badil.yaml read for the projection", result.Catalog)
	}
	if len(result.Values.Services) != 3 || result.Values.Services[1].CommitSha != "bbbbbbbbbb" ||
		result.Values.Services[0].ImageTag != "sha-${ARGOCD_APP_REVISION_SHORT}" {
		t.Errorf("result.Values = %+v, want the written file", result.Values)
	}
}

func TestTeardown_UnreadableBadilYAML(t *testing.T) {
	cases := []struct {
		name  string
		badil string
		want  string
	}{
		{name: "missing", want: "teardown previews/feature-x/values.yaml: gitops: not found"},
		{
			name:  "malformed",
			badil: "services:\n  backend-1: {repo: r, imageTag: latest}\n",
			want:  "teardown previews/feature-x/values.yaml: badil.yaml: services.backend-1.imageTag must be",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &teardownGitHub{gets: []string{pr42AndPR7}, badil: tc.badil}
			result, err := runTeardown(t, g)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("err = %v, want prefix %q", err, tc.want)
			}
			if result != nil {
				t.Errorf("result = %+v, want nil", result)
			}
			if n := g.pushes(); n != 0 {
				t.Errorf("pushes = %d, want none", n)
			}
		})
	}
}

// TestTeardown_ProjectsOnRetriedBadilYAML checks that a retry projects on
// the badil.yaml of the tree it runs on: one made malformed by the commit
// the push lost to fails the teardown, with nothing more pushed.
func TestTeardown_ProjectsOnRetriedBadilYAML(t *testing.T) {
	g := &teardownGitHub{
		gets:      []string{pr42AndPR7, pr42AndPR7},
		conflicts: 1,
		badil:     teardownBadil,
		onConflict: func(r *storetest.Remote) {
			r.Set("badil.yaml", "services:\n  backend-1: {repo: r, imageTag: latest}\n")
		},
	}
	_, err := runTeardown(t, g)
	const want = "teardown previews/feature-x/values.yaml: badil.yaml: services.backend-1.imageTag must be"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("err = %v, want prefix %q", err, want)
	}
	if n := g.pushes(); n != 1 {
		t.Errorf("pushes = %d, want 1", n)
	}
}

// frontAndPayments is a preview of needsBadil pinned by acme/front#1 and
// acme/payments#42: front, payments and what they need.
const frontAndPayments = `apiVersion: badil.dev/v1beta1
gitopsRepoURL: https://github.com/acme/gitops.git
services:
  - name: front
    repoURL: https://github.com/acme/front.git
    branch: main
    commitSha: aaaaaaaaaa
    imageTag: aaaaaaaaaa
    chart: {path: k8s/, imageTagParam: app.image.tag, hostParam: app.ingress.host}
    metadata:
      pr-url: https://github.com/acme/front/pull/1
  - name: backend
    repoURL: https://github.com/acme/backend.git
    branch: main
    imageTag: ${ARGOCD_APP_REVISION}
    chart: {path: k8s/, imageTagParam: app.image.tag, hostParam: app.ingress.host}
  - name: payments
    repoURL: https://github.com/acme/payments.git
    branch: main
    commitSha: bbbbbbbbbb
    imageTag: bbbbbbbbbb
    chart: {path: k8s/, imageTagParam: app.image.tag, hostParam: app.ingress.host}
    metadata:
      pr-url: https://github.com/acme/payments/pull/42
dependencies:
  - {name: postgres, chart: {repoURL: oci://r/postgresql, version: 1.0.0}}
  - {name: redis, chart: {repoURL: oci://r/redis, version: 1.0.0}}
  - {name: kafka, chart: {repoURL: oci://r/kafka, version: 1.0.0}}
`

// TestTeardown_DropsPartsOnlyThePRNeeded checks that closing a PR removes,
// in the teardown commit, the services and dependencies only its pinned
// services needed.
func TestTeardown_DropsPartsOnlyThePRNeeded(t *testing.T) {
	g := &teardownGitHub{gets: []string{frontAndPayments}, badil: needsBadil}
	result, err := g.run(t, func(tree lifecycle.Tree) (*lifecycle.TeardownResult, error) {
		return lifecycle.TeardownPreview(tree, githubtest.Forge.ParsePRURL, "feature-x", "acme", "payments", 42)
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	written := assertPushed(t, g, "feature-x: unpin payments")
	wantBody := "apiVersion: badil.dev/v1beta1\n" +
		"gitopsRepoURL: https://github.com/acme/gitops.git\n" +
		"services:\n" +
		"  - name: front\n" +
		"    repoURL: https://github.com/acme/front.git\n" +
		"    branch: main\n" +
		"    commitSha: aaaaaaaaaa\n" +
		"    imageTag: aaaaaaaaaa\n" +
		defaultChartYAML +
		"    metadata:\n" +
		"      pr-author: \"\"\n" +
		"      pr-url: https://github.com/acme/front/pull/1\n" +
		"      pr-number: \"\"\n" +
		"      created-at: \"\"\n" +
		"      updated-at: \"\"\n" +
		"      vcs.ref.name: \"\"\n" +
		"      cicd.pipeline.run.url: \"\"\n" +
		"  - name: backend\n" +
		"    repoURL: https://github.com/acme/backend.git\n" +
		"    branch: main\n" +
		"    imageTag: ${ARGOCD_APP_REVISION}\n" +
		defaultChartYAML +
		"dependencies:\n" +
		"  - name: postgres\n" +
		"    chart:\n" +
		"      repoURL: oci://r/postgresql\n" +
		"      version: 1.0.0\n" +
		"  - name: redis\n" +
		"    chart:\n" +
		"      repoURL: oci://r/redis\n" +
		"      version: 1.0.0\n"
	if got := decode(t, written); !preview.Equal(got, decode(t, wantBody)) {
		t.Errorf("written = %+v, want %s", got, wantBody)
	}
	if result.Deleted || !slices.Equal(result.Removed, []string{"payments"}) {
		t.Errorf("result = %+v, want Removed [payments], Deleted false", result)
	}
	if want := []string{"payments", "search", "kafka", "elasticsearch"}; !slices.Equal(result.Excluded, want) {
		t.Errorf("Excluded = %v, want %v", result.Excluded, want)
	}
}

func TestTeardown_DeleteWhenNoPinRemains(t *testing.T) {
	g := &teardownGitHub{gets: []string{onlyPR42}}
	result, err := runTeardown(t, g)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if written := assertPushed(t, g, "feature-x: delete"); written != "" {
		t.Errorf("feature-x = %q, want it deleted", written)
	}
	if _, ok := g.commit(t).Tree["previews/feature-x/values.yaml"]; ok {
		t.Error("previews/feature-x/values.yaml still there")
	}
	if !result.Deleted || !slices.Equal(result.Removed, []string{"backend-1"}) {
		t.Errorf("result = %+v, want Removed [backend-1], Deleted true", result)
	}
	if result.Values.Services != nil {
		t.Errorf("result.Values = %+v, want empty on delete", result.Values)
	}
}

func TestTeardown_NoOps(t *testing.T) {
	cases := []struct {
		name      string
		gets      []string
		conflicts int
		want      int // expected number of pushes
	}{
		{name: "file missing", gets: []string{""}},
		{
			name: "no match means no write",
			gets: []string{strings.Replace(pr42AndPR7, "pull/42", "pull/43", 1)},
		},
		{
			name:      "file gone on the retry",
			gets:      []string{pr42AndPR7, ""},
			conflicts: 1,
			want:      1,
		},
		{
			name: "retry matches nothing",
			gets: []string{
				onlyPR42,
				"services:\n  - name: backend-1\n  - name: front\n",
			},
			conflicts: 1,
			want:      1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &teardownGitHub{gets: tc.gets, conflicts: tc.conflicts, badil: teardownBadil}
			result, err := runTeardown(t, g)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if result == nil || len(result.Removed) != 0 || result.Deleted || !g.out.NoOp {
				t.Errorf("result = %+v, %+v; want empty no-op result", result, g.out)
			}
			if got := g.pushes(); got != tc.want {
				t.Errorf("pushes = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTeardown_DeleteConflictBecomesUpdate(t *testing.T) {
	// The first push, a delete, races a deploy from PR #7: the retried tree
	// shows its new pin, so the retry must update the file instead of
	// deleting it.
	g := &teardownGitHub{
		gets:      []string{onlyPR42, pr42AndPR7},
		conflicts: 1,
		badil:     teardownBadil,
	}
	result, err := runTeardown(t, g)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	c := g.commit(t)
	if g.pushes() != 2 || !slices.Equal(c.Body(), []string{"feature-x: unpin backend-1"}) {
		t.Fatalf("pushes = %d, body %q; want a delete then an update", g.pushes(), c.Body())
	}
	if !strings.Contains(c.Tree[teardownFile], "commitSha: bbbbbbbbbb") {
		t.Errorf("retry dropped PR #7's pin:\n%s", c.Tree[teardownFile])
	}
	if result.Deleted {
		t.Errorf("result = %+v, want an update", result)
	}
}

func runUnpin(t *testing.T, g *teardownGitHub, services ...string) (*lifecycle.TeardownResult, error) {
	t.Helper()
	return g.run(t, func(tree lifecycle.Tree) (*lifecycle.TeardownResult, error) {
		return lifecycle.UnpinPreviewServices(
			tree,
			githubtest.Forge.ParsePRURL,
			"feature-x",
			"acme",
			"backend-1",
			42,
			services,
		)
	})
}

// pr42TwoServices is a preview where acme/backend-1#42 pins backend-1 and
// backend-2 (a monorepo PR) and acme/front#9 pins front.
const pr42TwoServices = `apiVersion: badil.dev/v1beta1
gitopsRepoURL: https://github.com/acme/gitops.git
services:
  - name: backend-1
    repoURL: https://github.com/acme/backend-1.git
    branch: release
    commitSha: aaaaaaaaaa
    metadata:
      pr-url: https://github.com/acme/backend-1/pull/42
  - name: backend-2
    repoURL: https://github.com/acme/backend-2.git
    branch: main
    commitSha: aaaaaaaaaa
    metadata:
      pr-url: https://github.com/acme/backend-1/pull/42
  - name: front
    repoURL: https://github.com/acme/front.git
    branch: main
    commitSha: cccccccccc
    metadata:
      pr-url: https://github.com/acme/front/pull/9
dependencies: []
`

func TestUnpinServices_OnlyTheGivenServices(t *testing.T) {
	g := &teardownGitHub{gets: []string{pr42TwoServices}, badil: teardownBadil}
	result, err := runUnpin(t, g, "backend-2", "front")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := decode(t, assertPushed(t, g, "feature-x: unpin backend-2"))
	pins := map[string]string{}
	for _, svc := range got.Services {
		pins[svc.Name] = svc.CommitSha
	}
	// backend-1 stays pinned by the PR; front is another PR's pin.
	want := map[string]string{"backend-1": "aaaaaaaaaa", "backend-2": "", "front": "cccccccccc"}
	if !maps.Equal(pins, want) {
		t.Errorf("pins = %v, want %v", pins, want)
	}
	if got.Services[1].ImageTag != "sha-${ARGOCD_APP_REVISION_SHORT}" {
		t.Errorf("backend-2 imageTag = %q, want the branch-tracking tag of the projection", got.Services[1].ImageTag)
	}
	if result.Deleted || !slices.Equal(result.Removed, []string{"backend-2"}) {
		t.Errorf("result = %+v, want Removed [backend-2], Deleted false", result)
	}
	if !preview.Equal(result.Values, got) {
		t.Errorf("result.Values = %+v, want the written file", result.Values)
	}
	if result.Catalog == nil || !slices.Equal(result.Catalog.Services, []string{"backend-1", "backend-2", "front"}) {
		t.Errorf("Catalog = %+v, want the badil.yaml read for the projection", result.Catalog)
	}
	if result.Excluded != nil {
		t.Errorf("Excluded = %v, want nil", result.Excluded)
	}
}

func TestUnpinServices_KeepsFileWithNoPinLeft(t *testing.T) {
	g := &teardownGitHub{gets: []string{onlyPR42}, badil: teardownBadil}
	result, err := runUnpin(t, g, "backend-1")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, svc := range decode(t, assertPushed(t, g, "feature-x: unpin backend-1")).Services {
		if svc.CommitSha != "" {
			t.Errorf("%s still pinned at %s", svc.Name, svc.CommitSha)
		}
	}
	if result.Deleted || !slices.Equal(result.Removed, []string{"backend-1"}) || result.Catalog == nil {
		t.Errorf("result = %+v, want Removed [backend-1], Deleted false, Catalog set", result)
	}
}

func TestUnpinServices_UnreadableBadilYAML(t *testing.T) {
	g := &teardownGitHub{gets: []string{pr42TwoServices}}
	result, err := runUnpin(t, g, "backend-2")
	const want = "unpin previews/feature-x/values.yaml: gitops: not found"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("err = %v, want prefix %q", err, want)
	}
	if result != nil {
		t.Errorf("result = %+v, want nil", result)
	}
	if n := g.pushes(); n != 0 {
		t.Errorf("pushes = %d, want none", n)
	}
}
