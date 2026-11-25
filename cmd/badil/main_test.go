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
	"slices"
	"testing"
	"time"

	"github.com/badilhq/badil/internal/config"
	forgegithub "github.com/badilhq/badil/internal/forge/github"
	"github.com/badilhq/badil/internal/store"
)

// fakeGitHubAPI returns a GitHub API test server serving, under /api/v3/,
// installation 67890's token, GET /app of the App badil-dev, its bot user and
// acme/front#7, and the requests it received, as "<method> <path>".
func fakeGitHubAPI(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		switch r.URL.Path {
		case "/api/v3/app/installations/67890/access_tokens":
			body = map[string]any{
				"token":      "ghs_installation",
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			}
		case "/api/v3/app":
			body = map[string]any{"id": 12345, "slug": "badil-dev"}
		case "/api/v3/users/badil-dev[bot]":
			body = map[string]any{"id": 348525889, "login": "badil-dev[bot]"}
		case "/api/v3/repos/acme/front/pulls/7":
			body = map[string]any{"number": 7, "html_url": "http://gitea.local:3000/acme/front/pull/7"}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// mustNewForge returns the forge and the commit identity newForge builds from
// cfg with newApp's App, failing t on an error.
func mustNewForge(t *testing.T, cfg *config.Config) (*forgegithub.Forge, store.CommitIdentity) {
	t.Helper()
	app, err := newApp(cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	f, identity, err := newForge(context.Background(), app, cfg)
	if err != nil {
		t.Fatalf("newForge: %v", err)
	}
	return f, identity
}

// TestNewForge_CustomAPIURL checks that with GITHUB_API_URL on a test
// server, with or without its api/v3/ suffix, the installation token request,
// GET /app, GET /users/<slug>[bot] and a forge call all reach it under one
// path prefix, and that the forge and the bot email follow GITHUB_URL.
func TestNewForge_CustomAPIURL(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	want := []string{
		"GET /api/v3/app",
		"POST /api/v3/app/installations/67890/access_tokens",
		"GET /api/v3/users/badil-dev[bot]",
		"GET /api/v3/repos/acme/front/pulls/7",
	}
	for _, suffix := range []string{"/api/v3/", "/"} {
		t.Run(suffix, func(t *testing.T) {
			srv, requests := fakeGitHubAPI(t)
			cfg := &config.Config{
				AppID: 12345, PrivateKey: pemKey, InstallationID: 67890,
				GitHubURL: "http://gitea.local:3000", GitHubAPIURL: srv.URL + suffix,
			}
			ctx := context.Background()
			f, identity := mustNewForge(t, cfg)
			pr, prErr := f.GetPullRequest(ctx, "acme", "front", 7)
			if prErr != nil {
				t.Fatalf("GetPullRequest: %v", prErr)
			}
			if !slices.Equal(*requests, want) {
				t.Errorf("requests = %q, want %q", *requests, want)
			}
			if want := "348525889+badil-dev[bot]@users.noreply.gitea.local"; identity.Email != want {
				t.Errorf("bot email = %q, want %q", identity.Email, want)
			}
			if o, r, n, ok := f.ParsePRURL(pr.URL); !ok || o != "acme" || r != "front" || n != 7 {
				t.Errorf("ParsePRURL(%q) = %q, %q, %d, %v; want acme, front, 7, true", pr.URL, o, r, n, ok)
			}
		})
	}
}
