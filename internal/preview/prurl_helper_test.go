package preview_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/preview"
)

// prForge parses the pull request URLs of a forge on webURL, for tests that
// need a preview.PRURLParser without a forge adapter.
type prForge struct {
	webURL string
}

// ParsePRURL returns the owner, repo and number of u when u is exactly
// <webURL>/<owner>/<repo>/pull/<n>, n a positive integer; ok false for any
// other URL.
func (f prForge) ParsePRURL(u string) (string, string, int, bool) {
	path, ok := strings.CutPrefix(u, f.webURL+"/")
	if !ok {
		return "", "", 0, false
	}
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", "", 0, false
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number < 1 {
		return "", "", 0, false
	}
	return parts[0], parts[1], number, true
}

// githubForge returns the pull request URL rule of a forge on webURL.
func githubForge(t *testing.T, webURL string) prForge {
	t.Helper()
	return prForge{webURL: webURL}
}

// parsePR is the pull request URL rule on https://github.com.
func parsePR(t *testing.T) preview.PRURLParser {
	t.Helper()
	return githubForge(t, "https://github.com").ParsePRURL
}
