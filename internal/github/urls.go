package github

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// DefaultAPIURL is the API base of GITHUB_API_URL when it is unset, the one
// go-github and ghinstallation default to.
const DefaultAPIURL = "https://api.github.com/"

// ParseWebURL validates raw as a GITHUB_URL: an https or http URL with a
// host, no user info, query or fragment, and no path other than "/". It
// returns raw without a trailing "/". Its error reads after the setting's
// name.
func ParseWebURL(raw string) (string, error) {
	u, err := parseBaseURL(raw)
	if err != nil {
		return "", err
	}
	if u.Path != "" && u.Path != "/" || u.RawPath != "" {
		return "", fmt.Errorf("must not have a path, got %q", u.EscapedPath())
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// ValidateAPIURL validates raw as a GITHUB_API_URL: an https or http URL with
// a host, no user info, query or fragment, and any path. Its error reads after
// the setting's name.
func ValidateAPIURL(raw string) error {
	_, err := parseBaseURL(raw)
	return err
}

// parseBaseURL parses raw as an https or http URL with a host, no user info,
// query or fragment. The URL stays out of its error: it may hold credentials.
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if ue, ok := errors.AsType[*url.Error](err); ok {
		err = ue.Err
	}
	switch {
	case err != nil:
		return nil, fmt.Errorf("must be a URL: %w", err)
	case u.Scheme != "https" && u.Scheme != "http":
		return nil, fmt.Errorf("must be an https or http URL, got scheme %q", u.Scheme)
	case u.Hostname() == "":
		return nil, errors.New("must have a host")
	case u.User != nil:
		return nil, errors.New("must not contain user info")
	case u.RawQuery != "" || u.ForceQuery:
		return nil, errors.New("must not have a query")
	case strings.Contains(raw, "#"):
		return nil, errors.New("must not have a fragment")
	}
	return u, nil
}
