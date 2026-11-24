package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/config"
	"github.com/badilhq/badil/internal/fakegithub"
	forgegithub "github.com/badilhq/badil/internal/forge/github"
	"github.com/badilhq/badil/internal/github"
	"github.com/badilhq/badil/internal/store"
)

// installationTransport returns the transport of installation 67890 of a
// GitHub App whose API is srv, counting each installation token srv mints in
// mints.
func installationTransport(t *testing.T, mints *atomic.Int32) *ghinstallation.Transport {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/67890/access_tokens" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		mints.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_installation",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	atr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, 12345, pemKey)
	if err != nil {
		t.Fatal(err)
	}
	app, err := github.NewAppFromTransport(atr, "secret")
	if err != nil {
		t.Fatal(err)
	}
	itr := app.InstallationTransport(67890)
	itr.BaseURL = srv.URL
	return itr
}

// TestGitCredentials_OneTokenPerValidity checks that many git credential
// calls in a row, as before each ls-remote and fetch, make one installation
// token request while the token is valid.
func TestGitCredentials_OneTokenPerValidity(t *testing.T) {
	var mints atomic.Int32
	creds := gitCredentials(installationTransport(t, &mints))
	for range 20 {
		user, password, credErr := creds(context.Background())
		if credErr != nil || user != "x-access-token" || password != "ghs_installation" {
			t.Fatalf("credentials = %q, %q, %v; want x-access-token and the installation token",
				user, password, credErr)
		}
	}
	if n := mints.Load(); n != 1 {
		t.Errorf("access token requests = %d, want 1", n)
	}
}

// TestGitopsCredentials checks GITOPS_REPO_TOKEN, with the default or the
// configured username, replaces the installation token.
func TestGitopsCredentials(t *testing.T) {
	cases := []struct {
		name, username, token string
		wantUser, wantPass    string
		wantMints             int32
	}{
		{name: "token", token: "glpat-secret", wantUser: "x-access-token", wantPass: "glpat-secret"},
		{
			name: "token and username", username: "deploy-user", token: "glpat-secret",
			wantUser: "deploy-user", wantPass: "glpat-secret",
		},
		{name: "no token", wantUser: "x-access-token", wantPass: "ghs_installation", wantMints: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mints atomic.Int32
			cfg := &config.Config{GitOpsRepoUsername: tc.username, GitOpsRepoToken: tc.token}
			user, password, err := gitopsCredentials(cfg, installationTransport(t, &mints))(context.Background())
			if err != nil || user != tc.wantUser || password != tc.wantPass {
				t.Errorf("credentials = %q, %q, %v; want %q, %q", user, password, err, tc.wantUser, tc.wantPass)
			}
			if n := mints.Load(); n != tc.wantMints {
				t.Errorf("access token requests = %d, want %d", n, tc.wantMints)
			}
		})
	}
}

