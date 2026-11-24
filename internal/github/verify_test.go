package github_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"testing"

	"github.com/badilhq/badil/internal/fakegithub"
	"github.com/badilhq/badil/internal/github"
)

// TestVerifyApp_Mock checks VerifyApp's output against the mock GitHub
// server: the mock App's identity and its one installation.
func TestVerifyApp_Mock(t *testing.T) {
	mock := httptest.NewServer(fakegithub.New(fakegithub.Config{
		WebURL: "http://git.local:3000", WebhookURL: "http://127.0.0.1:1/webhooks/github", WebhookSecret: "s",
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
	var out bytes.Buffer
	if err = github.VerifyApp(t.Context(), app, &out); err != nil {
		t.Fatalf("VerifyApp: %v", err)
	}
	const want = "App credentials OK\n" +
		"  id:    1\n" +
		"  slug:  badil-mock\n" +
		"  name:  badil-mock\n" +
		"  owner: badil\n" +
		"  url:   http://git.local:3000/apps/badil-mock\n" +
		"\nInstallations (1):\n" +
		"  - id=1  account=badil (Organization)\n"
	if got := out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
