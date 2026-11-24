package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

const reconcileServices = `services:
  api:
    repo: https://github.com/acme/monorepo.git
  worker:
    repo: https://github.com/acme/monorepo
  front:
    repo: https://github.com/acme/front.git
`

const fetchedSHA = "fe7c4ed0000000000000000000000000000000ff"

// reconcileFake is a ReconcileAPI over a gitops repo of a real store. The
// repo holds badil, or reconcileServices when it is empty, with
// preview.maxPreviews set to maxPreviews when it is, the files of previews
// (projected on the catalog) and raw. It is made on the first api call, and
// kept by the next ones. FetchServiceCatalog fails with catalogErr when set.
// GetPullRequest returns pr, unless prErr is set; ListChangedFiles returns
// files, unless filesErr is set. ListOpenPullRequests returns, for
// "owner/repo", openPRs, unless listErrs holds an error for it; listed logs
// the repos listed, in order. Each push first runs beforePush when set,
// whose error fails the push. calls logs, in order, "files", "apply",
// "upsert" and "fanout". Metrics records the outcomes; nil records nothing.
type reconcileFake struct {
	storetest.GitHubURLs
	*metrics.Metrics

	badil       string
	catalogErr  error
	pr          *forge.PullRequest
	prErr       error
	fanErr      error
	upsertErr   error
	previews    map[string]*preview.Values
	raw         map[string]string
	maxPreviews int
	files       []forge.ChangedFile
	filesErr    error
	beforePush  func(*storetest.Remote) error
	openPRs     map[string][]forge.PullRequest
	listErrs    map[string]error

	g       *gitops
	listed  []string
	prReads int
	fanOuts []string
	upserts []string
	calls   []string
}

func (f *reconcileFake) FetchServiceCatalog(ctx context.Context) (*store.ServiceCatalog, error) {
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	return f.g.st.FetchServiceCatalog(ctx)
}

func (f *reconcileFake) GetPreview(ctx context.Context, slug string) (*preview.Values, string, error) {
	return f.g.st.GetPreview(ctx, slug)
}

func (f *reconcileFake) Apply(ctx context.Context, op store.Op) (store.Result, error) {
	f.calls = append(f.calls, "apply")
	return f.g.st.Apply(ctx, op)
}

// badilYAML returns badil, or reconcileServices when it is empty, with
// maxPreviews.
func (f *reconcileFake) badilYAML() string {
	badil := f.badil
	if badil == "" {
		badil = reconcileServices
	}
	if f.maxPreviews > 0 {
		badil += "preview:\n  maxPreviews: " + strconv.Itoa(f.maxPreviews) + "\n"
	}
	return badil
}

func (f *reconcileFake) ListChangedFiles(context.Context, string, string, int) ([]forge.ChangedFile, error) {
	f.calls = append(f.calls, "files")
	if f.filesErr != nil {
		return nil, f.filesErr
	}
	return f.files, nil
}

func (f *reconcileFake) ListOpenPullRequests(_ context.Context, owner, repo string) ([]forge.PullRequest, error) {
	key := owner + "/" + repo
	f.listed = append(f.listed, key)
	if err := f.listErrs[key]; err != nil {
		return nil, err
	}
	return f.openPRs[key], nil
}

func (f *reconcileFake) GetPullRequest(context.Context, string, string, int) (*forge.PullRequest, error) {
	f.prReads++
	if f.prErr != nil {
		return nil, f.prErr
	}
	return f.pr, nil
}

func (f *reconcileFake) UpsertComment(_ context.Context, _, _ string, _ int, body string) error {
	f.calls = append(f.calls, "upsert")
	f.upserts = append(f.upserts, body)
	return f.upsertErr
}

// fanOut records body as posted on the open PRs of a preview.
func (f *reconcileFake) fanOut(_ context.Context, _ forge.Forge, _ preview.Values, body string) error {
	f.calls = append(f.calls, "fanout")
	f.fanOuts = append(f.fanOuts, body)
	return f.fanErr
}

// open makes f's gitops repo on the first call.
func (f *reconcileFake) open(t *testing.T) {
	t.Helper()
	if f.g != nil {
		return
	}
	f.g = newGitops(t, f.badilYAML(), f.previews, f.raw)
	if f.beforePush != nil {
		f.g.remote.OnPush(func() error { return f.beforePush(f.g.remote) })
	}
}