// TestCommitIdentity checks the identity is the App's bot, read with the App
// JWT client for GET /app and the installation client for the bot user, and
// that a failed lookup is an error.
func TestCommitIdentity(t *testing.T) {
	var appAuth, userAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app":
			appAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "slug": "badil-dev"})
		case "/users/badil-dev[bot]":
			userAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 348525889, "login": "badil-dev[bot]"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	client := func(token string) *gh.Client {
		c := gh.NewClient(nil).WithAuthToken(token)
		u, err := url.Parse(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		c.BaseURL = u
		return c
	}

	id, err := commitIdentity(context.Background(), client("app-jwt"), client("installation"), "https://github.com")
	if err != nil {
		t.Fatalf("commitIdentity: %v", err)
	}
	want := store.CommitIdentity{
		Name:  "badil-dev[bot]",
		Email: "348525889+badil-dev[bot]@users.noreply.github.com",
	}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
	if appAuth != "Bearer app-jwt" || userAuth != "Bearer installation" {
		t.Errorf("auth = %q, %q; want the App JWT for /app and the installation for the user", appAuth, userAuth)
	}

	id, err = commitIdentity(context.Background(), client("app-jwt"), client("installation"), "http://gitea.local:3000")
	if err != nil {
		t.Fatalf("commitIdentity on gitea.local: %v", err)
	}
	if want := "348525889+badil-dev[bot]@users.noreply.gitea.local"; id.Email != want {
		t.Errorf("email on GITHUB_URL http://gitea.local:3000 = %q, want %q", id.Email, want)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err = commitIdentity(
		context.Background(), client("app-jwt"), client("installation"), "https://github.com",
	); err == nil {
		t.Error("commitIdentity with GET /app failing = nil error, want one")
	}
}

// TestCommitIdentity_Mock checks the identity against the mock GitHub server,
// with the App and installation clients on its API base: the mock App's bot
// and an email on the web URL's host name, without its port.
func TestCommitIdentity_Mock(t *testing.T) {
	const webURL = "http://git.local:3000"
	mock := httptest.NewServer(fakegithub.New(fakegithub.Config{
		WebURL: webURL, WebhookURL: "http://127.0.0.1:1/webhooks/github", WebhookSecret: "s",
		InstallationID: 1, AppSlug: "badil-mock", BotUserID: 1,
	}).Handler())
	t.Cleanup(mock.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := github.NewApp(github.AppConfig{
		AppID:      1,
		PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		APIURL:     mock.URL + "/api/v3/",
	})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	id, err := commitIdentity(
		t.Context(),
		app.Client(app.AppsTransport()),
		app.Client(app.InstallationTransport(1)),
		webURL,
	)
	if err != nil {
		t.Fatalf("commitIdentity: %v", err)
	}
	want := store.CommitIdentity{Name: "badil-mock[bot]", Email: "1+badil-mock[bot]@users.noreply.git.local"}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
}

// fakeGitHubAPI returns a GitHub API test server serving, under /api/v3/,
// installation 67890's token, GET /app of the App badil-dev, its bot user and
// acme/front#7, and the requests it received, as "<method> <path>".
func fakeGitHubAPI(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		switch r.URL.Path {
		case "/api/v3/app/installations/67890/access_tokens":
			body = map[string]any{
				"token":      "ghs_installation",
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			}
		case "/api/v3/app":
			body = map[string]any{"id": 12345, "slug": "badil-dev"}
		case "/api/v3/users/badil-dev[bot]":
			body = map[string]any{"id": 348525889, "login": "badil-dev[bot]"}
		case "/api/v3/repos/acme/front/pulls/7":
			body = map[string]any{"number": 7, "html_url": "http://gitea.local:3000/acme/front/pull/7"}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// mustNewForge returns the forge and the commit identity newForge builds from
// cfg with newApp's App, failing t on an error.
func mustNewForge(t *testing.T, cfg *config.Config) (*forgegithub.Forge, store.CommitIdentity) {
	t.Helper()
	app, err := newApp(cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	_, f, identity, err := newForge(context.Background(), app, cfg)
	if err != nil {
		t.Fatalf("newForge: %v", err)
	}
	return f, identity
}

// TestNewForge_CustomAPIURL checks that with GITHUB_API_URL on a test
// server, with or without its api/v3/ suffix, the installation token request,
// GET /app, GET /users/<slug>[bot] and a forge call all reach it under one
// path prefix, and that the forge and the bot email follow GITHUB_URL.
func TestNewForge_CustomAPIURL(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	want := []string{
		"GET /api/v3/app",
		"POST /api/v3/app/installations/67890/access_tokens",
		"GET /api/v3/users/badil-dev[bot]",
		"GET /api/v3/repos/acme/front/pulls/7",
	}
	for _, suffix := range []string{"/api/v3/", "/"} {
		t.Run(suffix, func(t *testing.T) {
			srv, requests := fakeGitHubAPI(t)
			cfg := &config.Config{
				AppID: 12345, PrivateKey: pemKey, InstallationID: 67890,
				GitHubURL: "http://gitea.local:3000", GitHubAPIURL: srv.URL + suffix,
			}
			ctx := context.Background()
			f, identity := mustNewForge(t, cfg)
			pr, prErr := f.GetPullRequest(ctx, "acme", "front", 7)
			if prErr != nil {
				t.Fatalf("GetPullRequest: %v", prErr)
			}
			if !slices.Equal(*requests, want) {
				t.Errorf("requests = %q, want %q", *requests, want)
			}
			if want := "348525889+badil-dev[bot]@users.noreply.gitea.local"; identity.Email != want {
				t.Errorf("bot email = %q, want %q", identity.Email, want)
			}
			if o, r, n, ok := f.ParsePRURL(pr.URL); !ok || o != "acme" || r != "front" || n != 7 {
				t.Errorf("ParsePRURL(%q) = %q, %q, %d, %v; want acme, front, 7, true", pr.URL, o, r, n, ok)
			}
		})
	}
}
