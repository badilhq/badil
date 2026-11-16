package preview

import (
	"regexp"
	"strings"
)

// DefaultImageTag is the image tag format when badil.yaml sets none: the
// full commit SHA.
const DefaultImageTag = "{sha}"

// MaxImageTagFormatLen is the longest image tag format: with {sha} expanded
// to a 40-character SHA, the tag stays within the 128 characters of an OCI
// tag.
const MaxImageTagFormatLen = 93

// imageTagFormat matches an image tag format: an optional prefix, one
// placeholder and an optional suffix, each part made of OCI tag characters.
var imageTagFormat = regexp.MustCompile(`^([a-zA-Z0-9_][a-zA-Z0-9_.-]*)?\{(sha|sha7|sha8)\}[a-zA-Z0-9_.-]*$`)

// ValidImageTagFormat reports whether format is a valid image tag format: an
// optional prefix, one of {sha}, {sha7} or {sha8}, an optional suffix, at most
// MaxImageTagFormatLen characters.
func ValidImageTagFormat(format string) bool {
	return len(format) <= MaxImageTagFormatLen && imageTagFormat.MatchString(format)
}

// shortSHA returns the first n characters of sha, or all of it when shorter.
func shortSHA(sha string, n int) string {
	return sha[:min(n, len(sha))]
}

// ExpandImageTag returns the image tag of format, a ValidImageTagFormat, for
// an entry pinned to commitSha, or for a branch-tracking entry when commitSha
// is empty: its placeholder becomes commitSha or its first 7 or 8 characters
// (all of it when shorter), or the Argo CD variable of the deployed revision,
// as the image tag table of docs/ARCHITECTURE.md shows.
func ExpandImageTag(format, commitSha string) string {
	prefix, rest, _ := strings.Cut(format, "{")
	placeholder, suffix, _ := strings.Cut(rest, "}")
	var sha string
	switch {
	case commitSha == "" && placeholder == "sha7":
		sha = "${ARGOCD_APP_REVISION_SHORT}"
	case commitSha == "" && placeholder == "sha8":
		sha = "${ARGOCD_APP_REVISION_SHORT_8}"
	case commitSha == "":
		sha = "${ARGOCD_APP_REVISION}"
	case placeholder == "sha7":
		sha = shortSHA(commitSha, 7)
	case placeholder == "sha8":
		sha = shortSHA(commitSha, 8)
	default:
		sha = commitSha
	}
	return prefix + sha + suffix
}
