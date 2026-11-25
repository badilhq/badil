package fakegithub_test

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/fakegithub"
)

// newMock serves a mock of installation 7 configured by cfg, sending
// webhooks to webhookURL.
func newMock(t *testing.T, cfg fakegithub.Config) *httptest.Server {
	t.Helper()
	cfg.WebURL = "http://git.local:3000"
	cfg.InstallationID = 7
	cfg.AppSlug = "badil-mock"
	cfg.BotUserID = 1
	cfg.WebhookSecret = "s3cret"
	if cfg.WebhookURL == "" {
		cfg.WebhookURL = "http://127.0.0.1:1/webhooks/github"
	}
	srv := httptest.NewServer(fakegithub.New(cfg).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// do sends method path with auth (none when empty) and body, and returns the
// status and the body.
func do(t *testing.T, srv *httptest.Server, method, path, auth, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// issueToken returns an installation token of installation 7.
func issueToken(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	status, body := do(t, srv, http.MethodPost, "/api/v3/app/installations/7/access_tokens", "Bearer jwt", "")
	if status != http.StatusCreated {
		t.Fatalf("token: status %d, body %s", status, body)
	}
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(body), &tok); err != nil || tok.Token == "" || tok.ExpiresAt.IsZero() {
		t.Fatalf("token body %s: %v", body, err)
	}
	return tok.Token
}

const badCredentials = `{"message":"Bad credentials"}`

func TestTokenRules(t *testing.T) {
	srv := newMock(t, fakegithub.Config{})
	tok := issueToken(t, srv)

	for _, tc := range []struct{ name, auth string }{
		{"no token", ""},
		{"unknown token", "token ghs_unknown"},
		{"jwt", "Bearer jwt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, srv, http.MethodGet, "/api/v3/users/badil-mock[bot]", tc.auth, "")
			if status != http.StatusUnauthorized || strings.TrimSpace(body) != badCredentials {
				t.Errorf("status %d, body %s; want 401 %s", status, body, badCredentials)
			}
		})
	}

	for _, auth := range []string{"token " + tok, "Bearer " + tok} {
		status, body := do(t, srv, http.MethodGet, "/api/v3/users/badil-mock[bot]", auth, "")
		want := `{"login":"badil-mock[bot]","id":1,"type":"Bot"}`
		if status != http.StatusOK || strings.TrimSpace(body) != want {
			t.Errorf("%s: status %d, body %s; want 200 %s", auth[:5], status, body, want)
		}
	}
}

func TestTokenEndpointRules(t *testing.T) {
	srv := newMock(t, fakegithub.Config{})
	status, body := do(t, srv, http.MethodPost, "/api/v3/app/installations/8/access_tokens", "Bearer jwt", "")
	if status != http.StatusNotFound {
		t.Errorf("token of another installation: status %d, body %s; want 404", status, body)
	}
	for _, path := range []string{"/api/v3/app", "/api/v3/app/installations"} {
		if status, _ = do(t, srv, http.MethodGet, path, "", ""); status != http.StatusUnauthorized {
			t.Errorf("GET %s without auth: status %d, want 401", path, status)
		}
	}
	if status, _ = do(
		t,
		srv,
		http.MethodPost,
		"/api/v3/app/installations/7/access_tokens",
		"",
		"",
	); status != http.StatusUnauthorized {
		t.Errorf("token without auth: status %d, want 401", status)
	}
}

func TestExpiredToken(t *testing.T) {
	srv := newMock(t, fakegithub.Config{TokenTTL: time.Millisecond})
	expired := issueToken(t, srv)
	time.Sleep(10 * time.Millisecond)
	status, body := do(t, srv, http.MethodGet, "/api/v3/users/badil-mock[bot]", "token "+expired, "")
	if status != http.StatusUnauthorized || strings.TrimSpace(body) != badCredentials {
		t.Errorf("status %d, body %s; want 401 %s", status, body, badCredentials)
	}
}

func TestAppShapes(t *testing.T) {
	srv := newMock(t, fakegithub.Config{})
	status, body := do(t, srv, http.MethodGet, "/api/v3/app", "Bearer jwt", "")
	want := `{"id":1,"slug":"badil-mock","owner":{"login":"badil"},"name":"badil-mock",` +
		`"html_url":"http://git.local:3000/apps/badil-mock"}`
	if status != http.StatusOK || strings.TrimSpace(body) != want {
		t.Errorf("GET /app: status %d, body %s; want 200 %s", status, body, want)
	}
	tok := issueToken(t, srv)
	status, body = do(t, srv, http.MethodGet, "/api/v3/users/octocat", "token "+tok, "")
	if status != http.StatusNotFound {
		t.Errorf("GET /users/octocat: status %d, body %s; want 404", status, body)
	}
}

func TestUnknownRoutes(t *testing.T) {
	srv := newMock(t, fakegithub.Config{})
	tok := issueToken(t, srv)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v3/repos/acme/web/deployments"},
		{http.MethodDelete, "/api/v3/repos/acme/web/pulls/1"},
		{http.MethodGet, "/api/v3/repos/acme/web/pulls/1"},
		{http.MethodGet, "/_mock/nothing"},
	} {
		status, body := do(t, srv, tc.method, tc.path, "token "+tok, "")
		if status != http.StatusNotFound || strings.TrimSpace(body) != `{"message":"Not Found"}` {
			t.Errorf("%s %s: status %d, body %s; want 404 Not Found", tc.method, tc.path, status, body)
		}
	}
	_, body := do(t, srv, http.MethodGet, "/_mock/stats", "", "")
	var stats map[string]int
	if err := json.Unmarshal([]byte(body), &stats); err != nil {
		t.Fatalf("stats %s: %v", body, err)
	}
	want := map[string]int{
		"POST /app/installations/{id}/access_tokens": 1,
		"GET /repos/acme/web/deployments":            1,
		"DELETE /repos/acme/web/pulls/1":             1,
		"GET /repos/{owner}/{repo}/pulls/{number}":   1,
	}
	if !maps.Equal(stats, want) {
		t.Errorf("stats = %v, want %v", stats, want)
	}
}

