package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/metrics"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/status"
	"github.com/badilhq/badil/internal/store"
)

const testStatusToken = "test-status-token"

// removedArgoCDPath is the route the status callback replaced.
var removedArgoCDPath = path.Join("/webhooks", "argocd") //nolint:gochecknoglobals // test fixture

// statusSHA is the full SHA the renderer reports for backend-1, pinned by
// its 7-character prefix.
const statusSHA = "abc1234def5678abc1234def5678abc1234def56"

// statusUpdatedAt is backend-1's updated-at in the status tests' preview.
const statusUpdatedAt = "2026-11-19T21:00:00Z"

// statusPreviews is feature-x: backend-1 pinned at a short SHA, front
// tracking its branch, and the dependency postgres.
func statusPreviews() map[string]*preview.Values {
	return map[string]*preview.Values{
		"feature-x": {
			APIVersion: preview.APIVersion,
			Services: []preview.ServiceEntry{
				{
					Name: "backend-1", CommitSha: statusSHA[:7],
					Metadata: &preview.Metadata{CreatedAt: statusUpdatedAt, UpdatedAt: statusUpdatedAt},
				},
				{Name: "front"},
			},
			Dependencies: []preview.DependencyEntry{{Name: "postgres"}},
		},
	}
}

// newStatusServer returns a server with the status route enabled by token
// over a memory-only status store, its fake gitops holding statusPreviews
// (unprojected), its metrics and its captured logs.
func newStatusServer(
	t *testing.T, token string, fg *fakeGitops,
) (*Server, *status.Store, *metrics.Metrics, *LogCapture) {
	t.Helper()
	capture := &LogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })
	if fg == nil {
		fg = &fakeGitops{}
	}
	if fg.previews == nil {
		fg.previews = statusPreviews()
		fg.stale = map[string]bool{"feature-x": true}
	}
	fg.open(t)
	m := metrics.New()
	st := status.NewMemory()
	a := app.New(app.Config{Store: fg, Forge: fg, Metrics: m, Status: st})
	return NewServer(a, fg, nil, fg, "test-deploy-token", WithMetrics(m), WithStatus(token, st)), st, m, capture
}

func postStatus(srv *Server, path, body, authorization string) *httptest.ResponseRecorder {
	return send(srv, http.MethodPost, path, body, authorization)
}

// statusBody is a status event of feature-x.
func statusBody(service, revision, s, at string) string {
	return `{"apiVersion":"badil.dev/v1beta1","preview":"feature-x","service":"` + service +
		`","revision":"` + revision + `","status":"` + s + `","at":"` + at + `","extra":1}`
}

// sendStatus posts body with the right token and fails t unless it is
// answered 204.
func sendStatus(t *testing.T, srv *Server, body string) {
	t.Helper()
	rec := postStatus(srv, statusPath, body, "Bearer "+testStatusToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d %q, want 204", rec.Code, rec.Body.String())
	}
}

// spinUpCount returns the samples of badil_preview_spinup_seconds for
// backend-1, and their sum.
func spinUpCount(t *testing.T, m *metrics.Metrics) (uint64, float64) {
	t.Helper()
	families, err := m.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "badil_preview_spinup_seconds" {
			continue
		}
		for _, metric := range f.GetMetric() {
			return metric.GetHistogram().GetSampleCount(), metric.GetHistogram().GetSampleSum()
		}
	}
	return 0, 0
}

// lastStatusLog returns the attributes of the last "status event" record.
func lastStatusLog(t *testing.T, capture *LogCapture) (slog.Level, map[string]string) {
	t.Helper()
	records := capture.Records()
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Message != "status event" {
			continue
		}
		attrs := map[string]string{}
		records[i].Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		return records[i].Level, attrs
	}
	t.Fatal("no status event log record")
	return 0, nil
}

func TestStatusEvent_RouteAbsentWithoutToken(t *testing.T) {
	srv, _, _, _ := newStatusServer(t, "", nil)
	for _, route := range []string{statusPath, removedArgoCDPath} {
		rec := postStatus(srv, route, statusBody("backend-1", statusSHA, "ready", statusUpdatedAt), "Bearer ")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", route, rec.Code)
		}
	}
	srv, _, _, _ = newStatusServer(t, testStatusToken, nil)
	if rec := postStatus(srv, removedArgoCDPath, "{}", "Bearer "+testStatusToken); rec.Code != http.StatusNotFound {
		t.Errorf("%s with the status token: status = %d, want 404", removedArgoCDPath, rec.Code)
	}
}

