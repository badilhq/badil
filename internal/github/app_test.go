package github_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

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
