package github

import (
	"strconv"
	"strings"
)

// ParseRepoURL returns the owner and repo, lowercased, of u when u, lowercased
// and with a trailing "/" then ".git" removed, is <web URL>/<owner>/<repo>:
// two non-empty segments after the forge's web URL, lowercased. Any other URL
// (another host, SSH, a deeper path) returns ok false.
func (f *Forge) ParseRepoURL(u string) (string, string, bool) {
	path, ok := strings.CutPrefix(normalizeRepoURL(u), strings.ToLower(f.webURL)+"/")
	if !ok {
		return "", "", false
	}
	owner, repo, ok := strings.Cut(path, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}
	return owner, repo, true
}

// ParsePRURL returns the owner, repo and number of u when u starts with
// <web URL>/<owner>/<repo>/pull/<number>, scheme and host compared
// case-sensitively. Trailing path segments (e.g. /files) are ignored.
func (f *Forge) ParsePRURL(u string) (string, string, int, bool) {
	m := f.prURL.FindStringSubmatch(u)
	if m == nil {
		return "", "", 0, false
	}
	number, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, false
	}
	return m[1], m[2], number, true
}

// normalizeRepoURL lowercases url and removes a trailing "/" then ".git".
func normalizeRepoURL(url string) string {
	u := strings.ToLower(url)
	u = strings.TrimSuffix(u, "/")
	return strings.TrimSuffix(u, ".git")
}
