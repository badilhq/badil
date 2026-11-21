package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/config"
)

func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"GITHUB_APP_ID":          "12345",
		"GITHUB_PRIVATE_KEY":     "fake-pem-key",
		"GITHUB_WEBHOOK_SECRET":  "secret",
		"GITHUB_INSTALLATION_ID": "67890",
		"GITOPS_REPO_URL":        "https://github.com/org/gitops-repo.git",
		"BADIL_DEPLOY_TOKEN":     "deploy-secret",
	}
}

func TestLoad_MissingAppID(t *testing.T) {
	env := validEnv()
	delete(env, "GITHUB_APP_ID")
	setEnv(t, env)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing GITHUB_APP_ID")
	}
}

func TestLoad_InvalidAppID(t *testing.T) {
	env := validEnv()
	env["GITHUB_APP_ID"] = "not-a-number"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for non-integer GITHUB_APP_ID")
	}
}

func TestLoad_MissingPrivateKey(t *testing.T) {
	env := validEnv()
	delete(env, "GITHUB_PRIVATE_KEY")
	setEnv(t, env)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing private key")
	}
}

func TestLoad_MissingWebhookSecret(t *testing.T) {
	env := validEnv()
	delete(env, "GITHUB_WEBHOOK_SECRET")
	setEnv(t, env)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing GITHUB_WEBHOOK_SECRET")
	}
}

func TestLoad_Valid(t *testing.T) {
	setEnv(t, validEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppID != 12345 {
		t.Errorf("AppID = %d, want 12345", cfg.AppID)
	}
	if string(cfg.PrivateKey) != "fake-pem-key" {
		t.Errorf("PrivateKey = %q, want %q", cfg.PrivateKey, "fake-pem-key")
	}
	if cfg.WebhookSecret != "secret" {
		t.Errorf("WebhookSecret = %q, want %q", cfg.WebhookSecret, "secret")
	}
	if cfg.InstallationID != 67890 {
		t.Errorf("InstallationID = %d, want 67890", cfg.InstallationID)
	}
	if cfg.GitOpsRepoURL != "https://github.com/org/gitops-repo.git" {
		t.Errorf("GitOpsRepoURL = %q, want %q", cfg.GitOpsRepoURL, "https://github.com/org/gitops-repo.git")
	}
	if cfg.GitOpsRepoUsername != "" || cfg.GitOpsRepoToken != "" {
		t.Errorf("GitOpsRepoUsername, GitOpsRepoToken = %q, %q; want empty when unset",
			cfg.GitOpsRepoUsername, cfg.GitOpsRepoToken)
	}
	if cfg.DeployToken != "deploy-secret" {
		t.Errorf("DeployToken = %q, want %q", cfg.DeployToken, "deploy-secret")
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
}

func TestLoad_MissingDeployToken(t *testing.T) {
	env := validEnv()
	delete(env, "BADIL_DEPLOY_TOKEN")
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DeployToken != "" {
		t.Errorf("DeployToken = %q, want empty", cfg.DeployToken)
	}
}

func TestLoad_MissingInstallationID(t *testing.T) {
	env := validEnv()
	delete(env, "GITHUB_INSTALLATION_ID")
	setEnv(t, env)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing GITHUB_INSTALLATION_ID")
	}
}

func TestLoad_InvalidInstallationID(t *testing.T) {
	env := validEnv()
	env["GITHUB_INSTALLATION_ID"] = "not-a-number"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for non-integer GITHUB_INSTALLATION_ID")
	}
}

