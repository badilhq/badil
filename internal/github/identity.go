package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	gh "github.com/google/go-github/v71/github"
)

// Identity is the author and committer of the gitops commits.
type Identity struct {
	Name, Email string
}

// CommitIdentity returns the GitHub App's bot as the author and committer of
// the gitops commits: name <slug>[bot] and email
// <id>+<slug>[bot]@users.noreply.<host>, where host is the host name of
// githubURL (GITHUB_URL) without its port, the slug comes from GET /app with
// appClient (the App JWT) and the bot user id from GET /users/<slug>[bot]
// with installClient (the installation).
func CommitIdentity(
	ctx context.Context, appClient, installClient *gh.Client, githubURL string,
) (Identity, error) {
	web, err := url.Parse(githubURL)
	if err != nil {
		return Identity{}, fmt.Errorf("GITHUB_URL: %w", err)
	}
	app, _, err := appClient.Apps.Get(ctx, "")
	if err != nil {
		return Identity{}, fmt.Errorf("GET /app: %w", err)
	}
	if app.GetSlug() == "" {
		return Identity{}, errors.New("GET /app: no slug")
	}
	login := app.GetSlug() + "[bot]"
	user, _, err := installClient.Users.Get(ctx, login)
	if err != nil {
		return Identity{}, fmt.Errorf("GET /users/%s: %w", login, err)
	}
	if user.GetID() == 0 {
		return Identity{}, fmt.Errorf("GET /users/%s: no id", login)
	}
	return Identity{
		Name:  login,
		Email: fmt.Sprintf("%d+%s@users.noreply.%s", user.GetID(), login, web.Hostname()),
	}, nil
}
