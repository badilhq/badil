// Package githubtest gives tests the GitHub forge's default repo and pull request URL rules, on https://github.com and without a client.
package githubtest

import (
	forgegithub "github.com/badilhq/badil/internal/forge/github"
)

// Forge is the GitHub forge on the default GITHUB_URL, https://github.com,
// without a client: its ParsePRURL and ParseRepoURL are the default repo and
// pull request URL rules.
var Forge = newForge() //nolint:gochecknoglobals // test fixture

func newForge() *forgegithub.Forge {
	f, err := forgegithub.New(nil, nil, 0, "https://github.com")
	if err != nil {
		panic(err)
	}
	return f
}

// URLs gives a forge fake the default repo and pull request URL rules: its
// ParseRepoURL and ParsePRURL are those of Forge. Embed it.
type URLs struct{}

// ParseRepoURL is Forge's ParseRepoURL.
func (URLs) ParseRepoURL(u string) (string, string, bool) {
	return Forge.ParseRepoURL(u)
}

// ParsePRURL is Forge's ParsePRURL.
func (URLs) ParsePRURL(u string) (string, string, int, bool) {
	return Forge.ParsePRURL(u)
}
