package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/badilhq/badil/internal/api"
	"github.com/badilhq/badil/internal/apiclient"
	"github.com/badilhq/badil/internal/preview"
)

// Exit codes of badil wait.
const (
	exitFailed  = 1
	exitTimeout = 2
	exitError   = 3
)

// defaultWaitTimeout is badil wait's default --timeout.
const defaultWaitTimeout = 15 * time.Minute

// retryInterval is the time between two requests of badil wait after a
// connection error or a 5xx answer.
var retryInterval = 2 * time.Second //nolint:gochecknoglobals // shortened by tests

// tablePadding is the space between two columns of badil list's table.
const tablePadding = 2

// codeError is an error that exits badil with code.
type codeError struct {
	code int
	err  error
}

func (e *codeError) Error() string { return e.err.Error() }

func (e *codeError) Unwrap() error { return e.err }

// exitCode returns the exit code of err: a codeError's code, else 1.
func exitCode(err error) int {
	if ce, ok := errors.AsType[*codeError](err); ok {
		return ce.code
	}
	return 1
}

// clientFlags are the flags of the commands that call the read API.
type clientFlags struct {
	server string
	token  string
	json   bool
}

// addClientFlags registers f's flags on cmd.
func addClientFlags(cmd *cobra.Command, f *clientFlags) {
	cmd.Flags().
		StringVar(&f.server, "server", "", "base URL of badil, e.g. https://badil.example.com (env BADIL_SERVER)")
	cmd.Flags().StringVar(&f.token, "token", "", "read or deploy token of badil (env BADIL_TOKEN)")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the response body")
}

// resolve fills the flags left empty from BADIL_SERVER and BADIL_TOKEN, and
// returns an error when the server is still unset.
func (f *clientFlags) resolve() error {
	if f.server == "" {
		f.server = os.Getenv("BADIL_SERVER")
	}
	if f.token == "" {
		f.token = os.Getenv("BADIL_TOKEN")
	}
	if f.server == "" {
		return errors.New("--server or BADIL_SERVER is required")
	}
	return nil
}

// client returns the API client of f's server and token.
func (f *clientFlags) client() *apiclient.Client {
	return apiclient.New(f.server, f.token)
}

// listCmd prints the previews of badil (GET /v1/previews).
func listCmd() *cobra.Command {
	var f clientFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List previews and their status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := f.resolve(); err != nil {
				return err
			}
			status, body, err := f.client().Get(cmd.Context(), apiclient.PreviewsPath)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return apiclient.AnswerError(status, body)
			}
			if f.json {
				_, err = cmd.OutOrStdout().Write(body)
				return err
			}
			var list api.PreviewList
			if err = json.Unmarshal(body, &list); err != nil {
				return fmt.Errorf("parse previews: %w", err)
			}
			return printPreviews(cmd.OutOrStdout(), list)
		},
	}
	addClientFlags(cmd, &f)
	return cmd
}

