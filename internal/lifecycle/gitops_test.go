package lifecycle_test

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

// gitops is the gitops repo of a lifecycle test: a remote and a store over
// it.
type gitops struct {
	t      *testing.T
	remote *storetest.Remote
	st     *store.GitHubStore
}

// newGitops returns a gitops repo holding badil (no badil.yaml when ""), the
// preview files of previews, each projected on badil's catalog when it
// parses so that a sync leaves it alone, and raw files (path to content), as
// storetest.Gitops makes it.
func newGitops(
	t *testing.T, badil string, previews map[string]*preview.Values, raw map[string]string,
) *gitops {
	t.Helper()
	remote, st := storetest.Gitops(t, badil, previews, nil, raw)
	return &gitops{t: t, remote: remote, st: st}
}

// marshal returns vals as a preview file.
func marshal(t *testing.T, vals preview.Values) string {
	t.Helper()
	out, err := yaml.Marshal(vals)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

// pushed returns the commits pushed since the remote was made, oldest first.
func (g *gitops) pushed() []storetest.Commit {
	g.t.Helper()
	commits := g.remote.Commits()
	out := make([]storetest.Commit, 0, len(commits))
	for i := len(commits) - 2; i >= 0; i-- {
		out = append(out, commits[i])
	}
	return out
}

// assertOneCommit fails t unless g was pushed exactly one commit, with
// subject and body, and returns it.
func (g *gitops) assertOneCommit(t *testing.T, subject string, body ...string) storetest.Commit {
	t.Helper()
	pushed := g.pushed()
	if len(pushed) != 1 || pushed[0].Subject() != subject || !slices.Equal(pushed[0].Body(), body) {
		var got []string
		for _, c := range pushed {
			got = append(got, c.Message)
		}
		t.Fatalf("pushed = %q, want one commit %q with body %q", got, subject, body)
	}
	return pushed[0]
}

// subjects returns the subjects of the pushed commits, oldest first.
func (g *gitops) subjects() []string {
	g.t.Helper()
	var out []string
	for _, c := range g.pushed() {
		out = append(out, c.Subject())
	}
	return out
}

// file returns the preview slug on the remote, nil when it has none.
func (g *gitops) file(slug string) *preview.Values {
	g.t.Helper()
	content, ok := g.remote.Files()["previews/"+slug+"/values.yaml"]
	if !ok {
		return nil
	}
	var vals preview.Values
	if err := yaml.Unmarshal([]byte(content), &vals); err != nil {
		g.t.Fatalf("parse %s: %v", slug, err)
	}
	return &vals
}

// pins returns, for the preview slug on the remote, each service's pinned
// commit ("" when it tracks its branch), nil when it has no file.
func (g *gitops) pins(slug string) map[string]string {
	g.t.Helper()
	vals := g.file(slug)
	if vals == nil {
		return nil
	}
	out := map[string]string{}
	for _, svc := range vals.Services {
		out[svc.Name] = svc.CommitSha
	}
	return out
}

// commitURL returns the GitHub URL of the gitops commit c.
func commitURL(c storetest.Commit) string {
	return "https://github.com/acme/gitops/commit/" + c.Hash
}

// link returns the last line of a comment linking to the commit c.
func link(c storetest.Commit) string {
	return "🔗 [" + c.Subject() + "](" + commitURL(c) + ")"
}

// teardownCall is one teardown of a PR from a preview.
type teardownCall struct {
	slug, owner, repo string
	number            int
}

// teardownsLogged returns the teardowns logged to capture, in order: done,
// skipped or failed.
func teardownsLogged(capture *logCapture) []teardownCall {
	var out []teardownCall
	for _, r := range capture.records() {
		if r.msg != "preview teardown" && r.msg != "preview teardown skipped" && r.msg != "preview teardown failed" {
			continue
		}
		owner, repo, _ := strings.Cut(r.attrs["repo"].String(), "/")
		out = append(out, teardownCall{
			slug: r.attrs["slug"].String(), owner: owner, repo: repo, number: int(r.attrs["pr_number"].Int64()),
		})
	}
	return out
}

// deployedLogged returns the services logged as deployed to capture, as
// "<slug>/<service>", in order.
func deployedLogged(capture *logCapture) []string {
	var out []string
	for _, attrs := range capture.find(slog.LevelInfo, "preview deploy") {
		out = append(out, attrs["slug"].String()+"/"+attrs["service"].String())
	}
	return out
}

// logCapture is a slog.Handler retaining every record.
type logCapture struct {
	mu      sync.Mutex
	entries []slog.Record
}

func (h *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, r.Clone())
	return nil
}

func (h *logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logCapture) WithGroup(string) slog.Handler      { return h }

// find returns the attributes of every record logged with msg at level.
func (h *logCapture) find(level slog.Level, msg string) []map[string]slog.Value {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]slog.Value
	for _, r := range h.entries {
		if r.Level != level || r.Message != msg {
			continue
		}
		attrs := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value
			return true
		})
		out = append(out, attrs)
	}
	return out
}

// logRecord is a record of a logCapture: its level, message and attributes.
type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]slog.Value
}

// records returns every record logged, in order.
func (h *logCapture) records() []logRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]logRecord, 0, len(h.entries))
	for _, r := range h.entries {
		attrs := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value
			return true
		})
		out = append(out, logRecord{level: r.Level, msg: r.Message, attrs: attrs})
	}
	return out
}

// count returns the number of records logged at level.
func (h *logCapture) count(level slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.entries {
		if r.Level == level {
			n++
		}
	}
	return n
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	capture := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return capture
}