// api is the lifecycle.ReconcileAPI over f: its store, forge and recorder,
// with its fanOut as the fan-out.
func (f *reconcileFake) api(t *testing.T) lifecycle.ReconcileAPI {
	t.Helper()
	f.open(t)
	return lifecycle.ReconcileAPI{Store: f, Forge: f, Recorder: f, FanOut: f.fanOut}
}

// teardownAPI is api narrowed to a lifecycle.TeardownAPI.
func (f *reconcileFake) teardownAPI(t *testing.T) lifecycle.TeardownAPI {
	t.Helper()
	f.open(t)
	return lifecycle.TeardownAPI{Store: f, Forge: f, Recorder: f, FanOut: f.fanOut}
}

// event builds a reconcile event for acme/<repo>#42 whose payload carries
// labels.
func event(action, repo, label string, labels ...string) lifecycle.Event {
	return lifecycle.Event{
		Target: lifecycle.Target{Owner: "acme", Repo: repo, FullName: "acme/" + repo, Number: 42},
		Action: action,
		Label:  label,
		Labels: labels,
	}
}

// fetchedPR is the PR acme/<repo>#42 as read from GitHub.
func fetchedPR(repo string, open bool, labels ...string) *forge.PullRequest {
	return &forge.PullRequest{
		Open:         open,
		HeadRef:      "feature/pricing",
		HeadSHA:      fetchedSHA,
		Author:       "octocat",
		URL:          "https://github.com/acme/" + repo + "/pull/42",
		Labels:       labels,
		FullName:     "acme/" + repo,
		HeadFullName: "acme/" + repo,
	}
}

// lastCommit returns the last commit f pushed, failing t when none was.
func (f *reconcileFake) lastCommit(t *testing.T) storetest.Commit {
	t.Helper()
	pushed := f.g.pushed()
	if len(pushed) == 0 {
		t.Fatal("no commit pushed")
	}
	return pushed[len(pushed)-1]
}

// assertOneCommit fails t unless f pushed exactly one commit, with subject
// and body, and returns it.
func (f *reconcileFake) assertOneCommit(t *testing.T, subject string, body ...string) storetest.Commit {
	t.Helper()
	return f.g.assertOneCommit(t, subject, body...)
}

// assertNothingPushed fails t when f pushed a commit.
func (f *reconcileFake) assertNothingPushed(t *testing.T) {
	t.Helper()
	if pushed := f.g.subjects(); len(pushed) != 0 {
		t.Errorf("pushed = %q, want nothing", pushed)
	}
}

// pinnedBy is a preview entry of service pinned by the PR at prURL, deployed
// recently.
func pinnedBy(service, prURL string) preview.ServiceEntry {
	return pin(service, prURL, recent)
}

const front42 = "https://github.com/acme/front/pull/42"

func TestReconcile_LabeledDeploysFetchedHead(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "bug", "Preview")}
	ev := event("labeled", "front", "preview", "preview")
	lifecycle.ReconcilePR(context.Background(), f.api(t), ev)

	c := f.assertOneCommit(t, "chore(preview): deploy acme/front#42 to feature-pricing",
		"feature-pricing: pin front@fe7c4ed")
	vals := f.g.file("feature-pricing")
	var names []string
	for _, svc := range vals.Services {
		names = append(names, svc.Name)
	}
	if want := []string{"api", "worker", "front"}; !slices.Equal(names, want) {
		t.Errorf("services = %v, want the catalog %v", names, want)
	}
	front := vals.Services[2]
	if m := front.Metadata; front.CommitSha != fetchedSHA || m == nil || m.VCSRefName != "feature/pricing" ||
		m.PRAuthor != "octocat" || m.PRURL != front42 || m.PRNumber != "42" || m.PipelineRunURL != "" ||
		m.UpdatedAt == "" {
		t.Errorf("front = %+v, metadata %+v", front, m)
	}
	if len(f.fanOuts) != 1 || !strings.HasSuffix(f.fanOuts[0], link(c)) {
		t.Errorf("fanOuts = %q, want one linking %s", f.fanOuts, link(c))
	}
	if td := teardownsLogged(capture); len(td) != 0 {
		t.Errorf("teardowns = %+v, want none", td)
	}
	logs := capture.find(slog.LevelInfo, "preview deploy")
	if len(logs) != 1 || logs[0]["slug"].String() != "feature-pricing" || logs[0]["service"].String() != "front" ||
		logs[0]["repo"].String() != "acme/front" || logs[0]["pr_number"].Int64() != 42 ||
		logs[0]["commit_sha"].String() != fetchedSHA || logs[0]["gitops_commit_url"].String() != commitURL(c) {
		t.Errorf("preview deploy logs = %v", logs)
	}
}

