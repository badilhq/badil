package github_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/badilhq/badil/internal/github"
)

func signPayload(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newApp(t *testing.T, secret string) *github.App {
	t.Helper()
	app, err := github.NewApp(github.AppConfig{
		AppID:         12345,
		PrivateKey:    generateTestKey(t),
		WebhookSecret: secret,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	return app
}

func TestValidateWebhook(t *testing.T) {
	secret := "test-secret"
	app := newApp(t, secret)

	t.Run("valid signature", func(t *testing.T) {
		body := []byte(`{"action":"opened"}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", signPayload(body, secret))
		req.Header.Set("Content-Type", "application/json")

		payload, err := app.ValidateWebhook(req)
		if err != nil {
			t.Fatalf("ValidateWebhook() error = %v", err)
		}
		if !bytes.Equal(payload, body) {
			t.Fatalf("payload mismatch: got %s, want %s", payload, body)
		}
	})

	t.Run("invalid signature", func(t *testing.T) {
		body := []byte(`{"action":"opened"}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
		req.Header.Set("Content-Type", "application/json")

		_, err := app.ValidateWebhook(req)
		if err == nil {
			t.Fatal("ValidateWebhook() expected error for invalid signature")
		}
	})

	t.Run("missing signature", func(t *testing.T) {
		body := []byte(`{"action":"opened"}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		_, err := app.ValidateWebhook(req)
		if err == nil {
			t.Fatal("ValidateWebhook() expected error for missing signature")
		}
	})
}
