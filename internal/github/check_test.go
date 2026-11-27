package github_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/fakegithub"
	"github.com/badilhq/badil/internal/github"
)

// TestCheckApp checks CheckApp against the fake GitHub, whose one
// installation is 1: installation 1 passes, installation 2 fails on its token.
func TestCheckApp(t *testing.T) {
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

	t.Run("installed", func(t *testing.T) {
		if got := github.CheckApp(t.Context(), app, 1); got != nil {
			t.Errorf("CheckApp(1) = %v, want nil", got)
		}
	})
	t.Run("wrong installation", func(t *testing.T) {
		got := github.CheckApp(t.Context(), app, 2)
		if got == nil || !strings.Contains(got.Error(), "installation 2 token") {
			t.Errorf("CheckApp(2) = %v, want an error containing %q", got, "installation 2 token")
		}
	})
}
