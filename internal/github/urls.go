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
// returns the canonical base: scheme and host lowercased, the scheme's
// default port (443, 80) dropped and no trailing "/", the form of the URLs
// the host itself writes, which the case-sensitive PR URL rule compares
// against. Its error reads after the setting's name.
func ParseWebURL(raw string) (string, error) {
	u, err := ParseBaseURL(raw)
	if err != nil {
		return "", err
	}
	if u.Path != "" && u.Path != "/" || u.RawPath != "" {
		return "", fmt.Errorf("must not have a path, got %q", u.EscapedPath())
	}
	host := strings.ToLower(u.Host)
	if port := u.Port(); port == "" || port == defaultPorts[u.Scheme] {
		host = strings.TrimSuffix(strings.TrimSuffix(host, port), ":")
	}
	return u.Scheme + "://" + host, nil
}

// defaultPorts are the ports ParseWebURL drops, by scheme.
var defaultPorts = map[string]string{"https": "443", "http": "80"}

// ValidateAPIURL validates raw as a GITHUB_API_URL: an https or http URL with
// a host, no user info, query or fragment, and any path. Its error reads after
// the setting's name.
func ValidateAPIURL(raw string) error {
	_, err := ParseBaseURL(raw)
	return err
}

// ErrUserInfo is ParseBaseURL's error for a URL with user info.
var ErrUserInfo = errors.New("must not contain user info")

// ParseBaseURL parses raw as an https or http URL with a host, no user info
// (ErrUserInfo), query or fragment. The URL stays out of its error: it may
// hold credentials. Its error reads after the setting's name.
func ParseBaseURL(raw string) (*url.URL, error) {
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
		return nil, ErrUserInfo
	case u.RawQuery != "" || u.ForceQuery:
		return nil, errors.New("must not have a query")
	case strings.Contains(raw, "#"):
		return nil, errors.New("must not have a fragment")
	}
	return u, nil
}
