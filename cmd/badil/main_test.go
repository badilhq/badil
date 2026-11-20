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
	"sync/atomic"
	"testing"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"

	"github.com/badilhq/badil/internal/github"
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

// TestGitCredentials_OneTokenPerValidity checks that many git credential
// calls in a row, as before each ls-remote and fetch, make one installation
// token request while the token is valid.
func TestGitCredentials_OneTokenPerValidity(t *testing.T) {
	var mints atomic.Int32
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
	atr.BaseURL = srv.URL
	app, err := github.NewAppFromTransport(atr, "secret")
	if err != nil {
		t.Fatal(err)
	}

	creds := gitCredentials(app.InstallationTransport(67890))
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
