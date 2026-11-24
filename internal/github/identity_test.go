package github_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	gh "github.com/google/go-github/v71/github"

	"github.com/badilhq/badil/internal/fakegithub"
	"github.com/badilhq/badil/internal/github"
)

// TestCommitIdentity checks the identity is the App's bot, read with the App
// JWT client for GET /app and the installation client for the bot user, and
// that a failed lookup is an error.
func TestCommitIdentity(t *testing.T) {
	var appAuth, userAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app":
			appAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "slug": "badil-dev"})
		case "/users/badil-dev[bot]":
			userAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 348525889, "login": "badil-dev[bot]"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	client := func(token string) *gh.Client {
		c := gh.NewClient(nil).WithAuthToken(token)
		u, err := url.Parse(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		c.BaseURL = u
		return c
	}

	id, err := github.CommitIdentity(
		context.Background(),
		client("app-jwt"),
		client("installation"),
		"https://github.com",
	)
	if err != nil {
		t.Fatalf("CommitIdentity: %v", err)
	}
	want := github.Identity{
		Name:  "badil-dev[bot]",
		Email: "348525889+badil-dev[bot]@users.noreply.github.com",
	}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
	if appAuth != "Bearer app-jwt" || userAuth != "Bearer installation" {
		t.Errorf("auth = %q, %q; want the App JWT for /app and the installation for the user", appAuth, userAuth)
	}

	id, err = github.CommitIdentity(
		context.Background(),
		client("app-jwt"),
		client("installation"),
		"http://gitea.local:3000",
	)
	if err != nil {
		t.Fatalf("CommitIdentity on gitea.local: %v", err)
	}
	if want := "348525889+badil-dev[bot]@users.noreply.gitea.local"; id.Email != want {
		t.Errorf("email on GITHUB_URL http://gitea.local:3000 = %q, want %q", id.Email, want)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err = github.CommitIdentity(
		context.Background(), client("app-jwt"), client("installation"), "https://github.com",
	); err == nil {
		t.Error("CommitIdentity with GET /app failing = nil error, want one")
	}
}

// TestCommitIdentity_Mock checks the identity against the mock GitHub server,
// with the App and installation clients on its API base: the mock App's bot
// and an email on the web URL's host name, without its port.
func TestCommitIdentity_Mock(t *testing.T) {
	const webURL = "http://git.local:3000"
	mock := httptest.NewServer(fakegithub.New(fakegithub.Config{
		WebURL: webURL, WebhookURL: "http://127.0.0.1:1/webhooks/github", WebhookSecret: "s",
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
	id, err := github.CommitIdentity(
		t.Context(),
		app.Client(app.AppsTransport()),
		app.Client(app.InstallationTransport(1)),
		webURL,
	)
	if err != nil {
		t.Fatalf("CommitIdentity: %v", err)
	}
	want := github.Identity{Name: "badil-mock[bot]", Email: "1+badil-mock[bot]@users.noreply.git.local"}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
}
