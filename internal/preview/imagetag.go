package preview

import "regexp"

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