func TestReconcile_LabeledButFetchedUnlabeledTearsDown(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		pr:       fetchedPR("front", true, "bug"),
		previews: map[string]*preview.Values{"feature-pricing": values(pinnedBy("front", front42))},
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "preview", "preview"))

	c := f.assertOneCommit(t, "chore(preview): unpin acme/front#42 from feature-pricing", "feature-pricing: delete")
	want := []teardownCall{{slug: "feature-pricing", owner: "acme", repo: "front", number: 42}}
	if got := teardownsLogged(capture); !slices.Equal(got, want) {
		t.Errorf("teardowns = %+v, want %+v", got, want)
	}
	wantBody := "## 🧹 Preview: `feature-pricing`\n\n" +
		"Preview deleted (label `preview` removed): no other PR is pinned.\n\n" +
		"---\n" + link(c)
	if len(f.upserts) != 1 || f.upserts[0] != wantBody {
		t.Errorf("upserts = %q, want one with body:\n%s", f.upserts, wantBody)
	}
}

func TestReconcile_UnlabeledClosedTearsDownAsClosed(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{
		pr: fetchedPR("front", false),
		previews: map[string]*preview.Values{"feature-pricing": values(
			pinnedBy("api", "https://github.com/acme/monorepo/pull/7"), pinnedBy("front", front42),
		)},
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("unlabeled", "front", "preview"))

	c := f.assertOneCommit(t, "chore(preview): unpin acme/front#42 from feature-pricing",
		"feature-pricing: unpin front")
	wantBody := "## 🧹 Preview: `feature-pricing`\n\n" +
		"Removed `front` from this preview (PR closed).\n\n" +
		"---\n" + link(c)
	if len(f.upserts) != 1 || f.upserts[0] != wantBody {
		t.Errorf("upserts = %q, want one with body:\n%s", f.upserts, wantBody)
	}
	if pins := f.g.pins("feature-pricing"); pins["front"] != "" || pins["api"] == "" {
		t.Errorf("pins = %v, want front unpinned, api kept", pins)
	}
}

func TestReconcile_ClosedTearsDownOutsideCatalogAndForks(t *testing.T) {
	cases := []struct {
		name string
		repo string
		fork bool
	}{
		{name: "repo not in catalog", repo: "docs"},
		{name: "fork pr", repo: "front", fork: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{pr: fetchedPR(tc.repo, false)}
			if tc.fork {
				f.pr.HeadFullName = "octocat/" + tc.repo
			}
			lifecycle.ReconcilePR(context.Background(), f.api(t), event("closed", tc.repo, ""))

			want := []teardownCall{{slug: "feature-pricing", owner: "acme", repo: tc.repo, number: 42}}
			if got := teardownsLogged(capture); !slices.Equal(got, want) {
				t.Errorf("teardowns = %+v, want %+v", got, want)
			}
		})
	}
}

func TestReconcile_TwoServicesInCatalogOrder(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("monorepo", true, "preview")}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "monorepo", "preview", "preview"))

	f.assertOneCommit(t, "chore(preview): deploy acme/monorepo#42 to feature-pricing",
		"feature-pricing: pin api@fe7c4ed", "feature-pricing: pin worker@fe7c4ed")
	if got, want := deployedLogged(capture), []string{"feature-pricing/api", "feature-pricing/worker"}; !slices.Equal(
		got, want) {
		t.Errorf("deployed = %v, want %v", got, want)
	}
	if len(f.fanOuts) != 1 {
		t.Errorf("fanOuts = %d, want 1: one comment per preview", len(f.fanOuts))
	}
}

func TestReconcile_SynchronizeOnLabeledPRDeploys(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "preview")}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("synchronize", "front", "", "preview"))

	if got := deployedLogged(capture); !slices.Equal(got, []string{"feature-pricing/front"}) {
		t.Errorf("deployed = %v, want [front]", got)
	}
}

