package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// reply is one scripted answer of a fake badil: a status and a body.
type reply struct {
	status int
	body   string
}

// fakeBadil is a badil server answering each request with the next reply,
// the last one again once they run out. It records the requests' URIs and
// Authorization headers.
type fakeBadil struct {
	mu      sync.Mutex
	replies []reply
	uris    []string
	auths   []string
}

func (f *fakeBadil) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.uris = append(f.uris, r.URL.RequestURI())
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	next := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(next.status)
	_, _ = w.Write([]byte(next.body))
}

// requests returns the URIs requested so far.
func (f *fakeBadil) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.uris...)
}

// startBadil serves replies and returns the fake and its URL.
func startBadil(t *testing.T, replies ...reply) (*fakeBadil, string) {
	t.Helper()
	f := &fakeBadil{replies: replies}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// runBadil runs badil with args and returns its stdout and exit code.
func runBadil(t *testing.T, args ...string) (string, int) {
	t.Helper()
	root := rootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		return out.String(), exitCode(err)
	}
	return out.String(), 0
}

// fastRetries shortens badil wait's retry interval for t.
func fastRetries(t *testing.T) {
	t.Helper()
	prev := retryInterval
	retryInterval = 10 * time.Millisecond
	t.Cleanup(func() { retryInterval = prev })
}

func waitBody(state, reason string) string {
	return `{"state":"` + state + `","reason":"` + reason + `","slug":"feature-x","services":[],` +
		`"urls":{"front":"https://front-feature-x.example.test/","api":"https://api-feature-x.example.test/"}}`
}

