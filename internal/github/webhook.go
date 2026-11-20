package github

import (
	"net/http"

	gh "github.com/google/go-github/v71/github"
)

// ValidateWebhook validates the webhook signature and returns the raw payload.
func (a *App) ValidateWebhook(r *http.Request) ([]byte, error) {
	return gh.ValidatePayload(r, a.webhookSecret)
}