func TestControlErrors(t *testing.T) {
	srv := newMock(t, fakegithub.Config{})
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"malformed body", http.MethodPost, "/_mock/repos/acme/web/pulls", `{"head_ref":`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, "/_mock/repos/acme/web/pulls", `{"head":"x"}`, http.StatusBadRequest},
		{"missing head", http.MethodPost, "/_mock/repos/acme/web/pulls", `{"head_ref":"f"}`, http.StatusBadRequest},
		{"unknown pr label", http.MethodPost, "/_mock/repos/acme/web/pulls/9/labels", `{"name":"preview"}`,
			http.StatusNotFound},
		{"unknown pr close", http.MethodPost, "/_mock/repos/acme/web/pulls/9/close", ``, http.StatusNotFound},
		{"unknown pr comments", http.MethodGet, "/_mock/repos/acme/web/pulls/9/comments", ``, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, body := do(t, srv, tc.method, tc.path, "", tc.body); status != tc.want {
				t.Errorf("status %d, body %s; want %d", status, body, tc.want)
			}
		})
	}

	t.Run("badil unreachable", func(t *testing.T) {
		status, body := do(t, srv, http.MethodPost, "/_mock/repos/acme/web/pulls", "",
			`{"number":3,"head_ref":"f","head_sha":"abc"}`)
		var out struct {
			PullRequest struct {
				Number int `json:"number"`
			} `json:"pull_request"`
			Webhook struct {
				Event  string `json:"event"`
				Status int    `json:"status"`
				Error  string `json:"error"`
			} `json:"webhook"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if status != http.StatusCreated || out.PullRequest.Number != 3 || out.Webhook.Event != "pull_request" ||
			out.Webhook.Status != 0 || out.Webhook.Error == "" {
			t.Errorf("status %d, body %s; want 201, PR 3 and a webhook error", status, body)
		}
		if again, _ := do(t, srv, http.MethodPost, "/_mock/repos/acme/web/pulls", "",
			`{"number":3,"head_ref":"f","head_sha":"abc"}`); again != http.StatusConflict {
			t.Errorf("opening PR 3 again: status %d, want 409", again)
		}
		if missing, _ := do(t, srv, http.MethodDelete, "/_mock/repos/acme/web/pulls/3/labels/none", "",
			""); missing != http.StatusNotFound {
			t.Errorf("removing a missing label: status %d, want 404", missing)
		}
	})
}

func TestLabeledEvents(t *testing.T) {
	srv := newMock(t, fakegithub.Config{})
	tok := "token " + issueToken(t, srv)
	do(t, srv, http.MethodPost, "/_mock/repos/acme/web/pulls", "",
		`{"number":1,"head_ref":"f","head_sha":"abc","author":"octocat","labels":["bug","preview"]}`)
	do(
		t,
		srv,
		http.MethodPost,
		"/_mock/repos/acme/web/pulls/1/labels",
		"",
		`{"name":"preview:demo","sender":"triager"}`,
	)
	do(t, srv, http.MethodPost, "/_mock/repos/acme/web/pulls/1/labels", "", `{"name":"preview:demo","sender":"other"}`)
	do(t, srv, http.MethodPost, "/_mock/repos/acme/web/pulls/1/labels", "", `{"name":"docs"}`)

	status, body := do(t, srv, http.MethodGet, "/api/v3/repos/acme/web/issues/1/events", tok, "")
	want := `[{"actor":{"login":"octocat"},"event":"labeled","label":{"name":"bug"}},` +
		`{"actor":{"login":"octocat"},"event":"labeled","label":{"name":"preview"}},` +
		`{"actor":{"login":"triager"},"event":"labeled","label":{"name":"preview:demo"}},` +
		`{"actor":{"login":"octocat"},"event":"labeled","label":{"name":"docs"}}]`
	if status != http.StatusOK || strings.TrimSpace(body) != want {
		t.Errorf("events: status %d, body %s; want 200 %s", status, body, want)
	}
	if status, _ = do(
		t,
		srv,
		http.MethodGet,
		"/api/v3/repos/acme/web/issues/9/events",
		tok,
		"",
	); status != http.StatusNotFound {
		t.Errorf("events of an unknown PR: status %d, want 404", status)
	}

	do(t, srv, http.MethodPost, "/_mock/reset", "", "")
	do(t, srv, http.MethodPost, "/_mock/repos/acme/web/pulls", "", `{"number":1,"head_ref":"f","head_sha":"abc"}`)
	if status, body = do(
		t,
		srv,
		http.MethodGet,
		"/api/v3/repos/acme/web/issues/1/events",
		tok,
		"",
	); strings.TrimSpace(
		body,
	) != "[]" {
		t.Errorf("events after reset: status %d, body %s; want []", status, body)
	}
}

func TestPermission(t *testing.T) {
	srv := newMock(t, fakegithub.Config{})
	tok := "token " + issueToken(t, srv)
	read := func(login string) (string, string, bool) {
		t.Helper()
		status, body := do(t, srv, http.MethodGet, "/api/v3/repos/acme/web/collaborators/"+login+"/permission", tok, "")
		var out struct {
			Permission string `json:"permission"`
			RoleName   string `json:"role_name"`
			User       struct {
				Login       string          `json:"login"`
				Permissions map[string]bool `json:"permissions"`
			} `json:"user"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil || status != http.StatusOK || out.User.Login != login {
			t.Fatalf("permission of %s: status %d, body %s: %v", login, status, body, err)
		}
		return out.Permission, out.RoleName, out.User.Permissions["push"]
	}

	if p, r, push := read("anyone"); p != "write" || r != "write" || !push {
		t.Errorf("default = %q, %q, push %v; want write, write, true", p, r, push)
	}
	for _, tc := range []struct {
		role, permission, roleName string
		push                       bool
	}{
		{"admin", "admin", "admin", true},
		{"maintain", "write", "maintain", true},
		{"write", "write", "write", true},
		{"triage", "read", "triage", false},
		{"read", "read", "read", false},
		{"none", "none", "", false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			status, body := do(t, srv, http.MethodPut, "/_mock/repos/acme/web/collaborators/Alice/permission", "",
				`{"permission":"`+tc.role+`"}`)
			if status != http.StatusOK {
				t.Fatalf("set %s: status %d, body %s", tc.role, status, body)
			}
			if p, r, push := read("alice"); p != tc.permission || r != tc.roleName || push != tc.push {
				t.Errorf("read = %q, %q, push %v; want %q, %q, %v", p, r, push, tc.permission, tc.roleName, tc.push)
			}
		})
	}
	if p, _, _ := read("bob"); p != "write" {
		t.Errorf("another login = %q, want write", p)
	}
	if status, _ := do(t, srv, http.MethodPut, "/_mock/repos/acme/web/collaborators/alice/permission", "",
		`{"permission":"owner"}`); status != http.StatusBadRequest {
		t.Errorf("unknown role: status %d, want 400", status)
	}
	do(t, srv, http.MethodPost, "/_mock/reset", "", "")
	if p, _, _ := read("alice"); p != "write" {
		t.Errorf("after reset = %q, want write", p)
	}
}