func TestWait_ExitCodes(t *testing.T) {
	fastRetries(t)
	ok := func(state, reason string) reply { return reply{http.StatusOK, waitBody(state, reason)} }
	cases := []struct {
		name    string
		replies []reply
		args    []string
		want    int
	}{
		{name: "ready", replies: []reply{ok("progressing", ""), ok("ready", "")}, want: 0},
		{name: "skipped", replies: []reply{ok("skipped", "not labeled")}, want: 0},
		{name: "failed", replies: []reply{ok("failed", "front is degraded at abc")}, want: 1},
		{name: "refused", replies: []reply{ok("refused", "fork pr")}, want: 1},
		{
			name:    "timeout while progressing",
			replies: []reply{ok("progressing", "")},
			args:    []string{"--timeout", "200ms"},
			want:    2,
		},
		{
			name:    "timeout while pending",
			replies: []reply{ok("pending", "")},
			args:    []string{"--timeout", "200ms"},
			want:    2,
		},
		{
			name:    "503 retried",
			replies: []reply{{http.StatusServiceUnavailable, `{"error":"ls-remote"}`}, ok("ready", "")},
			want:    0,
		},
		{
			name:    "timeout after a 503",
			replies: []reply{ok("progressing", ""), {http.StatusServiceUnavailable, `{"error":"ls-remote"}`}},
			args:    []string{"--timeout", "200ms"},
			want:    3,
		},
		{
			name:    "500 retried",
			replies: []reply{{http.StatusInternalServerError, "boom"}, ok("refused", "torn down")},
			want:    1,
		},
		{name: "not found", replies: []reply{{http.StatusNotFound, `{"error":"preview not found"}`}}, want: 3},
		{name: "unauthorized", replies: []reply{{http.StatusUnauthorized, "unauthorized"}}, want: 3},
		{name: "forbidden", replies: []reply{{http.StatusForbidden, "forbidden"}}, want: 3},
		{
			name:    "invalid query",
			replies: []reply{{http.StatusBadRequest, `{"error":"pr must be a positive integer"}`}},
			want:    3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, url := startBadil(t, tc.replies...)
			args := append([]string{"wait", "--server", url, "--slug", "feature-x"}, tc.args...)
			if _, code := runBadil(t, args...); code != tc.want {
				t.Errorf("exit code = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestWait_ConnectionErrorsUntilTheTimeout(t *testing.T) {
	fastRetries(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, code := runBadil(t, "wait", "--server", url, "--slug", "x", "--timeout", "100ms"); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestWait_BadFlags(t *testing.T) {
	_, url := startBadil(t, reply{http.StatusOK, waitBody("ready", "")})
	cases := map[string][]string{
		"no target":           {"wait", "--server", url},
		"both targets":        {"wait", "--server", url, "--slug", "x", "--repo", "acme/front", "--pr", "7"},
		"sha with slug":       {"wait", "--server", url, "--slug", "x", "--sha", "abc"},
		"repo without pr":     {"wait", "--server", url, "--repo", "acme/front"},
		"pr not a number":     {"wait", "--server", url, "--repo", "acme/front", "--pr", "abc"},
		"bad timeout":         {"wait", "--server", url, "--slug", "x", "--timeout", "soon"},
		"zero timeout":        {"wait", "--server", url, "--slug", "x", "--timeout", "0s"},
		"unknown flag":        {"wait", "--server", url, "--slug", "x", "--color"},
		"no server":           {"wait", "--slug", "x"},
		"positional argument": {"wait", "--server", url, "--slug", "x", "extra"},
	}
	t.Setenv("BADIL_SERVER", "")
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, code := runBadil(t, args...); code != 3 {
				t.Errorf("exit code = %d, want 3", code)
			}
		})
	}
}

func TestWait_RequestAndOutput(t *testing.T) {
	f, url := startBadil(t,
		reply{http.StatusOK, waitBody("pending", "")},
		reply{http.StatusOK, waitBody("progressing", "")},
		reply{http.StatusOK, waitBody("progressing", "")},
		reply{http.StatusOK, waitBody("ready", "")},
	)
	t.Setenv("BADIL_SERVER", url+"/")
	t.Setenv("BADIL_TOKEN", "tok")
	out, code := runBadil(t, "wait", "--repo", "acme/front", "--pr", "7", "--sha", "abc1234")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	want := "pending\nprogressing\nready\n  api: https://api-feature-x.example.test/\n  front: https://front-feature-x.example.test/\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if got := f.requests()[0]; got != "/v1/wait?pr=7&repo=acme%2Ffront&sha=abc1234" {
		t.Errorf("request = %q", got)
	}
	if f.auths[0] != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", f.auths[0])
	}
}

func TestWait_FailedOutputAndJSON(t *testing.T) {
	_, url := startBadil(
		t,
		reply{http.StatusOK, waitBody("progressing", "")},
		reply{http.StatusOK, waitBody("failed", "front is degraded at abc")},
	)
	out, code := runBadil(t, "wait", "--server", url, "--slug", "feature-x")
	if code != 1 || !strings.HasPrefix(out, "progressing\nfailed: front is degraded at abc\n") {
		t.Errorf("exit %d output %q, want 1 and the states", code, out)
	}

	_, url = startBadil(
		t,
		reply{http.StatusOK, waitBody("progressing", "")},
		reply{http.StatusOK, waitBody("ready", "")},
	)
	out, code = runBadil(t, "wait", "--server", url, "--slug", "feature-x", "--json")
	if code != 0 || out != waitBody("ready", "") {
		t.Errorf("exit %d output %q, want 0 and the final body", code, out)
	}
}

func TestWait_SlugPath(t *testing.T) {
	f, url := startBadil(t, reply{http.StatusOK, waitBody("ready", "")})
	if _, code := runBadil(t, "wait", "--server", url, "--slug", "feature-x"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := f.requests(); len(got) != 1 || got[0] != "/v1/previews/feature-x/wait" {
		t.Errorf("requests = %v", got)
	}
}

const listBody = `{"previews":[` +
	`{"slug":"bare","services":[{"name":"front","kind":"service","branch":"main","status":"unknown"}]},` +
	`{"slug":"feature-x","updatedAt":"2026-11-19T21:00:00Z","services":[` +
	`{"name":"backend-1","kind":"service","pinned":{"sha":"abc1234","pr":{"repo":"acme/backend-1","number":42,"url":"u"},"author":"o"},"status":"ready","revision":"abc1234def"},` +
	`{"name":"front","kind":"service","pinned":{"sha":"fff0000","pr":{"repo":"acme/front","number":7,"url":"u"},"author":"o"},"status":"ready","revision":"abc1234def"},` +
	`{"name":"postgres","kind":"dependency","status":"ready"}]}]}`

func TestList(t *testing.T) {
	f, url := startBadil(t, reply{http.StatusOK, listBody})
	out, code := runBadil(t, "list", "--server", url, "--token", "tok")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	want := "SLUG       SERVICES  PINNED           READY  UPDATED\n" +
		"bare       1         -                0/0    -\n" +
		"feature-x  3         backend-1,front  1/2    2026-11-19T21:00:00Z\n"
	if out != want {
		t.Errorf("output =\n%s\nwant\n%s", out, want)
	}
	if got := f.requests(); len(got) != 1 || got[0] != "/v1/previews" || f.auths[0] != "Bearer tok" {
		t.Errorf("requests = %v with %v", got, f.auths)
	}

	out, code = runBadil(t, "list", "--server", url, "--json")
	if code != 0 || out != listBody {
		t.Errorf("exit %d output %q, want 0 and the body", code, out)
	}

	_, url = startBadil(t, reply{http.StatusUnauthorized, "unauthorized"})
	if _, code = runBadil(t, "list", "--server", url); code == 0 {
		t.Error("exit code = 0 on a 401, want an error")
	}
}
