package github_test

import (
	"testing"

	forgegithub "github.com/badilhq/badil/internal/forge/github"
)

// newURLForge returns the GitHub forge on webURL, for its URL rules only.
func newURLForge(t *testing.T, webURL string) *forgegithub.Forge {
	t.Helper()
	f, err := forgegithub.New(nil, nil, 0, webURL)
	if err != nil {
		t.Fatalf("New(%q): %v", webURL, err)
	}
	return f
}

func TestNew_InvalidWebURL(t *testing.T) {
	for _, webURL := range []string{
		"", "github.com", "https://github.com/acme", "https://u@github.com", "https://github.com?x", "ssh://github.com",
	} {
		if _, err := forgegithub.New(nil, nil, 0, webURL); err == nil {
			t.Errorf("New(%q) = nil error, want one", webURL)
		}
	}
}

// TestParseRepoURL carries over the catalog matching cases of the
// github.com-only rule: case, trailing "/" and ".git" ignored, SSH,
// enterprise and nested URLs rejected.
func TestParseRepoURL(t *testing.T) {
	cases := []struct {
		webURL, in          string
		wantOwner, wantRepo string
		wantOK              bool
	}{
		{"https://github.com", "https://github.com/acme/monorepo.git", "acme", "monorepo", true},
		{"https://github.com", "https://github.com/Acme/MonoRepo/", "acme", "monorepo", true},
		{"https://github.com", "https://github.com/acme/front", "acme", "front", true},
		{"https://github.com", "HTTPS://GITHUB.COM/ACME/WEB.GIT", "acme", "web", true},
		{"https://github.com", "https://github.com/acme/front.git/", "acme", "front", true},
		{"https://github.com", "git@github.com:acme/ssh.git", "", "", false},
		{"https://github.com", "https://github.acme.example/acme/ghe.git", "", "", false},
		{"https://github.com", "https://github.com/acme/group/nested", "", "", false},
		{"https://github.com", "https://github.com/acme", "", "", false},
		{"https://github.com", "https://github.com/acme/", "", "", false},
		{"https://github.com", "https://github.com//front", "", "", false},
		{"https://github.com", "http://github.com/acme/front", "", "", false},
		{"https://github.com", "https://github.community/acme/front", "", "", false},
		{"https://github.com", "", "", "", false},
		{"https://github.com/", "https://github.com/acme/front", "acme", "front", true},
		{"http://gitea.local:3000", "http://gitea.local:3000/acme/front.git", "acme", "front", true},
		{"http://gitea.local:3000", "http://Gitea.Local:3000/Acme/Front", "acme", "front", true},
		{"http://gitea.local:3000", "https://github.com/acme/front.git", "", "", false},
		{"http://gitea.local:3000", "http://gitea.local/acme/front.git", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.webURL+" "+tc.in, func(t *testing.T) {
			owner, repo, ok := newURLForge(t, tc.webURL).ParseRepoURL(tc.in)
			if owner != tc.wantOwner || repo != tc.wantRepo || ok != tc.wantOK {
				t.Errorf("ParseRepoURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.in, owner, repo, ok, tc.wantOwner, tc.wantRepo, tc.wantOK)
			}
		})
	}
}

// TestParsePRURL carries over the cases of the github.com-only PR URL rule.
func TestParsePRURL(t *testing.T) {
	cases := []struct {
		webURL, in string
		wantOwner  string
		wantRepo   string
		wantNum    int
		wantOK     bool
	}{
		{"https://github.com", "https://github.com/acme/backend-1/pull/42", "acme", "backend-1", 42, true},
		{"https://github.com", "https://github.com/acme/backend-1/pull/42/files", "acme", "backend-1", 42, true},
		{"https://github.com", "https://github.com/Acme/Backend-1/pull/42", "Acme", "Backend-1", 42, true},
		{"https://github.com", "https://gitlab.com/acme/backend-1/pull/42", "", "", 0, false},
		{"https://github.com", "https://github.com/acme/backend-1/pull/", "", "", 0, false},
		{"https://github.com", "https://github.com/acme/backend-1/pulls/42", "", "", 0, false},
		{
			"https://github.com", "https://github.com/acme/backend-1/pull/99999999999999999999",
			"", "", 0, false,
		},
		{"https://github.com", "HTTPS://GITHUB.COM/acme/backend-1/pull/42", "", "", 0, false},
		{"https://github.com", "acme/front#7", "", "", 0, false},
		{"https://github.com", "", "", "", 0, false},
		{"http://gitea.local:3000", "http://gitea.local:3000/acme/front/pull/7", "acme", "front", 7, true},
		{"http://gitea.local:3000", "https://github.com/acme/front/pull/7", "", "", 0, false},
		{"http://gitea.local:3000", "http://gitea.localx3000/acme/front/pull/7", "", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.webURL+" "+tc.in, func(t *testing.T) {
			owner, repo, num, ok := newURLForge(t, tc.webURL).ParsePRURL(tc.in)
			if owner != tc.wantOwner || repo != tc.wantRepo || num != tc.wantNum || ok != tc.wantOK {
				t.Errorf("ParsePRURL(%q) = (%q, %q, %d, %v), want (%q, %q, %d, %v)",
					tc.in, owner, repo, num, ok, tc.wantOwner, tc.wantRepo, tc.wantNum, tc.wantOK)
			}
		})
	}
}