func TestStatusEvent_Unauthorized(t *testing.T) {
	for name, authorization := range map[string]string{
		"missing": "",
		"wrong":   "Bearer not-the-token",
		"scheme":  "Basic " + testStatusToken,
	} {
		t.Run(name, func(t *testing.T) {
			srv, st, m, _ := newStatusServer(t, testStatusToken, nil)
			rec := postStatus(
				srv,
				statusPath,
				statusBody("backend-1", statusSHA, "ready", statusUpdatedAt),
				authorization,
			)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("got %d %q, want 401", rec.Code, rec.Body.String())
			}
			assertCounter(t, m, "badil_status_events_total")
			if len(st.Preview("feature-x")) != 0 {
				t.Error("an unauthorized event was recorded")
			}
		})
	}
}

func TestStatusEvent_InvalidPayload(t *testing.T) {
	valid := func(mutate func(string) string) string {
		return mutate(statusBody("backend-1", statusSHA, "ready", statusUpdatedAt))
	}
	for name, body := range map[string]string{
		"bad json":       `{not json`,
		"not object":     `["backend-1"]`,
		"wrong type":     `{"service":1}`,
		"oversize":       `{"preview":"` + strings.Repeat("a", 64<<10) + `"}`,
		"no apiVersion":  valid(func(s string) string { return strings.Replace(s, `"apiVersion":"badil.dev/v1beta1",`, "", 1) }),
		"other version":  valid(func(s string) string { return strings.Replace(s, "v1beta1", "v1", 1) }),
		"no preview":     valid(func(s string) string { return strings.Replace(s, `"feature-x"`, `""`, 1) }),
		"no service":     statusBody("", statusSHA, "ready", statusUpdatedAt),
		"no revision":    statusBody("backend-1", "", "ready", statusUpdatedAt),
		"unknown status": statusBody("backend-1", statusSHA, "healthy", statusUpdatedAt),
		"no status":      statusBody("backend-1", statusSHA, "", statusUpdatedAt),
		"bad at":         statusBody("backend-1", statusSHA, "ready", "2026-11-19 21:00:00"),
		"no at":          statusBody("backend-1", statusSHA, "ready", ""),
	} {
		t.Run(name, func(t *testing.T) {
			srv, st, m, _ := newStatusServer(t, testStatusToken, nil)
			rec := postStatus(srv, statusPath, body, "Bearer "+testStatusToken)
			if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Body.String(), "invalid payload") {
				t.Errorf("got %d %q, want 400 invalid payload", rec.Code, rec.Body.String())
			}
			assertCounter(t, m, "badil_status_events_total")
			if len(st.Preview("feature-x")) != 0 {
				t.Error("an invalid event was recorded")
			}
		})
	}
}

// TestStatusEvent_Recorded checks that a valid event of a known service or
// dependency is recorded in memory, counted and logged.
func TestStatusEvent_Recorded(t *testing.T) {
	srv, st, m, capture := newStatusServer(t, testStatusToken, nil)
	sendStatus(t, srv, statusBody("backend-1", statusSHA, "progressing", "2026-11-19T21:01:00Z"))
	sendStatus(t, srv, statusBody("postgres", "16.4.2,0123456789abcdef", "degraded", "2026-11-19T21:01:30Z"))

	got := st.Preview("feature-x")
	if e := got["backend-1"]; e.Revision != statusSHA || e.Status != "progressing" ||
		!e.At.Equal(time.Date(2026, 11, 19, 21, 1, 0, 0, time.UTC)) {
		t.Errorf("backend-1 = %+v", e)
	}
	if e := got["postgres"]; e.Revision != "16.4.2,0123456789abcdef" || e.Status != "degraded" {
		t.Errorf("postgres = %+v", e)
	}
	assertCounter(t, m, "badil_status_events_total",
		`{result="recorded",status="degraded"} 1`, `{result="recorded",status="progressing"} 1`)
	_, attrs := lastStatusLog(t, capture)
	want := map[string]string{
		"preview": "feature-x", "service": "postgres", "revision": "16.4.2,0123456789abcdef",
		"status": "degraded", "result": "recorded",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("log %s = %q, want %q", k, attrs[k], v)
		}
	}
}

// TestStatusEvent_Ignored checks the three reasons a valid event is ignored,
// each answered 204, counted, logged with its reason and not recorded.
func TestStatusEvent_Ignored(t *testing.T) {
	srv, st, m, capture := newStatusServer(t, testStatusToken, nil)
	sendStatus(t, srv, statusBody("backend-1", statusSHA, "ready", "2026-11-19T21:05:00Z"))

	for _, tt := range []struct {
		body, reason string
	}{
		{
			strings.Replace(statusBody("backend-1", statusSHA, "ready", "2026-11-19T21:06:00Z"),
				"feature-x", "no-such-preview", 1),
			"unknown_preview",
		},
		{statusBody("no-such-service", statusSHA, "ready", "2026-11-19T21:06:00Z"), "unknown_service"},
		{statusBody("backend-1", "older", "degraded", "2026-11-19T21:04:59Z"), "stale"},
	} {
		sendStatus(t, srv, tt.body)
		_, attrs := lastStatusLog(t, capture)
		if attrs["result"] != "ignored" || attrs["reason"] != tt.reason {
			t.Errorf("%s: log result, reason = %q, %q", tt.reason, attrs["result"], attrs["reason"])
		}
	}
	if e := st.Preview("feature-x")["backend-1"]; e.Status != "ready" || e.Revision != statusSHA {
		t.Errorf("backend-1 = %+v, want the first ready kept", e)
	}
	if len(st.Preview("no-such-preview")) != 0 {
		t.Error("unknown preview recorded")
	}
	assertCounter(t, m, "badil_status_events_total",
		`{result="ignored",status="degraded"} 1`,
		`{result="ignored",status="ready"} 2`,
		`{result="recorded",status="ready"} 1`,
	)
}

