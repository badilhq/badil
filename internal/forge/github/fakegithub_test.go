package github_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/fakegithub"
	"github.com/badilhq/badil/internal/forge"
	forgegithub "github.com/badilhq/badil/internal/forge/github"
	"github.com/badilhq/badil/internal/github"
)

const (
	mockWebURL       = "http://git.local:3000"
	mockSecret       = "mock-secret"
	mockInstallation = 7
)

// mockEnv is badil's GitHub forge on the mock: the forge built through
// github.App.Client against the mock's API base, the App, and the mock, whose
// webhooks reach a receiver running the forge's ParseWebhook.
type mockEnv struct {
	forge *forgegithub.Forge
	app   *github.App
	mock  *httptest.Server

	mu     sync.Mutex
	events []forge.Event
}

// newMockEnv starts the mock, signing webhooks with secret, and badil's
// forge against it, validating webhooks with mockSecret.
func newMockEnv(t *testing.T, secret string) *mockEnv {
	t.Helper()
	env := &mockEnv{}
	receiver := httptest.NewServer(http.HandlerFunc(env.receive))
	t.Cleanup(receiver.Close)
	env.mock = httptest.NewServer(fakegithub.New(fakegithub.Config{
		WebURL:         mockWebURL,
		WebhookURL:     receiver.URL + "/webhooks/github",
		WebhookSecret:  secret,
		InstallationID: mockInstallation,
		AppSlug:        "badil-mock",
		BotUserID:      1,
	}).Handler())
	t.Cleanup(env.mock.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	env.app, err = github.NewApp(github.AppConfig{
		AppID:         1,
		PrivateKey:    pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		WebhookSecret: mockSecret,
		APIURL:        env.mock.URL + "/api/v3/",
	})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	env.forge, err = forgegithub.New(
		env.app.Client(env.app.InstallationTransport(mockInstallation)), env.app, mockInstallation, mockWebURL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return env
}

// receive answers a webhook as badil's server does: 401 for a rejected
// signature, 403 for another installation, else 200, recording the event.
func (e *mockEnv) receive(w http.ResponseWriter, r *http.Request) {
	ev, err := e.forge.ParseWebhook(r)
	switch {
	case errors.Is(err, forge.ErrUnverified):
		w.WriteHeader(http.StatusUnauthorized)
		return
	case errors.Is(err, forge.ErrForbidden):
		w.WriteHeader(http.StatusForbidden)
		return
	}
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
}

// lastEvent returns the last event received.
func (e *mockEnv) lastEvent(t *testing.T) forge.Event {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.events) == 0 {
		t.Fatal("no webhook received")
	}
	return e.events[len(e.events)-1]
}

// controlResult is a control call's answer.
type controlResult struct {
	PullRequest *gh.PullRequest `json:"pull_request"`
	Webhook     struct {
		Event  string `json:"event"`
		Action string `json:"action"`
		Status int    `json:"status"`
		Error  string `json:"error"`
	} `json:"webhook"`
}

// control calls the mock's control API and decodes the answer into out,
// failing t unless the status is want.
func (e *mockEnv) control(t *testing.T, method, path, body string, want int, out any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, e.mock.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.mock.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, body %s; want %d", method, path, resp.StatusCode, b, want)
	}
	if out != nil {
		if err = json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: body %s: %v", method, path, b, err)
		}
	}
}

// stats returns the mock's API calls per route.
func (e *mockEnv) stats(t *testing.T) map[string]int {
	t.Helper()
	var stats map[string]int
	e.control(t, http.MethodGet, "/_mock/stats", "", http.StatusOK, &stats)
	return stats
}

// filesJSON returns n changed files f0..f<n-1>, the first renamed from old0.
func filesJSON(n int) string {
	files := make([]string, n)
	for i := range files {
		files[i] = fmt.Sprintf(`{"filename":"f%d"}`, i)
	}
	files[0] = `{"filename":"f0","status":"renamed","previous_filename":"old0"}`
	return "[" + strings.Join(files, ",") + "]"
}