func TestReconcile_PayloadGuards(t *testing.T) {
	cases := []struct {
		name   string
		ev     lifecycle.Event
		reason string
	}{
		{"opened without the label", event("opened", "front", "", "bug"), "not labeled"},
		{"reopened without the label", event("reopened", "front", ""), "not labeled"},
		{"synchronize without the label", event("synchronize", "front", ""), "not labeled"},
		{"labeled with another label", event("labeled", "front", "bug", "bug", "preview"), "other label"},
		{"unlabeled another label", event("unlabeled", "front", "bug", "preview"), "other label"},
		{"repo not in catalog", event("labeled", "unknown", "preview", "preview"), "repo not in catalog"},
		{"labeled with an empty named label", event("labeled", "front", "preview:", "preview:"), "other label"},
		{"opened with an empty named label", event("opened", "front", "", "preview:--"), "not labeled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{pr: fetchedPR(tc.ev.Repo, true, "preview")}
			lifecycle.ReconcilePR(context.Background(), f.api(t), tc.ev)

			if f.prReads != 0 || len(f.calls) != 0 || len(teardownsLogged(capture)) != 0 {
				t.Errorf("prReads = %d, calls = %v; want nothing", f.prReads, f.calls)
			}
			f.assertNothingPushed(t)
			logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
			if len(logs) != 1 || logs[0]["reason"].String() != tc.reason ||
				logs[0]["repo"].String() != tc.ev.FullName || logs[0]["pr_number"].Int64() != 42 ||
				logs[0]["action"].String() != tc.ev.Action {
				t.Errorf("skip logs = %v, want reason %q", logs, tc.reason)
			}
		})
	}
}

func TestReconcile_LabelMatchIsCaseInsensitive(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "PREVIEW")}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "Preview", "Preview"))

	if got := deployedLogged(capture); len(got) != 1 {
		t.Errorf("deployed = %v, want one", got)
	}
}

func TestReconcile_ForkPRSkipped(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{pr: fetchedPR("front", true, "preview")}
	f.pr.HeadFullName = "octocat/front"
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "front", "preview", "preview"))

	f.assertNothingPushed(t)
	if td := teardownsLogged(capture); len(td) != 0 {
		t.Errorf("teardowns = %+v, want none", td)
	}
	logs := capture.find(slog.LevelInfo, "preview reconcile skipped")
	if len(logs) != 1 || logs[0]["reason"].String() != "fork pr" {
		t.Errorf("skip logs = %v, want reason fork pr", logs)
	}
}

func TestReconcile_DeployCommentFailureLogged(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics: metrics.New(),
		pr:      fetchedPR("monorepo", true, "preview"),
		fanErr:  errors.New("comment boom"),
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("labeled", "monorepo", "preview", "preview"))

	comment := capture.find(slog.LevelWarn, "preview deploy comment failed")
	if len(comment) != 1 || comment[0]["slug"].String() != "feature-pricing" ||
		comment[0]["err"].String() != "comment boom" {
		t.Errorf("preview deploy comment failed logs = %v", comment)
	}
	assertMetrics(t, f.Metrics, map[string]float64{`badil_reconciles_total{outcome="deployed"}`: 1},
		"badil_reconciles_total")
}

