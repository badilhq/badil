package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/badilhq/badil/internal/store"
)

// Config holds all application configuration.
type Config struct {
	AppID          int64
	PrivateKey     []byte //nolint:gosec // intentional field name
	WebhookSecret  string
	InstallationID int64
	// GitOpsRepoURL is the http(s) clone URL of the gitops repo.
	GitOpsRepoURL string
	// GitOpsRepoUsername is the basic-auth username of GitOpsRepoToken, ""
	// for the default.
	GitOpsRepoUsername string
	// GitOpsRepoToken authenticates the git calls to the gitops repo, ""
	// for the GitHub App installation token.
	GitOpsRepoToken string
	ListenAddr      string
	// DeployToken authenticates POST /v1/deploy and POST /v1/sync, "" when
	// they are disabled.
	DeployToken string
	// ReadToken authenticates the GET /v1 routes, which DeployToken also
	// grants, "" when only DeployToken does.
	ReadToken string
	// MetricsAddr is the listen address of the metrics server, "" when it is
	// disabled.
	MetricsAddr string
	// StatusToken authenticates POST /webhooks/status, "" when it is
	// disabled.
	StatusToken string
	// GitOpsCloneDir is the directory of the gitops repo clone, "" for a
	// temporary directory of the process.
	GitOpsCloneDir string
}

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	appID, err := requireEnvInt64("GITHUB_APP_ID")
	if err != nil {
		return nil, err
	}

	privateKey, err := LoadPrivateKey()
	if err != nil {
		return nil, err
	}

	webhookSecret, err := requireEnv("GITHUB_WEBHOOK_SECRET")
	if err != nil {
		return nil, err
	}

	installationID, err := requireEnvInt64("GITHUB_INSTALLATION_ID")
	if err != nil {
		return nil, err
	}

	gitOpsRepoURL, err := requireEnv("GITOPS_REPO_URL")
	if err != nil {
		return nil, err
	}
	if err = validateRepoURL(gitOpsRepoURL); err != nil {
		return nil, fmt.Errorf("GITOPS_REPO_URL %w", err)
	}

	username, token := os.Getenv("GITOPS_REPO_USERNAME"), os.Getenv("GITOPS_REPO_TOKEN")
	if username != "" && token == "" {
		return nil, errors.New("GITOPS_REPO_USERNAME requires GITOPS_REPO_TOKEN")
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	metricsAddr, err := loadMetricsAddr()
	if err != nil {
		return nil, err
	}

	return &Config{
		AppID:              appID,
		PrivateKey:         privateKey,
		WebhookSecret:      webhookSecret,
		InstallationID:     installationID,
		GitOpsRepoURL:      gitOpsRepoURL,
		GitOpsRepoUsername: username,
		GitOpsRepoToken:    token,
		ListenAddr:         listenAddr,
		DeployToken:        os.Getenv("BADIL_DEPLOY_TOKEN"),
		ReadToken:          os.Getenv("BADIL_READ_TOKEN"),
		MetricsAddr:        metricsAddr,
		StatusToken:        os.Getenv("BADIL_STATUS_TOKEN"),
		GitOpsCloneDir:     os.Getenv("GITOPS_CLONE_DIR"),
	}, nil
}

// validateRepoURL checks raw is an http(s) URL with a host, no user info, query
// or fragment, and a path store.RepoFullName accepts. Its error reads after the
// setting's name.
func validateRepoURL(raw string) error {
	u, err := url.Parse(raw)
	if ue, ok := errors.AsType[*url.Error](err); ok {
		// The URL itself stays out of the error: it may hold credentials.
		err = ue.Err
	}
	switch {
	case err != nil:
		return fmt.Errorf("must be a URL: %w", err)
	case u.Scheme != "https" && u.Scheme != "http":
		return fmt.Errorf("must be an https or http URL, got scheme %q", u.Scheme)
	case u.Hostname() == "":
		return errors.New("must have a host")
	case u.User != nil:
		return errors.New("must not contain credentials: set GITOPS_REPO_TOKEN")
	case u.RawQuery != "" || u.ForceQuery:
		return errors.New("must not have a query")
	case strings.Contains(raw, "#"):
		return errors.New("must not have a fragment")
	}
	if _, ok := store.RepoFullName(u.Path); !ok {
		return fmt.Errorf("must have a repository path of at least two segments, got %q", u.Path)
	}
	return nil
}

// loadMetricsAddr reads METRICS_ADDR: unset means ":9090", set to "" means
// disabled, and any other value must be a host:port.
func loadMetricsAddr() (string, error) {
	addr, ok := os.LookupEnv("METRICS_ADDR")
	if !ok {
		return ":9090", nil
	}
	if addr == "" {
		return "", nil
	}
	if !validHostPort(addr) {
		return "", fmt.Errorf("METRICS_ADDR must be host:port, got %q", addr)
	}
	return addr, nil
}

// validHostPort reports whether addr is a host:port with a numeric port in
// 0-65535.
func validHostPort(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return false
	}
	_, err = strconv.ParseUint(port, 10, 16)
	return err == nil
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func requireEnvInt64(key string) (int64, error) {
	s, err := requireEnv(key)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return v, nil
}

// LoadPrivateKey reads the GitHub App PEM from GITHUB_PRIVATE_KEY_PATH or,
// falling back, from inline GITHUB_PRIVATE_KEY.
func LoadPrivateKey() ([]byte, error) {
	if path := os.Getenv("GITHUB_PRIVATE_KEY_PATH"); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // path from trusted env var
		if err != nil {
			return nil, fmt.Errorf("reading private key file: %w", err)
		}
		return data, nil
	}
	if raw := os.Getenv("GITHUB_PRIVATE_KEY"); raw != "" {
		return []byte(raw), nil
	}
	return nil, errors.New("GITHUB_PRIVATE_KEY or GITHUB_PRIVATE_KEY_PATH is required")
}
