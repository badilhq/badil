package preview

import "strings"

// PRURLParser returns the owner, repo and number of a pull request URL of the
// forge, ok false for any other URL: forge.Forge.ParsePRURL.
type PRURLParser func(string) (owner, repo string, number int, ok bool)

// MatchesPR reports whether prURL is a pull request URL (parse) of
// owner/repo#number, owner and repo compared case-insensitively.
func MatchesPR(parse PRURLParser, prURL, owner, repo string, number int) bool {
	o, r, n, ok := parse(prURL)
	return ok && n == number && strings.EqualFold(o, owner) && strings.EqualFold(r, repo)
}