func TestForgeOnMock_PullRequest(t *testing.T) {
	env := newMockEnv(t, mockSecret)
	ctx := t.Context()
	env.control(t, http.MethodPost, "/_mock/repos/acme/web/pulls",
		`{"number":5,"head_ref":"feat/x","head_sha":"sha1","author":"octocat","labels":["bug"],"fork":true,`+
			`"files":`+filesJSON(150)+`}`, http.StatusCreated, nil)

	pr, err := env.forge.GetPullRequest(ctx, "acme", "web", 5)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	want := forge.PullRequest{
		Open: true, HeadRef: "feat/x", HeadSHA: "sha1", Author: "octocat",
		URL: mockWebURL + "/acme/web/pull/5", Labels: []string{"bug"}, FullName: "acme/web",
		HeadFullName: "mock-fork/web", Number: 5, ChangedFiles: 150,
	}
	if !reflect.DeepEqual(*pr, want) {
		t.Errorf("GetPullRequest = %+v, want %+v", *pr, want)
	}

	files, err := env.forge.ListChangedFiles(ctx, "acme", "web", 5)
	if err != nil {
		t.Fatalf("ListChangedFiles: %v", err)
	}
	if len(files) != 150 || files[0] != (forge.ChangedFile{Filename: "f0", PreviousFilename: "old0"}) ||
		files[149] != (forge.ChangedFile{Filename: "f149"}) {
		t.Errorf("ListChangedFiles = %d files, first %+v; want 150, f0 renamed from old0", len(files), files[0])
	}
	if got := env.stats(t)["GET /repos/{owner}/{repo}/pulls/{number}/files"]; got != 2 {
		t.Errorf("files pages read = %d, want 2", got)
	}
}

func TestForgeOnMock_ListOpenPullRequests(t *testing.T) {
	env := newMockEnv(t, mockSecret)
	for n := 1; n <= 105; n++ {
		env.control(t, http.MethodPost, "/_mock/repos/acme/web/pulls",
			fmt.Sprintf(`{"head_ref":"b%d","head_sha":"s%d"}`, n, n), http.StatusCreated, nil)
	}
	env.control(t, http.MethodPost, "/_mock/repos/acme/web/pulls/2/close", `{}`, http.StatusOK, nil)
	env.control(t, http.MethodPost, "/_mock/repos/acme/web/pulls/104/close", `{"merged":true}`, http.StatusOK, nil)

	prs, err := env.forge.ListOpenPullRequests(t.Context(), "acme", "web")
	if err != nil {
		t.Fatalf("ListOpenPullRequests: %v", err)
	}
	if len(prs) != 103 {
		t.Fatalf("ListOpenPullRequests = %d PRs, want 103", len(prs))
	}
	for _, pr := range prs {
		if pr.Number == 2 || pr.Number == 104 || !pr.Open {
			t.Errorf("listed PR %d (open %v), want only open PRs", pr.Number, pr.Open)
		}
	}
	if prs[0].Number != 1 || prs[102].Number != 105 || prs[102].HeadSHA != "s105" {
		t.Errorf("first, last = %+v, %+v; want 1 and 105", prs[0], prs[102])
	}
	if got := env.stats(t)["GET /repos/{owner}/{repo}/pulls"]; got != 2 {
		t.Errorf("list pages read = %d, want 2", got)
	}
}

func TestForgeOnMock_UpsertComment(t *testing.T) {
	env := newMockEnv(t, mockSecret)
	ctx := t.Context()
	env.control(t, http.MethodPost, "/_mock/repos/acme/web/pulls", `{"number":1,"head_ref":"f","head_sha":"s"}`,
		http.StatusCreated, nil)
	const comments = "/_mock/repos/acme/web/pulls/1/comments"

	if err := env.forge.UpsertComment(ctx, "acme", "web", 1, "first"); err != nil {
		t.Fatalf("UpsertComment create: %v", err)
	}
	var got []gh.IssueComment
	env.control(t, http.MethodGet, comments, "", http.StatusOK, &got)
	if len(got) != 1 || !strings.HasSuffix(got[0].GetBody(), "\nfirst") ||
		got[0].GetUser().GetLogin() != "badil-mock[bot]" {
		t.Fatalf("comments after create = %+v, want badil's comment ending in first", got)
	}
	id := got[0].GetID()

	if err := env.forge.UpsertComment(ctx, "acme", "web", 1, "second"); err != nil {
		t.Fatalf("UpsertComment edit: %v", err)
	}
	env.control(t, http.MethodGet, comments, "", http.StatusOK, &got)
	if len(got) != 1 || got[0].GetID() != id || !strings.HasSuffix(got[0].GetBody(), "\nsecond") ||
		!strings.HasPrefix(got[0].GetBody(), "<!-- badil:preview -->") {
		t.Fatalf("comments after edit = %+v, want comment %d edited to second", got, id)
	}

	stats := env.stats(t)
	if len(stats) == 0 {
		t.Errorf("stats before reset = none, want the calls of UpsertComment")
	}

	env.control(t, http.MethodPost, "/_mock/reset", "", http.StatusNoContent, nil)
	if stats = env.stats(t); len(stats) != 0 {
		t.Errorf("stats after reset = %v, want none", stats)
	}
	env.control(t, http.MethodGet, comments, "", http.StatusNotFound, nil)
}

