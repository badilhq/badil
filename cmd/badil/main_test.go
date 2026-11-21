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
	"sync/atomic"
	"testing"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/config"
	"github.com/badilhq/badil/internal/github"
	"github.com/badilhq/badil/internal/store"
)

func TestParseCleanupTTL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw     string
		want    time.Duration
		wantErr string
	}{
		{raw: "", want: 0},
		{raw: "168h", want: 168 * time.Hour},
		{raw: "0", wantErr: `CLEANUP_TTL must be a positive duration, got "0"`},
		{raw: "-1h", wantErr: `CLEANUP_TTL must be a positive duration, got "-1h"`},
		{raw: "abc", wantErr: `CLEANUP_TTL must be a positive duration, got "abc"`},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()

			got, err := parseCleanupTTL(tc.raw)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("parseCleanupTTL(%q) err = %v, want %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCleanupTTL(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("parseCleanupTTL(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

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

	id, err := commitIdentity(context.Background(), client("app-jwt"), client("installation"))
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

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err = commitIdentity(context.Background(), client("app-jwt"), client("installation")); err == nil {
		t.Error("commitIdentity with GET /app failing = nil error, want one")
	}
}