// TestStatusEvent_PreviewReadFails checks that a failed preview read answers
// 503, records nothing, and is counted and logged as an error.
func TestStatusEvent_PreviewReadFails(t *testing.T) {
	fg := &fakeGitops{getPreviewErr: errors.New("ls-remote gitops repo: connection refused")}
	srv, st, m, capture := newStatusServer(t, testStatusToken, fg)
	rec := postStatus(srv, statusPath, statusBody("backend-1", statusSHA, "ready", "2026-11-19T21:05:00Z"),
		"Bearer "+testStatusToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %q, want 503", rec.Code, rec.Body.String())
	}
	if len(st.Preview("feature-x")) != 0 {
		t.Error("recorded despite the failed read")
	}
	assertCounter(t, m, "badil_status_events_total", `{result="error",status="ready"} 1`)
	if n, _ := spinUpCount(t, m); n != 0 {
		t.Errorf("spin-up observed %d times, want 0", n)
	}
	level, attrs := lastStatusLog(t, capture)
	if level != slog.LevelError || attrs["result"] != "error" ||
		attrs["err"] != "ls-remote gitops repo: connection refused" {
		t.Errorf("log = %v %v, want an error line with the error", level, attrs)
	}
}

// loggedSpinUp reports whether a captured record has spinup_seconds=seconds.
func loggedSpinUp(capture *LogCapture, seconds float64) bool {
	found := false
	for _, r := range capture.Records() {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "spinup_seconds" && a.Value.Float64() == seconds {
				found = true
			}
			return true
		})
	}
	return found
}

