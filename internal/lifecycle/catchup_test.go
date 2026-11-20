package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
)

// queued is a reconcile catch-up started: its event and its done callback.
type queued struct {
	ev   lifecycle.Event
	done func(lifecycle.ReconcileResult)
}

// reconciles records the reconciles catch-up starts without running them.
type reconciles struct {
	mu   sync.Mutex
	jobs []queued
}

func (r *reconciles) reconcile(_ context.Context, ev lifecycle.Event, done func(lifecycle.ReconcileResult)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, queued{ev: ev, done: done})
}

// take returns the PR numbers of the reconciles started since the last take,
// in order, and completes each of them with outcome unless outcome is "".
func (r *reconciles) take(outcome string) []int {
	r.mu.Lock()
	jobs := r.jobs
	r.jobs = nil
	r.mu.Unlock()
	var numbers []int
	for _, j := range jobs {
		numbers = append(numbers, j.ev.Number)
		if outcome != "" {
			j.done(lifecycle.ReconcileResult{Outcome: outcome})
		}
	}
	return numbers
}

// listed is the open PR acme/front#number on branch feature/<number>, at
// head sha, with labels.
func listed(number int, sha string, labels ...string) forge.PullRequest {
	n := strconv.Itoa(number)
	return forge.PullRequest{
		Open:         true,
		HeadRef:      "feature/" + n,
		HeadSHA:      sha,
		Author:       "octocat",
		URL:          "https://github.com/acme/front/pull/" + n,
		Labels:       labels,
		FullName:     "acme/front",
		HeadFullName: "acme/front",
		Number:       number,
	}
}

// pinnedAt is an entry of front pinned by pr at sha.
func pinnedAt(pr forge.PullRequest, sha string) preview.ServiceEntry {
	e := pinnedBy("front", pr.URL)
	e.CommitSha = sha
	return e
}

// catchUp is a CatchUp over f whose reconciles are recorded in r.
func catchUp(t *testing.T, f *reconcileFake, r *reconciles) *lifecycle.CatchUp {
	t.Helper()
	f.open(t)
	return lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f}, r.reconcile)
}

// assertCatchUpLogged fails t unless the last catch-up line logged to capture
// holds want, keyed by attribute.
func assertCatchUpLogged(t *testing.T, capture *logCapture, want map[string]int64) {
	t.Helper()
	logs := capture.find(slog.LevelInfo, "catch-up")
	if len(logs) == 0 {
		t.Fatal("no catch-up line logged")
	}
	last := logs[len(logs)-1]
	for _, key := range []string{"repos", "open_labelled", "reconciled", "skipped_pinned", "skipped_seen", "list_errors"} {
		if got := last[key].Int64(); got != want[key] {
			t.Errorf("catch-up %s = %d, want %d", key, got, want[key])
		}
	}
}

