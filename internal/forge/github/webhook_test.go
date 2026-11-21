package github_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	f, err := forgegithub.New(nil, app, allowedInstallation, "https://github.com")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

func TestParseEvent(t *testing.T) {
	f := webhookForge(t)

	t.Run("pull_request payload fields", func(t *testing.T) {
		payload := []byte(`{"action":"labeled","number":42,` +
			`"label":{"name":"preview"},` +
			`"pull_request":{"number":42,"labels":[{"name":"preview"},{"name":"bug"}],` +
			`"head":{"ref":"feature/add-pricing","sha":"0000000000000000000000000000000000000001"},` +
			`"user":{"login":"octocat"}},` +
			`"repository":{"name":"backend-1","full_name":"acme/backend-1","owner":{"login":"acme"}},` +
			`"sender":{"login":"someone-else"},` + allowedInstallationField + `}`)
		event, err := f.ParseWebhook(signedWebhook("pull_request", payload))
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		want := forge.Event{
			Name: "pull_request",
			Kind: forge.KindPullRequest,
			PullRequest: forge.PullRequestEvent{
				Owner:    "acme",
				Repo:     "backend-1",
				FullName: "acme/backend-1",
				Number:   42,
				Action:   "labeled",
				Label:    "preview",
				Labels:   []string{"preview", "bug"},
				HeadRef:  "feature/add-pricing",
				HeadSHA:  "0000000000000000000000000000000000000001",
				Author:   "octocat",
			},
		}
		if !reflect.DeepEqual(event, want) {
			t.Fatalf("event = %+v, want %+v", event, want)
		}
	})

	t.Run("push payload fields", func(t *testing.T) {
		payload := []byte(`{"ref":"refs/heads/main","before":"0000000000000000000000000000000000000003",` +
			`"after":"0000000000000000000000000000000000000002","deleted":true,` +
			`"repository":{"name":"backend-1","full_name":"acme/backend-1","default_branch":"main",` +
			`"master_branch":"legacy"},` + allowedInstallationField + `}`)
		event, err := f.ParseWebhook(signedWebhook("push", payload))
		if err != nil {
			t.Fatalf("ParseWebhook() error = %v", err)
		}
		want := forge.Event{
			Name: "push",
			Kind: forge.KindPush,
			Push: forge.PushEvent{
				FullName:      "acme/backend-1",
				Ref:           "refs/heads/main",
				DefaultBranch: "main",
				After:         "0000000000000000000000000000000000000002",
				Deleted:       true,
			},
		}
		if !reflect.DeepEqual(event, want) {
			t.Fatalf("event = %+v, want %+v", event, want)
		}
	})
}
