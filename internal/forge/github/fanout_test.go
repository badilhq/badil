package github_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
)

func sampleInputs() preview.DeployInputs {
	return preview.DeployInputs{
		ServiceName:    "backend-1",
		HeadRef:        "feature/add-pricing",
		CommitSha:      "abc1234567890",
		PRAuthor:       "octocat",
		PRURL:          "https://github.com/acme/backend-1/pull/42",
		PRNumber:       "42",
		Timestamp:      "2026-05-26T10:00:00Z",
		WorkflowRunURL: "https://github.com/acme/backend-1/actions/runs/1",
	}
}

// fanOutGitHub answers PR state reads from states, keyed by "<repo>/pulls/<n>"
// (a missing key answers 500), lists no existing comment and records every
// comment write.
type fanOutGitHub struct {
	t      *testing.T
	states map[string]string
	mu     sync.Mutex
	writes []string
}

func (g *fanOutGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/repos/acme/")
	switch {
	case r.Method == http.MethodGet && strings.Contains(path, "/pulls/"):
		state, ok := g.states[path]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"boom"}`)
			return
		}
		_, _ = io.WriteString(w, `{"state":"`+state+`"}`)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/comments"):
		_, _ = io.WriteString(w, `[]`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/comments"):
		g.mu.Lock()
		g.writes = append(g.writes, path)
		g.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":1}`)
	default:
		g.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}
}

func pinnedBy(name, prURL string) preview.ServiceEntry {
	return preview.ServiceEntry{Name: name, CommitSha: "abc", Metadata: &preview.Metadata{PRURL: prURL}}
}

func TestFanOutPRComments_SkipsClosedPRs(t *testing.T) {
	g := &fanOutGitHub{t: t, states: map[string]string{
		"backend-1/pulls/42": "open",
		"backend-2/pulls/7":  "closed",
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()

	vals := preview.PreviewValues{Services: []preview.ServiceEntry{
		pinnedBy("backend-1", "https://github.com/acme/backend-1/pull/42"),
		pinnedBy("backend-2", "https://github.com/acme/backend-2/pull/7"),
	}}
	if err := lifecycle.FanOutPRComments(context.Background(), newForge(t, srv), vals, "## body"); err != nil {
		t.Fatalf("FanOutPRComments: %v", err)
	}
	if want := []string{"backend-1/issues/42/comments"}; !slices.Equal(g.writes, want) {
		t.Errorf("comment writes = %v, want %v", g.writes, want)
	}
}

func TestFanOutPRComments_StateError(t *testing.T) {
	g := &fanOutGitHub{t: t, states: map[string]string{
		"backend-1/pulls/42": "open",
		// front/pulls/9 answers 500
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()

	vals := preview.PreviewValues{Services: []preview.ServiceEntry{
		pinnedBy("backend-1", "https://github.com/acme/backend-1/pull/42"),
		pinnedBy("front", "https://github.com/acme/front/pull/9"),
	}}
	err := lifecycle.FanOutPRComments(context.Background(), newForge(t, srv), vals, "## body")
	if err == nil || !strings.Contains(err.Error(), "service front (acme/front#9): pr state: ") {
		t.Fatalf("err = %v, want a pr state error for front", err)
	}
	if want := []string{"backend-1/issues/42/comments"}; !slices.Equal(g.writes, want) {
		t.Errorf("comment writes = %v, want %v", g.writes, want)
	}
}

func TestFanOutPRComments_UpdateAndCreate(t *testing.T) {
	// Two services with PR URLs:
	//   backend-1: PR #42 → existing marker comment → expect PATCH
	//   backend-2: PR #7  → no existing comment    → expect POST
	var sawEdit, sawCreate atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/"):
			_, _ = io.WriteString(w, `{"state":"open"}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/42/comments"):
			_, _ = io.WriteString(w, `[{"id": 999, "body": "<!-- badil:preview -->\n old body"}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/7/comments"):
			_, _ = io.WriteString(w, `[{"id": 111, "body": "unrelated"}]`)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/issues/comments/999"):
			sawEdit.Store(true)
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "<!-- badil:preview -->") {
				t.Errorf("PATCH body missing marker: %s", body)
			}
			_, _ = io.WriteString(w, `{"id":999}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/7/comments"):
			sawCreate.Store(true)
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "<!-- badil:preview -->") {
				t.Errorf("POST body missing marker: %s", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":222}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	in1 := sampleInputs()
	in2 := sampleInputs()
	in2.ServiceName = "backend-2"
	in2.PRURL = "https://github.com/acme/backend-2/pull/7"
	in2.PRNumber = "7"

	vals := preview.PreviewValues{Services: []preview.ServiceEntry{
		{Name: "backend-1", CommitSha: "abc", Metadata: preview.BuildServiceMetadata(in1)},
		{Name: "backend-2", CommitSha: "def", Metadata: preview.BuildServiceMetadata(in2)},
	}}

	if err := lifecycle.FanOutPRComments(context.Background(), newForge(t, srv), vals, "## body"); err != nil {
		t.Fatalf("FanOutPRComments: %v", err)
	}
	if !sawEdit.Load() {
		t.Error("expected PATCH on existing comment for PR #42")
	}
	if !sawCreate.Load() {
		t.Error("expected POST for PR #7")
	}
}
