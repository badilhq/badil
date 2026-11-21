package github

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v71/github"
)

// AppConfig holds the configuration for a GitHub App.
type AppConfig struct {
	AppID         int64
	PrivateKey    []byte //nolint:gosec // intentional field name for PEM-encoded RSA private key
	WebhookSecret string
	// APIURL is the API base of the GitHub host (GITHUB_API_URL), "" or
	// DefaultAPIURL for the default.
	APIURL string
}

// App provides GitHub App authentication and webhook handling.
type App struct {
	webhookSecret []byte
	transport     *ghinstallation.AppsTransport
	// baseURL and uploadURL are the resolved API and upload bases of a
	// custom API base (NewClient), nil for the default.
	baseURL, uploadURL *url.URL
}

// NewApp creates a new App from the given configuration. Its transports and
// the clients of Client send every request under cfg.APIURL as NewClient
// resolves it.
func NewApp(cfg AppConfig) (*App, error) {
	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, cfg.AppID, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	app := &App{webhookSecret: []byte(cfg.WebhookSecret), transport: tr}
	if isDefaultAPIURL(cfg.APIURL) {
		return app, nil
	}
	c, err := NewClient(nil, cfg.APIURL)
	if err != nil {
		return nil, fmt.Errorf("GitHub API URL: %w", err)
	}
	app.baseURL, app.uploadURL = c.BaseURL, c.UploadURL
	// ghinstallation appends "/app/..." to its BaseURL: go-github's resolved
	// base keeps the token requests under the same path as the API calls.
	tr.BaseURL = strings.TrimSuffix(c.BaseURL.String(), "/")
	return app, nil
}

// NewAppFromTransport creates a new App using a pre-configured AppsTransport.
// This is useful for testing with custom base URLs.
func NewAppFromTransport(tr *ghinstallation.AppsTransport, webhookSecret string) (*App, error) {
	return &App{
		webhookSecret: []byte(webhookSecret),
		transport:     tr,
	}, nil
}

// InstallationTransport returns a transport authenticated as the given
// installation. It caches the installation token and mints a new one only when
// the cached one is due for refresh, so a process builds one and shares it.
// It requests its tokens under the App's API base.
func (a *App) InstallationTransport(installationID int64) *ghinstallation.Transport {
	return ghinstallation.NewFromAppsTransport(a.transport, installationID)
}

// AppsTransport returns the transport authenticated as the App itself, with
// the App JWT, for the App's own endpoints such as GET /app.
func (a *App) AppsTransport() *ghinstallation.AppsTransport {
	return a.transport
}

// Client returns a go-github client over rt on the App's API base: the
// default client for the default base, else the bases NewClient resolved.
func (a *App) Client(rt http.RoundTripper) *gh.Client {
	c := gh.NewClient(&http.Client{Transport: rt})
	if a.baseURL != nil {
		base, upload := *a.baseURL, *a.uploadURL
		c.BaseURL, c.UploadURL = &base, &upload
	}
	return c
}

// NewClient returns a go-github client over rt, http.DefaultTransport when
// nil, on the API base apiURL: gh.NewClient's default when apiURL is "" or
// DefaultAPIURL, else WithEnterpriseURLs(apiURL, apiURL), which appends
// "api/v3/" to a base whose path does not end with it and whose host neither
// starts with "api." nor contains ".api.".
func NewClient(rt http.RoundTripper, apiURL string) (*gh.Client, error) {
	var hc *http.Client
	if rt != nil {
		hc = &http.Client{Transport: rt}
	}
	c := gh.NewClient(hc)
	if isDefaultAPIURL(apiURL) {
		return c, nil
	}
	return c.WithEnterpriseURLs(apiURL, apiURL)
}

// isDefaultAPIURL reports whether apiURL means the default API base.
func isDefaultAPIURL(apiURL string) bool {
	return apiURL == "" || apiURL == DefaultAPIURL
}