func TestCatchUp_DecisionOrder(t *testing.T) {
	capture := captureLogs(t)
	unlabeled := listed(1, "s1", "bug")
	fork := listed(2, "s2", "preview")
	fork.HeadFullName = "octocat/front"
	pinned := listed(3, "s3", "preview")
	stale := listed(4, "s4", "Preview")
	conflict := listed(5, "s5", "preview:a", "preview:b")
	named := listed(6, "s6", "preview:demo")
	emptyNamed := listed(7, "s7", "preview:")
	f := &reconcileFake{
		Metrics: metrics.New(),
		previews: map[string]*preview.PreviewValues{
			"feature-3": values(pinnedAt(pinned, "s3")),
			"feature-4": values(pinnedAt(stale, "old")),
			"a":         values(pinnedAt(conflict, "s5")),
		},
		openPRs: map[string][]forge.PullRequest{
			"acme/front": {unlabeled, fork, pinned, stale, conflict, named, emptyNamed},
		},
		listErrs: map[string]error{"acme/monorepo": errors.New("boom")},
	}
	r := &reconciles{}
	catchUp(t, f, r).Run(context.Background())

	if want := []string{"acme/monorepo", "acme/front"}; !slices.Equal(f.listed, want) {
		t.Errorf("listed = %v, want %v", f.listed, want)
	}
	r.mu.Lock()
	jobs := slices.Clone(r.jobs)
	r.mu.Unlock()
	var got []int
	for _, j := range jobs {
		got = append(got, j.ev.Number)
	}
	if want := []int{4, 5, 6}; !slices.Equal(got, want) {
		t.Fatalf("reconciled = %v, want %v", got, want)
	}
	wantEv := lifecycle.Event{
		Target:  lifecycle.Target{Owner: "acme", Repo: "front", FullName: "acme/front", Number: 4},
		Action:  "synchronize",
		HeadRef: stale.HeadRef,
		Labels:  []string{"Preview"},
	}
	if ev := jobs[0].ev; ev.Target != wantEv.Target || ev.Action != wantEv.Action || ev.HeadRef != wantEv.HeadRef ||
		!slices.Equal(ev.Labels, wantEv.Labels) || ev.Label != "" {
		t.Errorf("event = %+v, want %+v", ev, wantEv)
	}
	assertCatchUpLogged(t, capture, map[string]int64{
		"repos": 2, "open_labelled": 5, "reconciled": 3, "skipped_pinned": 1, "list_errors": 1,
	})
	warns := capture.find(slog.LevelWarn, "catch-up list failed")
	if len(warns) != 1 || warns[0]["repo"].String() != "acme/monorepo" || warns[0]["err"].String() != "boom" {
		t.Errorf("list failures logged = %v, want one for acme/monorepo", warns)
	}
	assertMetrics(t, f.Metrics, map[string]float64{"badil_catchup_reconciles_total": 3},
		"badil_catchup_reconciles_total")
}

// TestCatchUp_PinnedPRURLInAnotherCase checks that an entry whose pr-url
// names the listed PR in another letter case, as a manual deploy may write
// it, counts as pinned at head.
func TestCatchUp_PinnedPRURLInAnotherCase(t *testing.T) {
	capture := captureLogs(t)
	pr := listed(7, "s7", "preview")
	entry := pinnedBy("front", "https://github.com/Acme/Front/pull/7")
	entry.CommitSha = "s7"
	f := &reconcileFake{
		previews: map[string]*preview.PreviewValues{"feature-7": values(entry)},
		openPRs:  map[string][]forge.PullRequest{"acme/front": {pr}},
	}
	r := &reconciles{}
	catchUp(t, f, r).Run(context.Background())

	if got := r.take(""); len(got) != 0 {
		t.Errorf("reconciled = %v, want none", got)
	}
	assertCatchUpLogged(t, capture, map[string]int64{"repos": 2, "open_labelled": 1, "skipped_pinned": 1})
}

func TestCatchUp_SkippedCallback(t *testing.T) {
	captureLogs(t)
	unlabeled := listed(1, "s1", "bug")
	fork := listed(2, "s2", "preview")
	fork.HeadFullName = "octocat/front"
	f := &reconcileFake{openPRs: map[string][]forge.PullRequest{
		"acme/front": {unlabeled, fork, listed(3, "s3", "preview")},
	}}
	f.open(t)
	type skip struct {
		t   lifecycle.Target
		res lifecycle.ReconcileResult
	}
	var skips []skip
	r := &reconciles{}
	api := lifecycle.CatchUpAPI{
		Store:    f,
		Forge:    f,
		Recorder: f,
		Skipped: func(t lifecycle.Target, res lifecycle.ReconcileResult) {
			skips = append(skips, skip{t, res})
		},
	}
	lifecycle.NewCatchUp(api, r.reconcile).Run(context.Background())

	target := func(n int) lifecycle.Target {
		return lifecycle.Target{Owner: "acme", Repo: "front", FullName: "acme/front", Number: n}
	}
	want := []skip{
		{target(1), lifecycle.ReconcileResult{Outcome: "skipped", Reason: "not labeled", HeadSHA: "s1"}},
		{target(2), lifecycle.ReconcileResult{Outcome: "skipped", Reason: "fork pr", HeadSHA: "s2"}},
	}
	if !slices.Equal(skips, want) {
		t.Errorf("skipped = %+v, want %+v", skips, want)
	}
	if got := r.take(""); !slices.Equal(got, []int{3}) {
		t.Errorf("reconciled = %v, want [3]", got)
	}
}

