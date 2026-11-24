// Package apiclient is the HTTP client of badil's API that the badil CLI uses.
package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/badilhq/badil/internal/api"
)

// maxBodyBytes caps the body of an answer of the read API.
const maxBodyBytes = 8 << 20

// PreviewsPath is the route of the list of previews (GET /v1/previews).
const PreviewsPath = "/v1/previews"

// Client calls badil's API at a server's base URL, with a token.
type Client struct {
	server string
	token  string
}

// New returns a client of the badil at the base URL server, that sends token
// as a bearer token when it is set.
func New(server, token string) *Client {
	return &Client{server: server, token: token}
}

// Get sends GET path to the server with the token, within ctx, and returns
// the answer's status and body.
func (c *Client) Get(ctx context.Context, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.server, "/")+path, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// Wait requests path until an answer in a state final reports final, an
// error answer that is not a 5xx, or ctx's end, retrying request errors and
// 5xx answers every interval and passing every other 200 answer to progress.
// It returns the final answer and its body, or, on ctx's end, a
// *TimeoutError.
func (c *Client) Wait(
	ctx context.Context,
	path string,
	interval time.Duration,
	final func(state string) bool,
	progress func(resp *api.WaitResponse),
) (*api.WaitResponse, []byte, error) {
	var lastBody []byte
	var lastErr error
	for {
		status, body, err := c.Get(ctx, path)
		switch {
		case ctx.Err() != nil:
			return nil, nil, &TimeoutError{LastBody: lastBody, LastErr: lastErr}
		case err != nil:
			lastErr = err
		case status >= http.StatusInternalServerError:
			lastErr = AnswerError(status, body)
		case status != http.StatusOK:
			return nil, nil, AnswerError(status, body)
		default:
			var resp api.WaitResponse
			if err = json.Unmarshal(body, &resp); err != nil {
				return nil, nil, fmt.Errorf("parse wait response: %w", err)
			}
			lastErr, lastBody = nil, body
			if final(resp.State) {
				return &resp, body, nil
			}
			progress(&resp)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, nil, &TimeoutError{LastBody: lastBody, LastErr: lastErr}
		case <-time.After(interval):
		}
	}
}

// AnswerError returns the error of a non-2xx answer: its JSON error message,
// or its status and body.
func AnswerError(status int, body []byte) error {
	var e api.ErrorResponse
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("%d %s: %s", status, http.StatusText(status), e.Error)
	}
	return fmt.Errorf("%d %s: %s", status, http.StatusText(status), strings.TrimSpace(string(body)))
}

// SlugWaitPath returns the wait route of the preview slug
// (GET /v1/previews/{slug}/wait).
func SlugWaitPath(slug string) string {
	return "/v1/previews/" + url.PathEscape(slug) + "/wait"
}

// PRWaitPath returns the wait route of the PR pr of repo (owner/repo), at
// sha when it is set (GET /v1/wait).
func PRWaitPath(repo string, pr int, sha string) string {
	q := url.Values{"repo": {repo}, "pr": {strconv.Itoa(pr)}}
	if sha != "" {
		q.Set("sha", sha)
	}
	return "/v1/wait?" + q.Encode()
}

// TimeoutError is the error of a wait whose context ended: the body of its
// last 200 answer, and the error of its last request when that request
// failed or answered a 5xx.
type TimeoutError struct {
	LastBody []byte
	LastErr  error
}

func (e *TimeoutError) Error() string {
	if e.LastErr != nil {
		return "timed out: " + e.LastErr.Error()
	}
	return "timed out"
}

func (e *TimeoutError) Unwrap() error { return e.LastErr }
