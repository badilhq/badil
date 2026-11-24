package github_test

import (
	"testing"

	"github.com/badilhq/badil/internal/preview"
)

// parsePR is the default GitHub pull request URL rule.
func parsePR(t *testing.T) preview.PRURLParser {
	t.Helper()
	return newURLForge(t, "https://github.com").ParsePRURL
}

func TestMatchesPR(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"same", "https://github.com/acme/front/pull/7", true},
		{"letter case", "https://github.com/Acme/FRONT/pull/7", true},
		{"trailing path", "https://github.com/acme/front/pull/7/files", true},
		{"different number", "https://github.com/acme/front/pull/8", false},
		{"different repo", "https://github.com/acme/back/pull/7", false},
		{"different owner", "https://github.com/other/front/pull/7", false},
		{"unparsable", "acme/front#7", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := preview.MatchesPR(parsePR(t), tc.url, "acme", "front", 7); got != tc.want {
				t.Errorf("MatchesPR(%q, acme, front, 7) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestMatchesPR_CustomHost(t *testing.T) {
	parse := newURLForge(t, "http://gitea.local:3000").ParsePRURL
	if !preview.MatchesPR(parse, "http://gitea.local:3000/acme/front/pull/7", "acme", "front", 7) {
		t.Error("MatchesPR on gitea.local = false for its own PR URL, want true")
	}
	if preview.MatchesPR(parse, "https://github.com/acme/front/pull/7", "acme", "front", 7) {
		t.Error("MatchesPR on gitea.local = true for a github.com PR URL, want false")
	}
}
