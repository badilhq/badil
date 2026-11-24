package github

import (
	"cmp"
	"context"

	"github.com/bradleyfalzon/ghinstallation/v2"
)

// defaultGitUsername is the basic-auth username of an installation token, and
// of GITOPS_REPO_TOKEN when GITOPS_REPO_USERNAME is unset.
const defaultGitUsername = "x-access-token"

// GitopsCredentials authenticates the store's git calls with token
// (GITOPS_REPO_TOKEN), as username (GITOPS_REPO_USERNAME) or
// defaultGitUsername, when the token is set, and as the installation of itr
// (GitCredentials) otherwise.
func GitopsCredentials(
	username, token string, itr *ghinstallation.Transport,
) func(context.Context) (string, string, error) {
	if token == "" {
		return GitCredentials(itr)
	}
	user := cmp.Or(username, defaultGitUsername)
	return func(context.Context) (string, string, error) {
		return user, token, nil
	}
}

// GitCredentials authenticates the store's git calls as the installation of
// itr: user x-access-token, with the installation token itr caches and
// refreshes before it expires.
func GitCredentials(itr *ghinstallation.Transport) func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		token, err := itr.Token(ctx)
		return defaultGitUsername, token, err
	}
}