func TestReconcile_ExpiredContextFailsTheReconcile(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{Metrics: metrics.New(), pr: fetchedPR("monorepo", true, "preview")}
	api := f.api(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lifecycle.ReconcilePR(ctx, api, event("labeled", "monorepo", "preview", "preview"))

	f.assertNothingPushed(t)
	failed := capture.find(slog.LevelError, "preview reconcile failed")
	if len(failed) != 1 || !strings.Contains(failed[0]["err"].String(), context.Canceled.Error()) {
		t.Errorf("preview reconcile failed logs = %v, want one with %q", failed, context.Canceled)
	}
	assertMetrics(t, f.Metrics, map[string]float64{`badil_reconciles_total{outcome="failed"}`: 1},
		"badil_reconciles_total")
}

func TestReconcile_Errors(t *testing.T) {
	cases := []struct {
		name string
		f    *reconcileFake
	}{
		{name: "catalog", f: &reconcileFake{catalogErr: errors.New("boom")}},
		{name: "pull request", f: &reconcileFake{prErr: errors.New("boom")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			lifecycle.ReconcilePR(context.Background(), tc.f.api(t), event("labeled", "front", "preview", "preview"))

			if len(tc.f.calls) != 0 || len(teardownsLogged(capture)) != 0 {
				t.Errorf("calls = %v; want nothing", tc.f.calls)
			}
			tc.f.assertNothingPushed(t)
			logs := capture.find(slog.LevelError, "preview reconcile failed")
			if len(logs) != 1 || logs[0]["err"].String() != "boom" || logs[0]["repo"].String() != "acme/front" ||
				logs[0]["pr_number"].Int64() != 42 {
				t.Errorf("preview reconcile failed logs = %v", logs)
			}
		})
	}
}

// TestReconcile_PushFailure fails the push of a deploy that also leaves
// another preview: the reconcile fails, the teardown is recorded as failed,
// and nothing is commented.
func TestReconcile_PushFailure(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{
		Metrics:    metrics.New(),
		pr:         fetchedPR("front", true, "preview:pricing"),
		previews:   map[string]*preview.Values{"feature-pricing": values(pinnedBy("front", front42))},
		beforePush: func(*storetest.Remote) error { return errors.New("push boom") },
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t),
		event("labeled", "front", "preview:pricing", "preview:pricing"))

	f.assertNothingPushed(t)
	if len(f.upserts) != 0 || len(f.fanOuts) != 0 {
		t.Errorf("upserts = %q, fanOuts = %q; want none", f.upserts, f.fanOuts)
	}
	logs := capture.find(slog.LevelError, "preview reconcile failed")
	if len(logs) != 1 || !strings.Contains(logs[0]["err"].String(), "push boom") {
		t.Errorf("reconcile failed logs = %v", logs)
	}
	if failed := capture.find(slog.LevelError, "preview teardown failed"); len(failed) != 1 {
		t.Errorf("teardown failed logs = %v, want one", failed)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="failed"}`:                1,
		`badil_teardowns_total{result="error",trigger="webhook"}`: 1,
	}, "badil_reconciles_total", "badil_teardowns_total")
	if pins := f.g.pins("feature-pricing"); pins["front"] == "" {
		t.Errorf("feature-pricing pins = %v, want front still pinned", pins)
	}
}

// TestReconcile_OpLabels checks a reconcile that deploys is timed as a
// deploy operation, and one of a closed PR as a teardown.
func TestReconcile_OpLabels(t *testing.T) {
	cases := []struct {
		name string
		pr   *forge.PullRequest
		ev   lifecycle.Event
		op   string
	}{
		{
			"open labelled", fetchedPR("front", true, "preview"),
			event("labeled", "front", "preview", "preview"), "deploy",
		},
		{"closed", fetchedPR("front", false, "preview"), event("closed", "front", "", "preview"), "teardown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			m := metrics.New()
			remote := storetest.NewRemote(t, map[string]string{"badil.yaml": reconcileServices})
			st, err := store.Open(context.Background(), store.CloneConfig{URL: remote.URL()},
				storetest.Identity, store.WithMetrics(m))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			f := &reconcileFake{pr: tc.pr}
			lifecycle.ReconcilePR(context.Background(),
				lifecycle.ReconcileAPI{Store: st, Forge: f, Recorder: m, FanOut: f.fanOut}, tc.ev)

			ops := gitopsOps(t, m)
			delete(ops, "clone")
			delete(ops, "fetch")
			delete(ops, "ls_remote")
			if want := map[string]uint64{tc.op: 1}; !maps.Equal(ops, want) {
				t.Errorf("gitops operations = %v, want %v", ops, want)
			}
		})
	}
}

// gitopsOps returns how many gitops operations m timed, by op.
func gitopsOps(t *testing.T, m *metrics.Metrics) map[string]uint64 {
	t.Helper()
	families, err := m.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := map[string]uint64{}
	for _, mf := range families {
		if mf.GetName() != "badil_gitops_operation_duration_seconds" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "op" {
					out[l.GetValue()] += metric.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return out
}

// TestReconcile_MonorepoDeployOneCommit deploys two services of a monorepo PR
// that moves from its branch preview, where another PR keeps a pin.
func TestReconcile_MonorepoDeployOneCommit(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{
		pr: fetchedPR("monorepo", true, "preview:pricing"),
		previews: map[string]*preview.Values{"feature-pricing": values(
			pinnedBy("api", "https://github.com/acme/monorepo/pull/42"), pinnedBy("front", front42),
		)},
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t),
		event("labeled", "monorepo", "preview:pricing", "preview:pricing"))

	c := f.assertOneCommit(t, "chore(preview): deploy acme/monorepo#42 to pricing, unpin from feature-pricing",
		"feature-pricing: unpin api", "pricing: pin api@fe7c4ed", "pricing: pin worker@fe7c4ed")
	if pins := f.g.pins("pricing"); pins["api"] != fetchedSHA || pins["worker"] != fetchedSHA {
		t.Errorf("pricing pins = %v, want api and worker", pins)
	}
	if len(f.upserts) != 1 || !strings.HasSuffix(f.upserts[0], link(c)) {
		t.Errorf("upserts = %q, want the teardown comment linking the commit", f.upserts)
	}
	if len(f.fanOuts) != 2 {
		t.Errorf("fanOuts = %d, want one per preview", len(f.fanOuts))
	}
}

// TestReconcile_TeardownOnRetriedTree loses the first push, a delete, to a
// deploy by another PR: the retry unpins the closed PR and keeps the file.
func TestReconcile_TeardownOnRetriedTree(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{
		Metrics:  metrics.New(),
		pr:       fetchedPR("front", false),
		previews: map[string]*preview.Values{"feature-pricing": onlyFront42()},
	}
	raced := false
	f.beforePush = func(r *storetest.Remote) error {
		if !raced {
			raced = true
			r.Set("previews/feature-pricing/values.yaml", marshal(t, *values(
				pinnedBy("api", "https://github.com/acme/monorepo/pull/7"), pinnedBy("front", front42),
			)))
		}
		return nil
	}
	lifecycle.ReconcilePR(context.Background(), f.api(t), event("closed", "front", ""))

	c := f.lastCommit(t)
	if c.Subject() != "chore(preview): unpin acme/front#42 from feature-pricing" ||
		!slices.Equal(c.Body(), []string{"feature-pricing: unpin front"}) {
		t.Errorf("commit = %q, want the unpin of front", c.Message)
	}
	if pins := f.g.pins("feature-pricing"); pins["front"] != "" || pins["api"] != "abc123" {
		t.Errorf("pins = %v, want front unpinned, api kept", pins)
	}
	assertMetrics(t, f.Metrics, map[string]float64{
		`badil_reconciles_total{outcome="torn_down"}`:                1,
		`badil_teardowns_total{result="unpinned",trigger="webhook"}`: 1,
	}, "badil_reconciles_total", "badil_teardowns_total")
}

func TestReconcilePR_HeadSHA(t *testing.T) {
	const payload = "0000000000000000000000000000000000000001"
	cases := []struct {
		name string
		f    *reconcileFake
		ev   lifecycle.Event
		want lifecycle.ReconcileResult
	}{
		{
			name: "the read PR's head",
			f:    &reconcileFake{pr: fetchedPR("front", true, "preview")},
			ev:   event("synchronize", "front", "", "preview"),
			want: lifecycle.ReconcileResult{Outcome: lifecycle.OutcomeDeployed, HeadSHA: fetchedSHA},
		},
		{
			name: "the event's on a skip before the PR is read",
			f:    &reconcileFake{pr: fetchedPR("front", true, "preview")},
			ev:   event("synchronize", "front", ""),
			want: lifecycle.ReconcileResult{
				Outcome: lifecycle.OutcomeSkipped, Reason: lifecycle.NotLabeledReason, HeadSHA: payload,
			},
		},
		{
			name: "the event's on a repo outside the catalog",
			f:    &reconcileFake{pr: fetchedPR("front", true, "preview")},
			ev:   event("synchronize", "elsewhere", "", "preview"),
			want: lifecycle.ReconcileResult{
				Outcome: lifecycle.OutcomeSkipped, Reason: lifecycle.RepoNotInCatalogReason, HeadSHA: payload,
			},
		},
		{
			name: "the event's when the PR can't be read",
			f:    &reconcileFake{prErr: errors.New("boom")},
			ev:   event("synchronize", "front", "", "preview"),
			want: lifecycle.ReconcileResult{Outcome: lifecycle.OutcomeFailed, HeadSHA: payload},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLogs(t)
			tc.ev.HeadSHA = payload
			if got := lifecycle.ReconcilePR(context.Background(), tc.f.api(t), tc.ev); got != tc.want {
				t.Errorf("ReconcilePR = %+v, want %+v", got, tc.want)
			}
		})
	}
}
