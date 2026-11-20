package preview_test

import (
	"testing"

	"github.com/badilhq/badil/internal/preview"
)

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantOwner string
		wantRepo  string
		wantNum   int
		wantOK    bool
	}{
		{"valid", "https://github.com/acme/backend-1/pull/42", "acme", "backend-1", 42, true},
		{"trailing path", "https://github.com/acme/backend-1/pull/42/files", "acme", "backend-1", 42, true},
		{"non-github host", "https://gitlab.com/acme/backend-1/pull/42", "", "", 0, false},
		{"missing number", "https://github.com/acme/backend-1/pull/", "", "", 0, false},
		{"number overflow", "https://github.com/acme/backend-1/pull/99999999999999999999", "", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, num, ok := preview.ParsePRURL(tc.in)
			if owner != tc.wantOwner || repo != tc.wantRepo || num != tc.wantNum || ok != tc.wantOK {
				t.Errorf("ParsePRURL(%q) = (%q, %q, %d, %v), want (%q, %q, %d, %v)",
					tc.in, owner, repo, num, ok, tc.wantOwner, tc.wantRepo, tc.wantNum, tc.wantOK)
			}
		})
	}
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
			if got := preview.MatchesPR(tc.url, "acme", "front", 7); got != tc.want {
				t.Errorf("MatchesPR(%q, acme, front, 7) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
