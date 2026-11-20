package github

import (
	"net/http"

	"github.com/bradleyfalzon/ghinstallation/v2"
)

// AppConfig holds the configuration for a GitHub App.
type AppConfig struct {
	AppID         int64
	PrivateKey    []byte //nolint:gosec // intentional field name for PEM-encoded RSA private key
	WebhookSecret string
}

// App provides GitHub App authentication and webhook handling.
type App struct {
	webhookSecret []byte
	transport     *ghinstallation.AppsTransport
}

// NewApp creates a new App from the given configuration.
func NewApp(cfg AppConfig) (*App, error) {
	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, cfg.AppID, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &App{
		webhookSecret: []byte(cfg.WebhookSecret),
		transport:     tr,
	}, nil
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
func (a *App) InstallationTransport(installationID int64) *ghinstallation.Transport {
	return ghinstallation.NewFromAppsTransport(a.transport, installationID)
}
