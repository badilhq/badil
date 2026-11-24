package github_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"

	"github.com/badilhq/badil/internal/github"
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
	creds := github.GitCredentials(installationTransport(t, &mints))
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
			user, password, err := github.GitopsCredentials(
				tc.username, tc.token, installationTransport(t, &mints),
			)(context.Background())
			if err != nil || user != tc.wantUser || password != tc.wantPass {
				t.Errorf("credentials = %q, %q, %v; want %q, %q", user, password, err, tc.wantUser, tc.wantPass)
			}
			if n := mints.Load(); n != tc.wantMints {
				t.Errorf("access token requests = %d, want %d", n, tc.wantMints)
			}
		})
	}
}