func TestLoad_MissingGitOpsRepoURL(t *testing.T) {
	env := validEnv()
	delete(env, "GITOPS_REPO_URL")
	setEnv(t, env)

	_, err := config.Load()
	if want := "GITOPS_REPO_URL is required"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestLoad_GitOpsRepoURL(t *testing.T) {
	for _, value := range []string{
		"https://github.com/acme/gitops.git",
		"https://github.com/acme/gitops",
		"https://github.com/acme/gitops/",
		"https://github.com/acme/gitops.git/",
		"http://gitea.gitea.svc:3000/acme/gitops.git",
		"https://gitlab.example.com/group/sub/gitops.git",
	} {
		t.Run(value, func(t *testing.T) {
			env := validEnv()
			env["GITOPS_REPO_URL"] = value
			setEnv(t, env)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.GitOpsRepoURL != value {
				t.Errorf("GitOpsRepoURL = %q, want %q", cfg.GitOpsRepoURL, value)
			}
		})
	}
}

func TestLoad_InvalidGitOpsRepoURL(t *testing.T) {
	cases := []struct {
		value, want string
	}{
		{"ssh://git@github.com/acme/gitops.git", `GITOPS_REPO_URL must be an https or http URL, got scheme "ssh"`},
		{
			"git@github.com:acme/gitops.git",
			"GITOPS_REPO_URL must be a URL: first path segment in URL cannot contain colon",
		},
		{"file:///srv/acme/gitops.git", `GITOPS_REPO_URL must be an https or http URL, got scheme "file"`},
		{"github.com/acme/gitops.git", `GITOPS_REPO_URL must be an https or http URL, got scheme ""`},
		{"https:///acme/gitops.git", "GITOPS_REPO_URL must have a host"},
		{
			"https://x-access-token:secret@github.com/acme/gitops.git",
			"GITOPS_REPO_URL must not contain credentials: set GITOPS_REPO_TOKEN",
		},
		{
			"https://user@github.com/acme/gitops.git",
			"GITOPS_REPO_URL must not contain credentials: set GITOPS_REPO_TOKEN",
		},
		{"https://github.com/acme/gitops.git?ref=main", "GITOPS_REPO_URL must not have a query"},
		{"https://github.com/acme/gitops.git#main", "GITOPS_REPO_URL must not have a fragment"},
		{"https://github.com/acme/gitops.git#", "GITOPS_REPO_URL must not have a fragment"},
		{"https://github.com", `GITOPS_REPO_URL must have a repository path of at least two segments, got ""`},
		{
			"https://github.com/gitops.git",
			`GITOPS_REPO_URL must have a repository path of at least two segments, got "/gitops.git"`,
		},
		{
			"https://github.com/acme/",
			`GITOPS_REPO_URL must have a repository path of at least two segments, got "/acme/"`,
		},
		{
			"https://github.com//gitops",
			`GITOPS_REPO_URL must have a repository path of at least two segments, got "//gitops"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			env := validEnv()
			env["GITOPS_REPO_URL"] = tc.value
			setEnv(t, env)
			_, err := config.Load()
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoad_InvalidGitOpsRepoURLHidesCredentials(t *testing.T) {
	env := validEnv()
	env["GITOPS_REPO_URL"] = "https://user:s3cret@github.com/acme/%zz"
	setEnv(t, env)
	_, err := config.Load()
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v, want an error without the URL's password", err)
	}
}

func TestLoad_GitOpsRepoCredentials(t *testing.T) {
	cases := []struct {
		name, username, token string
		wantErr               string
	}{
		{name: "token only", token: "glpat-secret"},
		{name: "username and token", username: "deploy-user", token: "glpat-secret"},
		{
			name:     "username without token",
			username: "deploy-user",
			wantErr:  "GITOPS_REPO_USERNAME requires GITOPS_REPO_TOKEN",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["GITOPS_REPO_USERNAME"] = tc.username
			env["GITOPS_REPO_TOKEN"] = tc.token
			setEnv(t, env)
			cfg, err := config.Load()
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.GitOpsRepoUsername != tc.username || cfg.GitOpsRepoToken != tc.token {
				t.Errorf("GitOpsRepoUsername, GitOpsRepoToken = %q, %q; want %q, %q",
					cfg.GitOpsRepoUsername, cfg.GitOpsRepoToken, tc.username, tc.token)
			}
		})
	}
}

func TestLoad_DefaultListenAddr(t *testing.T) {
	setEnv(t, validEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
}

func TestLoad_CustomListenAddr(t *testing.T) {
	env := validEnv()
	env["LISTEN_ADDR"] = ":9090"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
}

func TestLoad_PrivateKeyPathTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyFile, []byte("file-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	env := validEnv()
	env["GITHUB_PRIVATE_KEY_PATH"] = keyFile
	env["GITHUB_PRIVATE_KEY"] = "inline-key"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(cfg.PrivateKey) != "file-key" {
		t.Errorf("PrivateKey = %q, want %q (file should take precedence)", cfg.PrivateKey, "file-key")
	}
}

// loadWithMetricsAddr loads a valid config with METRICS_ADDR set to value, or
// unset when set is false.
func loadWithMetricsAddr(t *testing.T, set bool, value string) (*config.Config, error) {
	t.Helper()
	setEnv(t, validEnv())
	t.Setenv("METRICS_ADDR", value)
	if !set {
		if err := os.Unsetenv("METRICS_ADDR"); err != nil {
			t.Fatal(err)
		}
	}
	return config.Load()
}

func TestLoad_MetricsAddr(t *testing.T) {
	cases := []struct {
		name  string
		set   bool
		value string
		want  string
	}{
		{name: "unset defaults", want: ":9090"},
		{name: "empty disables", set: true, value: "", want: ""},
		{name: "port only", set: true, value: ":19090", want: ":19090"},
		{name: "host and port", set: true, value: "127.0.0.1:9091", want: "127.0.0.1:9091"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadWithMetricsAddr(t, tc.set, tc.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.MetricsAddr != tc.want {
				t.Errorf("MetricsAddr = %q, want %q", cfg.MetricsAddr, tc.want)
			}
		})
	}
}

func TestLoad_InvalidMetricsAddr(t *testing.T) {
	for _, value := range []string{"9090", ":", "host:abc", ":99999"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadWithMetricsAddr(t, true, value)
			if want := `METRICS_ADDR must be host:port, got "` + value + `"`; err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}

func TestLoad_StatusToken(t *testing.T) {
	setEnv(t, validEnv())
	t.Setenv("BADIL_STATUS_TOKEN", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.StatusToken != "" {
		t.Errorf("StatusToken = %q, want empty when unset", cfg.StatusToken)
	}

	t.Setenv("BADIL_STATUS_TOKEN", "status-secret")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.StatusToken != "status-secret" {
		t.Errorf("StatusToken = %q, want %q", cfg.StatusToken, "status-secret")
	}
}

func TestLoad_ReadToken(t *testing.T) {
	setEnv(t, validEnv())
	t.Setenv("BADIL_READ_TOKEN", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReadToken != "" {
		t.Errorf("ReadToken = %q, want empty when unset", cfg.ReadToken)
	}

	t.Setenv("BADIL_READ_TOKEN", "read-secret")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReadToken != "read-secret" {
		t.Errorf("ReadToken = %q, want %q", cfg.ReadToken, "read-secret")
	}
}

func TestLoad_GitOpsCloneDir(t *testing.T) {
	setEnv(t, validEnv())
	t.Setenv("GITOPS_CLONE_DIR", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitOpsCloneDir != "" {
		t.Errorf("GitOpsCloneDir = %q, want empty when unset", cfg.GitOpsCloneDir)
	}

	t.Setenv("GITOPS_CLONE_DIR", "/var/lib/badil/gitops")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitOpsCloneDir != "/var/lib/badil/gitops" {
		t.Errorf("GitOpsCloneDir = %q, want %q", cfg.GitOpsCloneDir, "/var/lib/badil/gitops")
	}
}