// printPreviews prints list as a table: each preview's slug, its number of
// services and dependencies, its pinned entries, how many of them are ready
// at their pin, and its updatedAt.
func printPreviews(w io.Writer, list api.PreviewList) error {
	tw := tabwriter.NewWriter(w, 0, 0, tablePadding, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tSERVICES\tPINNED\tREADY\tUPDATED")
	for _, p := range list.Previews {
		var pinned []string
		ready := 0
		for _, e := range p.Services {
			if e.Pinned == nil {
				continue
			}
			pinned = append(pinned, e.Name)
			if e.Status == "ready" && preview.AtPin(e.Revision, e.Pinned.SHA) {
				ready++
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d/%d\t%s\n",
			p.Slug, len(p.Services), orDash(strings.Join(pinned, ",")), ready, len(pinned), orDash(p.UpdatedAt))
	}
	return tw.Flush()
}

// orDash returns s, or "-" when it is empty.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// waitFlags are the flags of badil wait.
type waitFlags struct {
	clientFlags

	repo    string
	pr      int
	sha     string
	slug    string
	timeout time.Duration
}

// path returns the wait route of f's target, or an error when f does not
// set exactly one target form: --repo and --pr (with an optional --sha), or
// --slug.
func (f *waitFlags) path() (string, error) {
	byPR := f.repo != "" || f.pr != 0 || f.sha != ""
	switch {
	case byPR && f.slug != "":
		return "", errors.New("use --repo, --pr and --sha, or --slug, not both")
	case f.slug != "":
		return apiclient.SlugWaitPath(f.slug), nil
	case f.repo == "" || f.pr <= 0:
		return "", errors.New("--repo and a positive --pr, or --slug, are required")
	}
	return apiclient.PRWaitPath(f.repo, f.pr, f.sha), nil
}

// waitCmd waits for a preview to reach a final state (GET /v1/wait or GET
// /v1/previews/{slug}/wait), until --timeout.
func waitCmd() *cobra.Command {
	var f waitFlags
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Wait for a PR's or a slug's preview to be ready",
		Long: `Wait for a PR's preview (--repo, --pr and --sha) or a preview (--slug) to be
ready, until --timeout.

Exit codes: 0 ready or skipped; 1 failed or refused; 2 the timeout passed while
pending or progressing; 3 not found, unauthorized, an invalid query, bad flags,
or the timeout passed while the last request failed.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return &codeError{code: exitError, err: err}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := f.path()
			if err == nil {
				err = f.resolve()
			}
			if err == nil && f.timeout <= 0 {
				err = errors.New("--timeout must be positive")
			}
			if err != nil {
				return &codeError{code: exitError, err: err}
			}
			return runWait(cmd, &f, path)
		},
	}
	addClientFlags(cmd, &f.clientFlags)
	cmd.Flags().StringVar(&f.repo, "repo", "", "the PR's repository, owner/repo")
	cmd.Flags().IntVar(&f.pr, "pr", 0, "the PR's number")
	cmd.Flags().StringVar(&f.sha, "sha", "", "the commit to wait for, full or a prefix (default: the PR's current pin)")
	cmd.Flags().StringVar(&f.slug, "slug", "", "the preview to wait for, instead of a PR")
	cmd.Flags().DurationVar(&f.timeout, "timeout", defaultWaitTimeout, "how long to wait")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &codeError{code: exitError, err: err}
	})
	return cmd
}

// runWait requests path until a final state, a 4xx answer or f.timeout,
// retrying connection errors and 5xx answers every retryInterval, and
// prints the states as it goes.
func runWait(cmd *cobra.Command, f *waitFlags, path string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), f.timeout)
	defer cancel()
	out := cmd.OutOrStdout()
	var last *api.WaitResponse
	final := func(state string) bool {
		_, done := waitFinal(state)
		return done
	}
	progress := func(resp *api.WaitResponse) {
		if !f.json && (last == nil || last.State != resp.State) {
			fmt.Fprintln(out, stateLine(resp))
		}
		last = resp
	}
	resp, body, err := f.client().Wait(ctx, path, retryInterval, final, progress)
	if te, ok := errors.AsType[*apiclient.TimeoutError](err); ok {
		return waitTimedOut(cmd, f, te.LastBody, te.LastErr)
	}
	if err != nil {
		return &codeError{code: exitError, err: err}
	}
	code, _ := waitFinal(resp.State)
	return finishWait(cmd, f, resp, body, code)
}

// waitFinal returns the exit code of state and true when it is final.
func waitFinal(state string) (int, bool) {
	switch state {
	case "ready", "skipped":
		return 0, true
	case "failed", "refused":
		return exitFailed, true
	}
	return 0, false
}

// finishWait prints the final response resp (its body with --json) and
// returns the error of its exit code, nil for 0.
func finishWait(cmd *cobra.Command, f *waitFlags, resp *api.WaitResponse, body []byte, code int) error {
	out := cmd.OutOrStdout()
	if f.json {
		if _, err := out.Write(body); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(out, stateLine(resp))
		for _, name := range slices.Sorted(maps.Keys(resp.URLs)) {
			fmt.Fprintf(out, "  %s: %s\n", name, resp.URLs[name])
		}
	}
	if code == 0 {
		return nil
	}
	cmd.SilenceErrors = true
	return &codeError{code: code, err: errors.New(stateLine(resp))}
}

// waitTimedOut ends a wait whose timeout passed: exit 3 when the last
// request failed with lastErr, else exit 2 after printing the last response
// body with --json.
func waitTimedOut(cmd *cobra.Command, f *waitFlags, lastBody []byte, lastErr error) error {
	if lastErr != nil {
		return &codeError{code: exitError, err: fmt.Errorf("timed out after %s: %w", f.timeout, lastErr)}
	}
	if f.json && lastBody != nil {
		if _, err := cmd.OutOrStdout().Write(lastBody); err != nil {
			return err
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "timed out after %s\n", f.timeout)
	cmd.SilenceErrors = true
	return &codeError{code: exitTimeout, err: fmt.Errorf("timed out after %s", f.timeout)}
}

// stateLine is the line printed for resp: its state, with its reason when
// set.
func stateLine(resp *api.WaitResponse) string {
	if resp.Reason != "" {
		return resp.State + ": " + resp.Reason
	}
	return resp.State
}
