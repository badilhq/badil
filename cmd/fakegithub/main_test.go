package main

import (
	"io"
	"strings"
	"testing"
)

// env returns a getenv with the webhook secret set.
func env(name string) string {
	if name == secretEnv {
		return "s3cret"
	}
	return ""
}

func TestParseSettings(t *testing.T) {
	base := []string{"--webhook-url", "http://localhost:8080/webhooks/github"}

	t.Run("defaults and trailing slash", func(t *testing.T) {
		s, err := parseSettings(append(base, "--web-url", "http://localhost:3000/"), env, io.Discard)
		if err != nil {
			t.Fatalf("parseSettings: %v", err)
		}
		if s.server.WebURL != "http://localhost:3000" {
			t.Errorf("WebURL = %q, want http://localhost:3000", s.server.WebURL)
		}
		if s.listen != ":8090" || s.server.InstallationID != 1 || s.server.AppSlug != "badil-mock" ||
			s.server.BotUserID != 1 || s.server.WebhookSecret != "s3cret" {
			t.Errorf("settings = %+v, want the defaults and the secret from the environment", s)
		}
	})

	for _, tc := range []struct{ name, webURL, want string }{
		{"path", "http://localhost:3000/gitea", "must not have a path"},
		{"user info", "http://u:p@localhost:3000", "must not contain user info"},
		{"query", "http://localhost:3000?x=1", "must not have a query"},
		{"fragment", "http://localhost:3000#x", "must not have a fragment"},
		{"scheme", "ftp://localhost:3000", "must be an https or http URL"},
		{"missing", "", "--web-url is required"},
	} {
		t.Run("web-url "+tc.name, func(t *testing.T) {
			_, err := parseSettings(append(base, "--web-url", tc.webURL), env, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("parseSettings(--web-url %q) error = %v, want %q", tc.webURL, err, tc.want)
			}
		})
	}

	t.Run("missing secret", func(t *testing.T) {
		_, err := parseSettings(append(base, "--web-url", "http://localhost:3000"),
			func(string) string { return "" }, io.Discard)
		if err == nil || !strings.Contains(err.Error(), secretEnv) {
			t.Errorf("error = %v, want %s is required", err, secretEnv)
		}
	})

	t.Run("missing webhook url", func(t *testing.T) {
		_, err := parseSettings([]string{"--web-url", "http://localhost:3000"}, env, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "--webhook-url is required") {
			t.Errorf("error = %v, want --webhook-url is required", err)
		}
	})
}
