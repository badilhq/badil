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

// Lengths of the short SHAs of {sha7} and {sha8}.
const (
	sha7Len = 7
	sha8Len = 8
)

// ExpandImageTag returns the image tag of format for an entry pinned to
// commitSha, or for a branch-tracking entry when commitSha is empty:
//
//	placeholder  pinned                       branch-tracking
//	{sha}        commitSha                    ${ARGOCD_APP_REVISION}
//	{sha7}       first 7 chars of commitSha   ${ARGOCD_APP_REVISION_SHORT}
//	{sha8}       first 8 chars of commitSha   ${ARGOCD_APP_REVISION_SHORT_8}
//
// A short SHA is all of commitSha when it is shorter. Argo CD substitutes the
// branch-tracking variables with the deployed revision. An empty format is
// DefaultImageTag; any other invalid format (ValidImageTagFormat) is returned
// as is.
func ExpandImageTag(format, commitSha string) string {
	if format == "" {
		format = DefaultImageTag
	}
	if !ValidImageTagFormat(format) {
		return format
	}
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
		sha = shortSHA(commitSha, sha7Len)
	case placeholder == "sha8":
		sha = shortSHA(commitSha, sha8Len)
	default:
		sha = commitSha
	}
	return prefix + sha + suffix
}
