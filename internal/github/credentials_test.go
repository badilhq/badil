package github_test

import (
	"context"
	"testing"

	"github.com/badilhq/badil/internal/github"
)

// TestGitopsCredentials checks GITOPS_REPO_TOKEN authenticates the git calls,
// with the default or the configured username.
func TestGitopsCredentials(t *testing.T) {
	cases := []struct {
		name, username, token string
		wantUser, wantPass    string
	}{
		{name: "token", token: "glpat-secret", wantUser: "x-access-token", wantPass: "glpat-secret"},
		{
			name: "token and username", username: "deploy-user", token: "glpat-secret",
			wantUser: "deploy-user", wantPass: "glpat-secret",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user, password, err := github.GitopsCredentials(tc.username, tc.token)(context.Background())
			if err != nil || user != tc.wantUser || password != tc.wantPass {
				t.Errorf("credentials = %q, %q, %v; want %q, %q", user, password, err, tc.wantUser, tc.wantPass)
			}
		})
	}
}