func TestCatchUp_SeenMap(t *testing.T) {
	cases := []struct {
		name    string
		outcome string
		want    []int
	}{
		{name: "skipped is not retried at the same head", outcome: "skipped"},
		{name: "deployed is not retried at the same head", outcome: "deployed"},
		{name: "torn down is not retried at the same head", outcome: "torn_down"},
		{name: "failed is retried at the same head", outcome: "failed", want: []int{4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := captureLogs(t)
			f := &reconcileFake{openPRs: map[string][]forge.PullRequest{"acme/front": {listed(4, "s4", "preview")}}}
			r := &reconciles{}
			c := catchUp(t, f, r)
			c.Run(context.Background())
			if got := r.take(tc.outcome); !slices.Equal(got, []int{4}) {
				t.Fatalf("first run reconciled %v, want [4]", got)
			}

			c.Run(context.Background())
			if got := r.take(tc.outcome); !slices.Equal(got, tc.want) {
				t.Errorf("second run reconciled %v, want %v", got, tc.want)
			}
			seen := int64(1 - len(tc.want))
			assertCatchUpLogged(t, capture, map[string]int64{
				"repos": 2, "open_labelled": 1, "reconciled": int64(len(tc.want)), "skipped_seen": seen,
			})
		})
	}
}

func TestCatchUp_NewHeadIsReconciledAgain(t *testing.T) {
	captureLogs(t)
	f := &reconcileFake{openPRs: map[string][]forge.PullRequest{"acme/front": {listed(4, "s4", "preview")}}}
	r := &reconciles{}
	c := catchUp(t, f, r)
	c.Run(context.Background())
	r.take("skipped")

	f.openPRs["acme/front"] = []forge.PullRequest{listed(4, "s4-new", "preview")}
	c.Run(context.Background())
	if got := r.take("skipped"); !slices.Equal(got, []int{4}) {
		t.Errorf("reconciled at a new head = %v, want [4]", got)
	}
}

func TestCatchUp_QueuedIsNotQueuedTwice(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{openPRs: map[string][]forge.PullRequest{"acme/front": {listed(4, "s4", "preview")}}}
	r := &reconciles{}
	c := catchUp(t, f, r)
	c.Run(context.Background())
	r.mu.Lock()
	first := r.jobs[0]
	r.jobs = nil
	r.mu.Unlock()

	f.openPRs["acme/front"] = []forge.PullRequest{listed(4, "s4-new", "preview")}
	c.Run(context.Background())
	if got := r.take(""); len(got) != 0 {
		t.Fatalf("reconciled while queued = %v, want none", got)
	}
	assertCatchUpLogged(t, capture, map[string]int64{"repos": 2, "open_labelled": 1, "skipped_seen": 1})

	first.done(lifecycle.ReconcileResult{Outcome: "failed"})
	c.Run(context.Background())
	if got := r.take(""); !slices.Equal(got, []int{4}) {
		t.Errorf("reconciled once the queued one failed = %v, want [4]", got)
	}
}

