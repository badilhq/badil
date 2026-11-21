package github_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/github"
)

func generateTestKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	encodeErr := pem.Encode(&buf, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	return buf.Bytes()
}

func TestNewApp(t *testing.T) {
	t.Run("valid key", func(t *testing.T) {
		app, err := github.NewApp(github.AppConfig{
			AppID:         12345,
			PrivateKey:    generateTestKey(t),
			WebhookSecret: "test-secret",
		})
		if err != nil {
			t.Fatalf("NewApp() error = %v", err)
		}
		if app == nil {
			t.Fatal("NewApp() returned nil")
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		_, err := github.NewApp(github.AppConfig{
			AppID:         12345,
			PrivateKey:    []byte("not-a-pem-key"),
			WebhookSecret: "test-secret",
		})
		if err == nil {
			t.Fatal("NewApp() expected error for invalid key")
		}
	})
}

// TestNewApp_DefaultAPIURL checks that without a custom API base the
// transports and clients use go-github's and ghinstallation's defaults.
func TestNewApp_DefaultAPIURL(t *testing.T) {
	key := generateTestKey(t)
	for _, apiURL := range []string{"", github.DefaultAPIURL} {
		app, err := github.NewApp(github.AppConfig{AppID: 12345, PrivateKey: key, APIURL: apiURL})
		if err != nil {
			t.Fatalf("NewApp(APIURL %q): %v", apiURL, err)
		}
		if got := app.AppsTransport().BaseURL; got != "https://api.github.com" {
			t.Errorf("APIURL %q: apps transport BaseURL = %q, want https://api.github.com", apiURL, got)
		}
		if got := app.InstallationTransport(1).BaseURL; got != "https://api.github.com" {
			t.Errorf("APIURL %q: installation transport BaseURL = %q, want https://api.github.com", apiURL, got)
		}
		def := gh.NewClient(nil)
		c := app.Client(app.AppsTransport())
		if *c.BaseURL != *def.BaseURL || *c.UploadURL != *def.UploadURL {
			t.Errorf("APIURL %q: client bases = %s, %s; want %s, %s",
				apiURL, c.BaseURL, c.UploadURL, def.BaseURL, def.UploadURL)
		}
		nc, err := github.NewClient(apiURL)
		if err != nil || *nc.BaseURL != *def.BaseURL || *nc.UploadURL != *def.UploadURL {
			t.Errorf("NewClient(%q) = %v, %v; want the default client", apiURL, nc, err)
		}
	}
}

// TestNewApp_CustomAPIURL checks that the installation token request, GET
// /app and an installation call reach a custom API base under one path
// prefix, with or without its api/v3/ suffix.
func TestNewApp_CustomAPIURL(t *testing.T) {
	key := generateTestKey(t)
	for _, suffix := range []string{"/api/v3/", "/"} {
		t.Run(suffix, func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v3/app/installations/67890/access_tokens":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"token":      "ghs_installation",
						"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					})
				case "/api/v3/app":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 12345, "slug": "badil-dev"})
				case "/api/v3/repos/acme/front/pulls/7":
					_ = json.NewEncoder(w).Encode(map[string]any{"number": 7})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			app, err := github.NewApp(github.AppConfig{AppID: 12345, PrivateKey: key, APIURL: srv.URL + suffix})
			if err != nil {
				t.Fatalf("NewApp: %v", err)
			}
			ctx := context.Background()
			if _, _, err = app.Client(app.AppsTransport()).Apps.Get(ctx, ""); err != nil {
				t.Fatalf("GET /app: %v", err)
			}
			installClient := app.Client(app.InstallationTransport(67890))
			if _, _, err = installClient.PullRequests.Get(ctx, "acme", "front", 7); err != nil {
				t.Fatalf("GET pull: %v", err)
			}
			want := []string{
				"GET /api/v3/app",
				"POST /api/v3/app/installations/67890/access_tokens",
				"GET /api/v3/repos/acme/front/pulls/7",
			}
			if !slices.Equal(paths, want) {
				t.Errorf("requests = %q, want %q", paths, want)
			}
		})
	}
}

func TestNewClient_InvalidAPIURL(t *testing.T) {
	if _, err := github.NewClient("http://[::1"); err == nil {
		t.Error("NewClient with an unparsable API URL = nil error, want one")
	}
}