// TestStatusEvent_SpinUp checks that spin-up is observed only for the first
// ready whose revision starts with the 7-character pin, within 24 h of
// updated-at.
func TestStatusEvent_SpinUp(t *testing.T) {
	for _, tt := range []struct {
		name   string
		events []string
		want   uint64
		sum    float64
	}{
		{
			name: "first ready at the pin",
			events: []string{
				statusBody("backend-1", statusSHA, "progressing", "2026-11-19T21:01:00Z"),
				statusBody("backend-1", statusSHA, "ready", "2026-11-19T21:02:00Z"),
				statusBody("backend-1", statusSHA, "degraded", "2026-11-19T21:03:00Z"),
				statusBody("backend-1", statusSHA, "ready", "2026-11-19T21:04:00Z"),
			},
			want: 1, sum: 120,
		},
		{
			name:   "revision of another commit",
			events: []string{statusBody("backend-1", "abc1235"+statusSHA[7:], "ready", "2026-11-19T21:02:00Z")},
		},
		{
			name:   "revision shorter than the pin",
			events: []string{statusBody("backend-1", statusSHA[:6], "ready", "2026-11-19T21:02:00Z")},
		},
		{
			name:   "before updated-at",
			events: []string{statusBody("backend-1", statusSHA, "ready", "2026-11-19T20:59:59Z")},
		},
		{
			name:   "at updated-at",
			events: []string{statusBody("backend-1", statusSHA, "ready", statusUpdatedAt)},
			want:   1,
		},
		{
			name:   "24 h after updated-at",
			events: []string{statusBody("backend-1", statusSHA, "ready", "2026-11-20T21:00:00Z")},
			want:   1, sum: 86400,
		},
		{
			name:   "over 24 h after updated-at",
			events: []string{statusBody("backend-1", statusSHA, "ready", "2026-11-20T21:00:01Z")},
		},
		{
			name:   "branch-tracking service",
			events: []string{statusBody("front", statusSHA, "ready", "2026-11-19T21:02:00Z")},
		},
		{
			name:   "dependency",
			events: []string{statusBody("postgres", statusSHA, "ready", "2026-11-19T21:02:00Z")},
		},
		{
			name: "stale ready",
			events: []string{
				statusBody("backend-1", statusSHA, "degraded", "2026-11-19T21:03:00Z"),
				statusBody("backend-1", statusSHA, "ready", "2026-11-19T21:02:00Z"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, m, capture := newStatusServer(t, testStatusToken, nil)
			for _, body := range tt.events {
				sendStatus(t, srv, body)
			}
			n, sum := spinUpCount(t, m)
			if n != tt.want || sum != tt.sum {
				t.Errorf("spin-up samples, sum = %d, %v; want %d, %v", n, sum, tt.want, tt.sum)
			}
			if tt.want == 1 && tt.sum > 0 && !loggedSpinUp(capture, tt.sum) {
				t.Errorf("no status event logged spinup_seconds=%v", tt.sum)
			}
		})
	}
}

// TestStatusEvent_PreviewDeletedWhileRecording checks that an event whose
// preview is deleted between its read and its record leaves no status: the
// deletion is either one of badil's own pushes, whose WithApplied Delete ran
// before the record, or an outside commit the next read fetches.
func TestStatusEvent_PreviewDeletedWhileRecording(t *testing.T) {
	for _, tt := range []struct {
		name   string
		delete func(t *testing.T, fg *fakeGitops, st *status.Store)
	}{
		{
			name: "deleted by apply",
			delete: func(t *testing.T, fg *fakeGitops, st *status.Store) {
				t.Helper()
				res, err := fg.st.Apply(t.Context(), store.Op{
					Kind: store.OpTeardown,
					Change: func(tree lifecycle.Tree) (store.Summary, error) {
						tree.DeletePreview("feature-x")
						return store.Summary{Subject: "teardown feature-x"}, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				// What badil serve's WithApplied callback does.
				st.Delete(res.Deleted...)
			},
		},
		{
			name: "deleted outside",
			delete: func(t *testing.T, fg *fakeGitops, _ *status.Store) {
				t.Helper()
				fg.remote.Change(map[string]string{"previews/feature-x/values.yaml": ""})
				fg.st.Invalidate()
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fg := &fakeGitops{}
			var st *status.Store
			reads := 0
			fg.afterGetPreview = func() {
				reads++
				if reads == 1 {
					tt.delete(t, fg, st)
				}
			}
			srv, st0, m, capture := newStatusServer(t, testStatusToken, fg)
			st = st0
			sendStatus(t, srv, statusBody("backend-1", statusSHA, "ready", "2026-11-19T21:05:00Z"))

			if got := st.Preview("feature-x"); len(got) != 0 {
				t.Errorf("status of the deleted preview = %v, want none", got)
			}
			_, attrs := lastStatusLog(t, capture)
			if attrs["result"] != "ignored" || attrs["reason"] != "unknown_preview" {
				t.Errorf("log result, reason = %q, %q, want ignored, unknown_preview", attrs["result"], attrs["reason"])
			}
			assertCounter(t, m, "badil_status_events_total", `{result="ignored",status="ready"} 1`)
			if n, _ := spinUpCount(t, m); n != 0 {
				t.Errorf("spin-up observed %d times, want 0", n)
			}
		})
	}
}

// listerFunc lists the previews with a function.
type listerFunc func(ctx context.Context) ([]string, error)

func (f listerFunc) ListPreviewFiles(ctx context.Context) ([]string, error) { return f(ctx) }

// TestStatusEvent_RereadFails checks that an event whose second preview read
// fails stays recorded, logs the failure and asks for a garbage collection,
// which removes the status of a preview the gitops repo no longer lists.
func TestStatusEvent_RereadFails(t *testing.T) {
	fg := &fakeGitops{}
	reads := 0
	fg.afterGetPreview = func() {
		reads++
		if reads == 1 {
			fg.getPreviewErr = errors.New("ls-remote gitops repo: connection refused")
		}
	}
	srv, st, m, capture := newStatusServer(t, testStatusToken, fg)
	listed := make(chan struct{}, 4)
	st.Start(t.Context(), listerFunc(func(context.Context) ([]string, error) {
		listed <- struct{}{}
		return nil, nil
	}))
	t.Cleanup(func() { st.Close(context.Background()) })
	// The garbage collection Start asks for listed before the event, so it
	// keeps what the event records.
	<-listed

	sendStatus(t, srv, statusBody("backend-1", statusSHA, "progressing", "2026-11-19T21:05:00Z"))
	_, attrs := lastStatusLog(t, capture)
	if attrs["result"] != "recorded" {
		t.Errorf("log result = %q, want recorded", attrs["result"])
	}
	assertCounter(t, m, "badil_status_events_total", `{result="recorded",status="progressing"} 1`)
	warned := false
	for _, r := range capture.Records() {
		if r.Message == "status event preview re-read failed" && r.Level == slog.LevelWarn {
			warned = true
		}
	}
	if !warned {
		t.Error("no warning for the failed re-read")
	}

	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("no garbage collection after the failed re-read")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(st.Preview("feature-x")) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("status of the unlisted preview = %v, want none", st.Preview("feature-x"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
