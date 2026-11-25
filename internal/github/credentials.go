package github

import (
	"cmp"
	"context"
)

// defaultGitUsername is the basic-auth username of GITOPS_REPO_TOKEN when
// GITOPS_REPO_USERNAME is unset.
const defaultGitUsername = "x-access-token"

// GitopsCredentials authenticates the store's git calls with token
// (GITOPS_REPO_TOKEN), as username (GITOPS_REPO_USERNAME) or
// defaultGitUsername when it is empty.
func GitopsCredentials(username, token string) func(context.Context) (string, string, error) {
	user := cmp.Or(username, defaultGitUsername)
	return func(context.Context) (string, string, error) {
		return user, token, nil
	}
}
