package store_test

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// teardownFile is the values file of the preview feature-x.
const teardownFile = "previews/feature-x/values.yaml"

// teardownGitHub is the gitops repo of a write to the preview feature-x: its
// file is gets[0] (none when "") and its badil.yaml is badil (none when "").
// The writes are answered with writes, then 200; each 409 first commits the
// next file of gets (the last one repeats; "" removes the file), then runs
// onConflict when set.
type teardownGitHub struct {
	gets       []string
	writes     []int
	badil      string
	onConflict func(*remoteRepo)
	fake       *gitopsFake
}

// open returns a store over g.
func (g *teardownGitHub) open(t *testing.T) *store.GitHubStore {
	t.Helper()
	var extra map[string]string
	if g.badil != "" {
		extra = map[string]string{"badil.yaml": g.badil}
	}
	g.fake = newFileFake(t, teardownFile, g.gets, g.writes, extra)
	g.fake.onConflict = g.onConflict
	return g.fake.open(t)
}

// requests returns the writes received so far.
func (g *teardownGitHub) requests() []writeRequest {
	return g.fake.requests()
}

func runTeardown(t *testing.T, g *teardownGitHub) (*store.TeardownResult, error) {
	t.Helper()
	return g.open(t).TeardownPreviewWithRetry(context.Background(), "feature-x", "acme", "backend-1", 42)
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
		t.Fatalf("TeardownPreviewWithRetry: %v", err)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPut {
		t.Fatalf("requests = %+v, want one PUT", reqs)
	}
	const wantMsg = "chore(preview): remove backend-1 from feature-x"
	if reqs[0].message != wantMsg || reqs[0].sha != blobSHA(pr42AndPR7) {
		t.Errorf("PUT message/sha = %q/%q, want %q at the file's blob", reqs[0].message, reqs[0].sha, wantMsg)
	}
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
	if got := decode(t, reqs[0].content); !preview.Equal(got, decode(t, wantBody)) {
		t.Errorf("PUT content = %+v, want %s", got, wantBody)
	}
	if result.Deleted || !slices.Equal(result.Removed, []string{"backend-1"}) {
		t.Errorf("result = %+v, want Removed [backend-1], Deleted false", result)
	}
	if result.Catalog == nil || !slices.Equal(result.Catalog.Services, []string{"backend-1", "backend-2", "front"}) {
		t.Errorf("Catalog = %+v, want the badil.yaml read for the projection", result.Catalog)
	}
	if result.CommitMessage != wantMsg || !strings.HasSuffix(result.CommitURL, "/c0ffee") {
		t.Errorf("result message/url = %q/%q", result.CommitMessage, result.CommitURL)
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
			if reqs := g.requests(); len(reqs) != 0 {
				t.Errorf("requests = %+v, want no write", reqs)
			}
		})
	}
}

