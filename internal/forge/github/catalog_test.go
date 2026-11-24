package github_test

import (
	"reflect"
	"testing"

	"github.com/badilhq/badil/internal/catalog"
)

// reposCatalog has services on GitHub repos written in several forms, and
// on an SSH and an enterprise URL.
const reposCatalog = `services:
  api: {repo: https://github.com/acme/monorepo.git}
  worker: {repo: https://github.com/Acme/MonoRepo/}
  front: {repo: https://github.com/acme/front}
  web: {repo: HTTPS://GITHUB.COM/ACME/WEB.GIT}
  ssh: {repo: "git@github.com:acme/ssh.git"}
  ghe: {repo: https://github.acme.example/acme/ghe.git}
  nested: {repo: https://github.com/acme/group/nested}
`

// parseRepo is the GitHub forge's repo URL rule on https://github.com.
func parseRepo(t *testing.T) catalog.RepoURLParser {
	t.Helper()
	return newURLForge(t, "https://github.com").ParseRepoURL
}

func TestServicesForRepo(t *testing.T) {
	cat, err := catalog.Parse([]byte(reposCatalog))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	tests := []struct {
		name, owner, repo string
		want              []string
	}{
		{"two services on one repo, .git and trailing slash", "acme", "monorepo", []string{"api", "worker"}},
		{"case-insensitive owner and repo", "ACME", "MonoRepo", []string{"api", "worker"}},
		{"no .git", "acme", "front", []string{"front"}},
		{"uppercase url", "acme", "web", []string{"web"}},
		{"ssh url never matches", "acme", "ssh", nil},
		{"enterprise url never matches", "acme", "ghe", nil},
		{"unknown repo", "acme", "unknown", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cat.ServicesForRepo(parseRepo(t), tt.owner, tt.repo)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ServicesForRepo(%q, %q) = %v, want %v", tt.owner, tt.repo, got, tt.want)
			}
		})
	}
}

func TestRepos(t *testing.T) {
	cat, err := catalog.Parse([]byte(reposCatalog))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	want := []catalog.Repo{
		{Owner: "acme", Repo: "monorepo"},
		{Owner: "acme", Repo: "front"},
		{Owner: "acme", Repo: "web"},
	}
	if got := cat.Repos(parseRepo(t)); !reflect.DeepEqual(got, want) {
		t.Errorf("Repos = %v, want %v", got, want)
	}
}