func TestForgeOnMock_ListInstallations(t *testing.T) {
	env := newMockEnv(t, mockSecret)
	insts, _, err := env.app.Client(env.app.AppsTransport()).Apps.ListInstallations(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	if len(insts) != 1 || insts[0].GetID() != mockInstallation || insts[0].GetAccount().GetLogin() != "badil" ||
		insts[0].GetAccount().GetType() != "Organization" {
		t.Errorf("installations = %+v, want installation %d of badil", insts, mockInstallation)
	}
}

func TestForgeOnMock_Webhooks(t *testing.T) {
	env := newMockEnv(t, mockSecret)
	const pull = "/_mock/repos/acme/web/pulls/4"
	prEvent := func(action, label, sha string, labels ...string) forge.Event {
		return forge.Event{Name: "pull_request", Kind: forge.KindPullRequest, PullRequest: forge.PullRequestEvent{
			Owner: "acme", Repo: "web", FullName: "acme/web", Number: 4, Action: action, Label: label,
			Labels: append([]string{}, labels...), HeadRef: "feat/x", HeadSHA: sha, Author: "octocat",
		}}
	}
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		want                     forge.Event
	}{
		{"open", http.MethodPost, "/_mock/repos/acme/web/pulls",
			`{"number":4,"head_ref":"feat/x","head_sha":"sha1","author":"octocat"}`, http.StatusCreated,
			prEvent("opened", "", "sha1")},
		{"label", http.MethodPost, pull + "/labels", `{"name":"preview"}`, http.StatusOK,
			prEvent("labeled", "preview", "sha1", "preview")},
		{"push", http.MethodPost, pull + "/push", `{"head_sha":"sha2"}`, http.StatusOK,
			prEvent("synchronize", "", "sha2", "preview")},
		{"unlabel", http.MethodDelete, pull + "/labels/preview", ``, http.StatusOK,
			prEvent("unlabeled", "preview", "sha2")},
		{"close", http.MethodPost, pull + "/close", `{"merged":true}`, http.StatusOK,
			prEvent("closed", "", "sha2")},
		{"reopen", http.MethodPost, pull + "/reopen", ``, http.StatusOK,
			prEvent("reopened", "", "sha2")},
		{"gitops push", http.MethodPost, "/_mock/repos/acme/gitops/push", `{"after":"sha9"}`, http.StatusOK,
			forge.Event{Name: "push", Kind: forge.KindPush, Push: forge.PushEvent{
				FullName: "acme/gitops", Ref: "refs/heads/main", DefaultBranch: "main", After: "sha9",
			}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var res controlResult
			env.control(t, tc.method, tc.path, tc.body, tc.status, &res)
			if res.Webhook.Status != http.StatusOK || res.Webhook.Error != "" {
				t.Fatalf("webhook = %+v, want status 200", res.Webhook)
			}
			if got := env.lastEvent(t); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("event = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestForgeOnMock_WrongSecret(t *testing.T) {
	env := newMockEnv(t, "another-secret")
	var res controlResult
	env.control(t, http.MethodPost, "/_mock/repos/acme/web/pulls", `{"head_ref":"f","head_sha":"s"}`,
		http.StatusCreated, &res)
	if res.Webhook.Status != http.StatusUnauthorized || res.Webhook.Event != "pull_request" ||
		res.Webhook.Action != "opened" {
		t.Errorf("webhook = %+v, want pull_request opened answered 401", res.Webhook)
	}
}