// TestTeardown_ReadsBadilYAMLOnceAcrossConflicts checks that the retries
// project on the badil.yaml read before the first update: a malformed one
// committed with each conflict is never read.
func TestTeardown_ReadsBadilYAMLOnceAcrossConflicts(t *testing.T) {
	g := &teardownGitHub{
		gets:   []string{pr42AndPR7, pr42AndPR7},
		writes: []int{http.StatusConflict, http.StatusConflict},
		badil:  teardownBadil,
		onConflict: func(r *remoteRepo) {
			r.set("badil.yaml", "services:\n  backend-1: {repo: r, imageTag: latest}\n")
		},
	}
	if _, err := runTeardown(t, g); err != nil {
		t.Fatalf("TeardownPreviewWithRetry: %v", err)
	}
	if reqs := g.requests(); len(reqs) != store.MaxWriteRetries || reqs[2].sha != blobSHA(pr42AndPR7) {
		t.Errorf("requests = %+v, want %d PUTs, the last at the re-read file's blob", reqs, store.MaxWriteRetries)
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
	result, err := g.open(t).TeardownPreviewWithRetry(context.Background(), "feature-x", "acme", "payments", 42)
	if err != nil {
		t.Fatalf("TeardownPreviewWithRetry: %v", err)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPut {
		t.Fatalf("requests = %+v, want one PUT", reqs)
	}
	const wantMsg = "chore(preview): remove payments from feature-x"
	if reqs[0].message != wantMsg {
		t.Errorf("PUT message = %q, want %q", reqs[0].message, wantMsg)
	}
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
	if got := decode(t, reqs[0].content); !preview.Equal(got, decode(t, wantBody)) {
		t.Errorf("PUT content = %+v, want %s", got, wantBody)
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
		t.Fatalf("TeardownPreviewWithRetry: %v", err)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodDelete {
		t.Fatalf("requests = %+v, want one DELETE", reqs)
	}
	const wantMsg = "chore(preview): delete feature-x preview"
	if reqs[0].message != wantMsg || reqs[0].sha != blobSHA(onlyPR42) {
		t.Errorf("DELETE message/sha = %q/%q, want %q at the file's blob", reqs[0].message, reqs[0].sha, wantMsg)
	}
	if !result.Deleted || !slices.Equal(result.Removed, []string{"backend-1"}) {
		t.Errorf("result = %+v, want Removed [backend-1], Deleted true", result)
	}
	if result.CommitMessage != wantMsg || !strings.HasSuffix(result.CommitURL, "/c0ffee") {
		t.Errorf("result message/url = %q/%q", result.CommitMessage, result.CommitURL)
	}
	if result.Values.Services != nil {
		t.Errorf("result.Values = %+v, want empty on delete", result.Values)
	}
}

func TestTeardown_NoOps(t *testing.T) {
	cases := []struct {
		name   string
		gets   []string
		writes []int
		want   int // expected number of writes
	}{
		{name: "file missing", gets: []string{""}},
		{
			name: "no match means no write",
			gets: []string{strings.Replace(pr42AndPR7, "pull/42", "pull/43", 1)},
		},
		{
			name:   "404 on re-fetch",
			gets:   []string{pr42AndPR7, ""},
			writes: []int{http.StatusConflict},
			want:   1,
		},
		{
			name:   "404 on DELETE",
			gets:   []string{onlyPR42},
			writes: []int{http.StatusNotFound},
			want:   1,
		},
		{
			name: "409 then re-fetch matches nothing",
			gets: []string{
				onlyPR42,
				"services:\n  - name: backend-1\n  - name: front\n",
			},
			writes: []int{http.StatusConflict},
			want:   1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &teardownGitHub{gets: tc.gets, writes: tc.writes, badil: teardownBadil}
			result, err := runTeardown(t, g)
			if err != nil {
				t.Fatalf("TeardownPreviewWithRetry: %v", err)
			}
			if result == nil || len(result.Removed) != 0 || result.Deleted || result.CommitURL != "" {
				t.Errorf("result = %+v, want empty no-op result", result)
			}
			if got := len(g.requests()); got != tc.want {
				t.Errorf("writes = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTeardown_DeleteConflictBecomesUpdate(t *testing.T) {
	// The first DELETE races a deploy from PR #7: the re-fetch shows its new
	// pin, so the retry must update the file instead of deleting it.
	g := &teardownGitHub{
		gets:   []string{onlyPR42, pr42AndPR7},
		writes: []int{http.StatusConflict},
		badil:  teardownBadil,
	}
	result, err := runTeardown(t, g)
	if err != nil {
		t.Fatalf("TeardownPreviewWithRetry: %v", err)
	}
	reqs := g.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %+v, want DELETE then PUT", reqs)
	}
	if reqs[0].method != http.MethodDelete || reqs[0].sha != blobSHA(onlyPR42) {
		t.Errorf("first write = %s at %s, want DELETE at the first file's blob", reqs[0].method, reqs[0].sha)
	}
	if reqs[1].method != http.MethodPut || reqs[1].sha != blobSHA(pr42AndPR7) {
		t.Errorf("retry = %s at %s, want PUT at the re-read file's blob", reqs[1].method, reqs[1].sha)
	}
	if !strings.Contains(reqs[1].content, "commitSha: bbbbbbbbbb") {
		t.Errorf("retry dropped PR #7's pin:\n%s", reqs[1].content)
	}
	if result.Deleted || result.CommitMessage != "chore(preview): remove backend-1 from feature-x" {
		t.Errorf("result = %+v, want an update", result)
	}
}

func TestTeardown_ConflictsExhausted(t *testing.T) {
	g := &teardownGitHub{
		gets:   []string{onlyPR42},
		writes: slices.Repeat([]int{http.StatusConflict}, store.MaxWriteRetries),
	}
	_, err := runTeardown(t, g)
	const want = "teardown previews/feature-x/values.yaml: 3 attempts exhausted"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if got := len(g.requests()); got != store.MaxWriteRetries {
		t.Errorf("writes = %d, want %d", got, store.MaxWriteRetries)
	}
}

func runUnpin(t *testing.T, g *teardownGitHub, services ...string) (*store.TeardownResult, error) {
	t.Helper()
	return g.open(t).UnpinServicesWithRetry(
		context.Background(), "feature-x", "acme", "backend-1", 42, services,
	)
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
		t.Fatalf("UnpinServicesWithRetry: %v", err)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPut || reqs[0].sha != blobSHA(pr42TwoServices) {
		t.Fatalf("requests = %+v, want one PUT at the file's blob", reqs)
	}
	const wantMsg = "chore(preview): remove backend-2 from feature-x"
	if reqs[0].message != wantMsg {
		t.Errorf("PUT message = %q, want %q", reqs[0].message, wantMsg)
	}
	got := decode(t, reqs[0].content)
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
	if result.CommitMessage != wantMsg || !strings.HasSuffix(result.CommitURL, "/c0ffee") {
		t.Errorf("result message/url = %q/%q", result.CommitMessage, result.CommitURL)
	}
}

func TestUnpinServices_KeepsFileWithNoPinLeft(t *testing.T) {
	g := &teardownGitHub{gets: []string{onlyPR42}, badil: teardownBadil}
	result, err := runUnpin(t, g, "backend-1")
	if err != nil {
		t.Fatalf("UnpinServicesWithRetry: %v", err)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPut {
		t.Fatalf("requests = %+v, want one PUT, never a DELETE", reqs)
	}
	for _, svc := range decode(t, reqs[0].content).Services {
		if svc.CommitSha != "" {
			t.Errorf("%s still pinned at %s", svc.Name, svc.CommitSha)
		}
	}
	if result.Deleted || !slices.Equal(result.Removed, []string{"backend-1"}) || result.Catalog == nil {
		t.Errorf("result = %+v, want Removed [backend-1], Deleted false, Catalog set", result)
	}
	if result.CommitMessage != "chore(preview): remove backend-1 from feature-x" {
		t.Errorf("CommitMessage = %q", result.CommitMessage)
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
	if reqs := g.requests(); len(reqs) != 0 {
		t.Errorf("requests = %+v, want no write", reqs)
	}
}
