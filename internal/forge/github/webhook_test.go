package github_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/badilhq/badil/internal/forge"
	forgegithub "github.com/badilhq/badil/internal/forge/github"
	"github.com/badilhq/badil/internal/github"
)

const (
	testWebhookSecret        = "secret"
	allowedInstallation      = 123
	allowedInstallationField = `"installation":{"id":123}`
)

// signedWebhook builds a delivery of eventType with payload, signed with
// testWebhookSecret.
func signedWebhook(eventType string, payload []byte) *http.Request {
	mac := hmac.New(sha256.New, []byte(testWebhookSecret))
	mac.Write(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-Github-Event", eventType)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// webhookForge returns a forge validating deliveries with testWebhookSecret
// and accepting the installation allowedInstallation.
func webhookForge(t *testing.T) *forgegithub.Forge {
	t.Helper()
	app, err := github.NewAppFromTransport(nil, testWebhookSecret)
	if err != nil {
		t.Fatalf("NewAppFromTransport: %v", err)
	}
	return forgegithub.New(nil, app, allowedInstallation)
}

func TestParseEvent(t *testing.T) {
	f := webhookForge(t)

	t.Run("pull_request event", func(t *testing.T) {
		payload := []byte(`{"action":"opened","number":1,"pull_request":{"number":1},` + allowedInstallationField + `}`)
		event, err := f.ParseWebhook(signedWebhook("pull_request", payload))
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		if event.Kind != forge.KindPullRequest {
			t.Fatalf("kind = %v, want %v", event.Kind, forge.KindPullRequest)
		}
		if event.PullRequest.Action != "opened" {
			t.Fatalf("action = %q, want %q", event.PullRequest.Action, "opened")
		}
	})

	t.Run("installation event", func(t *testing.T) {
		payload := []byte(`{"action":"created",` + allowedInstallationField + `}`)
		event, err := f.ParseWebhook(signedWebhook("installation", payload))
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		if event.Kind != forge.KindOther {
			t.Fatalf("kind = %v, want %v", event.Kind, forge.KindOther)
		}
		if event.Name != "installation" {
			t.Fatalf("name = %q, want %q", event.Name, "installation")
		}
	})

	t.Run("unknown event type", func(t *testing.T) {
		_, err := f.ParseWebhook(signedWebhook("unknown_event", []byte(`{}`)))
		if err == nil {
			t.Fatal("ParseWebhook() expected error for unknown event type")
		}
	})
}
