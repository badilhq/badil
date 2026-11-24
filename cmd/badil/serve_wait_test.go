package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/server"
	"github.com/badilhq/badil/internal/status"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

const serveReadToken = "read-token"

// waitAnswer is the answer of a wait request.
type waitAnswer struct {
	code  int
	state string
	err   error
}

// served is the read API serveWaits serves: the HTTP server, the badil
// server, the gitops store and remote, and the base URL.
type served struct {
	httpSrv *http.Server
	srv     *server.Server
	st      *store.GitHubStore
	remote  *storetest.Remote
	base    string
}

// serveWaits serves, as serve wires them, the read API over a gitops repo
// holding feature-x, pinned by acme/backend-1#42 with no status yet.
func serveWaits(t *testing.T) served {
	t.Helper()
	previews := map[string]*preview.Values{"feature-x": {Services: []preview.ServiceEntry{{
		Name: "backend-1", CommitSha: "abc1234",
		Metadata: &preview.Metadata{PRURL: "https://github.com/acme/backend-1/pull/42"},
	}}}}
	previewStatus := status.NewMemory()
	wake := server.NewWake()
	remote, st := storetest.Gitops(t, "services:\n  backend-1:\n    repo: https://github.com/acme/backend-1.git\n",
		previews, nil, nil, statusHooks(previewStatus, wake)...)
	srv := server.NewServer(&loopFake{}, st, "",
		server.WithReadToken(serveReadToken), server.WithStatus("", previewStatus), server.WithWake(wake))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := newHTTPServer(ln.Addr().String(), srv)
	go func() { _ = httpSrv.Serve(ln) }()
	t.Cleanup(func() { _ = httpSrv.Close() })
	return served{httpSrv: httpSrv, srv: srv, st: st, remote: remote, base: "http://" + ln.Addr().String()}
}

// startWaitRequest sends GET path to base with the read token in the
// background, and returns the channel its answer is sent on.
func startWaitRequest(base, path string) <-chan waitAnswer {
	out := make(chan waitAnswer, 1)
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, http.NoBody)
		req.Header.Set("Authorization", "Bearer "+serveReadToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- waitAnswer{err: err}
			return
		}
		defer resp.Body.Close()
		var body server.WaitResponse
		err = json.NewDecoder(resp.Body).Decode(&body)
		out <- waitAnswer{code: resp.StatusCode, state: body.State, err: err}
	}()
	return out
}

// awaitAnswer returns the answer sent on ch, failing t unless it comes
// within 2 s.
func awaitAnswer(t *testing.T, ch <-chan waitAnswer) waitAnswer {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(2 * time.Second):
		t.Fatal("no answer within 2s")
	}
	return waitAnswer{}
}

// assertWaiting fails t if ch gets an answer within 100 ms.
func assertWaiting(t *testing.T, ch <-chan waitAnswer) {
	t.Helper()
	select {
	case a := <-ch:
		t.Fatalf("answered %+v, want a blocked wait", a)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestServe_ShutdownEndsWaits checks that a wait blocked in progressing
// answers its state as soon as serve's shutdown starts, that the shutdown
// returns without hitting its deadline, and that ending the waits again
// does not panic.
func TestServe_ShutdownEndsWaits(t *testing.T) {
	sv := serveWaits(t)
	ch := startWaitRequest(sv.base, "/v1/previews/feature-x/wait")
	assertWaiting(t, ch)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	start := time.Now()
	if err := shutdownServers(ctx, sv.httpSrv); err != nil {
		t.Fatalf("shutdownServers: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("shutdownServers took %s, want under 2s", elapsed)
	}
	if a := awaitAnswer(t, ch); a.err != nil || a.code != http.StatusOK || a.state != "progressing" {
		t.Errorf("wait answered %+v, want 200 progressing", a)
	}
	sv.srv.EndWaits()
}

// TestServe_AppliedWakesWaits checks that serve's WithApplied callback wakes
// the waits: an Apply deleting the preview ends its wait with 404.
func TestServe_AppliedWakesWaits(t *testing.T) {
	sv := serveWaits(t)
	ch := startWaitRequest(sv.base, "/v1/previews/feature-x/wait")
	assertWaiting(t, ch)

	if _, err := sv.st.Apply(
		context.Background(),
		store.Op{Kind: store.OpSweep, Change: func(tree lifecycle.Tree) (store.Summary, error) {
			tree.DeletePreview("feature-x")
			return store.Summary{Subject: "chore(preview): sweep"}, nil
		}},
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a := awaitAnswer(t, ch); a.code != http.StatusNotFound {
		t.Errorf("wait answered %+v, want 404", a)
	}
}

// TestServe_HeadMovingFetchWakesWaits checks that serve's WithHeadMoved
// callback wakes the waits: a fetch moving the head to a commit that deletes
// the preview ends its wait with 404.
func TestServe_HeadMovingFetchWakesWaits(t *testing.T) {
	sv := serveWaits(t)
	ch := startWaitRequest(sv.base, "/v1/previews/feature-x/wait")
	assertWaiting(t, ch)

	sv.remote.Set("previews/feature-x/values.yaml", "")
	sv.st.Invalidate()
	if _, err := sv.st.ListPreviewFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := awaitAnswer(t, ch); a.code != http.StatusNotFound {
		t.Errorf("wait answered %+v, want 404", a)
	}
}
