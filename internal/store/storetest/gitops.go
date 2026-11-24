package storetest

import (
	"context"
	"log/slog"
	"maps"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/catalog"
	forgegithub "github.com/badilhq/badil/internal/forge/github"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// GitHubForge is the GitHub forge on the default GITHUB_URL,
// https://github.com, without a client: its ParsePRURL and ParseRepoURL are
// the default repo and pull request URL rules.
var GitHubForge = newGitHubForge() //nolint:gochecknoglobals // test fixture

func newGitHubForge() *forgegithub.Forge {
	f, err := forgegithub.New(nil, nil, 0, "https://github.com")
	if err != nil {
		panic(err)
	}
	return f
}

// GitHubURLs gives a forge fake the default repo and pull request URL rules:
// its ParseRepoURL and ParsePRURL are those of GitHubForge. Embed it.
type GitHubURLs struct{}

// ParseRepoURL is GitHubForge's ParseRepoURL.
func (GitHubURLs) ParseRepoURL(u string) (string, string, bool) {
	return GitHubForge.ParseRepoURL(u)
}

// ParsePRURL is GitHubForge's ParsePRURL.
func (GitHubURLs) ParsePRURL(u string) (string, string, int, bool) {
	return GitHubForge.ParsePRURL(u)
}

// GitopsURL is the git URL of the gitops repo of the stores Gitops opens,
// acme/gitops: their full name, commit links and preview files' gitopsRepoURL
// derive from it (store.WithRepoURL).
const GitopsURL = "https://github.com/acme/gitops.git"

// Identity is the commit identity of the tests' stores.
var Identity = store.CommitIdentity{ //nolint:gochecknoglobals // test fixture
	Name:  "badil-test[bot]",
	Email: "1+badil-test[bot]@users.noreply.github.com",
}

// Project returns vals projected on catalog, as a sync of the acme/gitops
// store writes it. The projection's logs are discarded.
func Project(slug string, vals preview.Values, catalog *catalog.Catalog) preview.Values {
	cat := catalog.Catalog
	cat.GitopsRepoURL = GitopsURL
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	defer slog.SetDefault(prev)
	return preview.Project(vals, slug, cat, catalog.URLs).Values
}

// Gitops returns a remote holding badil as badil.yaml (none when ""), the
// preview file of each of previews and raw files (path to content), and a
// store of acme/gitops over it, configured by opts, closed when t ends. A preview whose slug is
// not in stale is projected on badil's catalog when it parses (Project), so
// that a sync leaves it alone.
func Gitops(
	t testing.TB,
	badil string,
	previews map[string]*preview.Values,
	stale map[string]bool,
	raw map[string]string,
	opts ...store.Option,
) (*Remote, *store.GitHubStore) {
	t.Helper()
	files := map[string]string{}
	var cat *catalog.Catalog
	if badil != "" {
		files["badil.yaml"] = badil
		cat, _ = catalog.Parse([]byte(badil))
	}
	for slug, vals := range previews {
		v := *vals
		if cat != nil && !stale[slug] {
			v = Project(slug, v, cat)
		}
		out, err := yaml.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", slug, err)
		}
		files["previews/"+slug+"/values.yaml"] = string(out)
	}
	maps.Copy(files, raw)
	remote := NewRemote(t, files)
	st, err := store.Open(
		context.Background(),
		store.CloneConfig{URL: remote.URL()},
		Identity,
		append([]store.Option{store.WithRepoURL(GitopsURL)}, opts...)...)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return remote, st
}