func TestCatchUp_StopsOnceCtxIsDone(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{openPRs: map[string][]forge.PullRequest{
		"acme/monorepo": {listed(7, "s7", "preview"), listed(8, "s8", "preview")},
		"acme/front":    {listed(4, "s4", "preview")},
	}}
	f.open(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []int
	reconcile := func(_ context.Context, ev lifecycle.Event, done func(lifecycle.ReconcileResult)) {
		got = append(got, ev.Number)
		cancel()
		done(lifecycle.ReconcileResult{Outcome: "skipped"})
	}
	lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f}, reconcile).Run(ctx)

	if !slices.Equal(got, []int{7}) {
		t.Errorf("reconciled = %v, want [7]", got)
	}
	if want := []string{"acme/monorepo"}; !slices.Equal(f.listed, want) {
		t.Errorf("listed = %v, want %v", f.listed, want)
	}
	assertCatchUpLogged(t, capture, map[string]int64{"repos": 2, "open_labelled": 1, "reconciled": 1})
}

func TestCatchUp_CatalogUnreadable(t *testing.T) {
	capture := captureLogs(t)
	f := &reconcileFake{catalogErr: errors.New("boom")}
	r := &reconciles{}
	catchUp(t, f, r).Run(context.Background())

	if len(f.listed) != 0 || len(r.jobs) != 0 {
		t.Errorf("listed %v and reconciled %d, want nothing", f.listed, len(r.jobs))
	}
	if logs := capture.find(slog.LevelError, "catch-up failed"); len(logs) != 1 || logs[0]["err"].String() != "boom" {
		t.Errorf("catch-up failed logs = %v, want one with err boom", logs)
	}
}

// syncReconcile reconciles through ReconcilePR over api, at once.
func syncReconcile(api lifecycle.ReconcileAPI) lifecycle.ReconcileFunc {
	return func(ctx context.Context, ev lifecycle.Event, done func(lifecycle.ReconcileResult)) {
		done(lifecycle.ReconcilePR(ctx, api, ev))
	}
}

func TestCatchUp_ReconcilePinsThenSkipsPinned(t *testing.T) {
	capture := captureLogs(t)
	pr := fetchedPR("front", true, "preview")
	pr.Number = 42
	f := &reconcileFake{pr: pr, openPRs: map[string][]forge.PullRequest{"acme/front": {*pr}}}
	c := lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f}, syncReconcile(f.api(t)))

	c.Run(context.Background())
	if pushed := f.g.subjects(); len(pushed) != 1 {
		t.Fatalf("pushed = %q, want one deploy", pushed)
	}
	assertCatchUpLogged(t, capture, map[string]int64{"repos": 2, "open_labelled": 1, "reconciled": 1})

	// A new process, with an empty seen map, finds it pinned at head.
	c = lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f}, syncReconcile(f.api(t)))
	c.Run(context.Background())
	if pushed := f.g.subjects(); len(pushed) != 1 {
		t.Errorf("pushed = %q, want only the first deploy", pushed)
	}
	assertCatchUpLogged(t, capture, map[string]int64{"repos": 2, "open_labelled": 1, "skipped_pinned": 1})
}

func TestCatchUp_NoServiceChangedReconciledOncePerHead(t *testing.T) {
	capture := captureLogs(t)
	f := platformFake(changed("docs/README.md")...)
	f.pr.Number = 42
	f.openPRs = map[string][]forge.PullRequest{"acme/platform": {*f.pr}}
	c := lifecycle.NewCatchUp(lifecycle.CatchUpAPI{Store: f, Forge: f, Recorder: f}, syncReconcile(f.api(t)))

	c.Run(context.Background())
	c.Run(context.Background())
	if got := f.upserts; len(got) != 1 || got[0] != noServiceBody {
		t.Errorf("upserts = %q, want one no-service comment", got)
	}
	assertCatchUpLogged(t, capture, map[string]int64{"repos": 2, "open_labelled": 1, "skipped_seen": 1})
	assertMetrics(t, f.Metrics, map[string]float64{
		"badil_catchup_reconciles_total":                           1,
		`badil_reconcile_skips_total{reason="no service changed"}`: 1,
	}, "badil_catchup_reconciles_total", "badil_reconcile_skips_total")
}
