package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/badilhq/badil/internal/api"
	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
)

// deployTimeout caps the upstream work for a single deploy. The catalog read,
// the pull request read, the gitops fetch and push (with its retries) and the fan-out comments add
// up to a few round-trips; 30s is generous without letting a stuck request
// pin a goroutine forever.
const deployTimeout = 30 * time.Second

// validateDeployRequest returns the first missing field's JSON tag, or "" if
// all are set.
func validateDeployRequest(r api.DeployRequest) string {
	switch {
	case r.Service == "":
		return "service"
	case r.CommitSha == "":
		return "commit_sha"
	case r.PRNumber == "":
		return "pr_number"
	case r.WorkflowRunURL == "":
		return "workflow_run_url"
	}
	return ""
}

// parsePRNumber returns s as a positive integer, ok false when it is not one.
func parsePRNumber(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), deployTimeout)
	defer cancel()

	var req api.DeployRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONError(ctx, w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if missing := validateDeployRequest(req); missing != "" {
		writeJSONError(ctx, w, http.StatusBadRequest, fmt.Sprintf("missing required field: %s", missing))
		return
	}

	prNumber, ok := parsePRNumber(req.PRNumber)
	if !ok {
		writeJSONError(ctx, w, http.StatusBadRequest, "invalid pr_number: "+req.PRNumber)
		return
	}

	logger := slog.With(
		slog.String("service", req.Service),
		slog.String("commit_sha", req.CommitSha),
		slog.String("pr_number", req.PRNumber),
	)

	out, err := s.app.Deploy(ctx, app.DeployRequest{
		Service:        req.Service,
		CommitSha:      req.CommitSha,
		PRNumber:       prNumber,
		WorkflowRunURL: req.WorkflowRunURL,
	})
	if out.Slug != "" {
		logger = logger.With(slog.String("slug", out.Slug))
	}
	if err != nil {
		writeDeployError(ctx, logger, w, req.Service, out, err)
		return
	}

	resp := api.DeployResponse{PreviewSlug: out.Slug, GitOpsCommitURL: out.Deployed.Commit.URL}
	if out.FanOutErr != nil {
		logger.WarnContext(ctx, "comment fan-out had failures", slog.String("err", out.FanOutErr.Error()))
		resp.Warnings = splitJoinedErr(out.FanOutErr)
	}

	logger.InfoContext(ctx, "deploy succeeded", slog.String("gitops_commit_url", out.Deployed.Commit.URL))
	writeJSON(ctx, w, http.StatusOK, resp)
}

// writeDeployError answers err, the error of the app.Deploy of service whose
// outcome is out, and logs the failures that are not the caller's.
func writeDeployError(
	ctx context.Context, logger *slog.Logger, w http.ResponseWriter, service string, out app.DeployOutcome, err error,
) {
	var catalogErr *app.CatalogReadError
	if errors.As(err, &catalogErr) {
		writeUpstreamError(ctx, logger, w, "fetch catalog", catalogErr.Err)
		return
	}
	if errors.Is(err, app.ErrServiceNotInCatalog) {
		writeJSONError(ctx, w, http.StatusNotFound,
			fmt.Sprintf("service %q not in catalog", service))
		return
	}
	if errors.Is(err, app.ErrServiceRepoNotOnForge) {
		writeJSONError(ctx, w, http.StatusBadRequest,
			fmt.Sprintf("service %s repo is not a repository of the forge", service))
		return
	}
	var prErr *app.PullRequestReadError
	if errors.As(err, &prErr) {
		writePullRequestReadError(ctx, logger, w, prErr)
		return
	}
	if errors.Is(err, lifecycle.ErrPreviewLimit) {
		msg := fmt.Sprintf("preview limit (%d) reached", out.MaxPreviews)
		logger.WarnContext(ctx, "deploy failed", slog.String("op", "preview limit"), slog.String("err", msg))
		writeJSONError(ctx, w, http.StatusConflict, msg)
		return
	}
	if errors.Is(err, lifecycle.ErrNamespaceInUse) {
		msg := fmt.Sprintf("namespace %s exists and is not a preview", out.Slug)
		logger.WarnContext(ctx, "deploy failed", slog.String("op", "namespace in use"), slog.String("err", msg))
		writeJSONError(ctx, w, http.StatusConflict, msg)
		return
	}
	var nsErr *lifecycle.NamespaceCheckError
	if errors.As(err, &nsErr) {
		logger.ErrorContext(ctx, "deploy failed", slog.String("op", "namespace check"), slog.String("err", err.Error()))
		writeJSONError(ctx, w, http.StatusBadGateway, err.Error())
		return
	}
	var checkErr *lifecycle.CapCheckError
	if errors.As(err, &checkErr) {
		writeUpstreamError(ctx, logger, w, checkErr.Op, checkErr.Err)
		return
	}
	writeUpstreamError(ctx, logger, w, "write preview", err)
}

// writePullRequestReadError answers a deploy whose pull request could not be
// read: 404 when the forge does not find it, 502 logged with op
// "read pull request" otherwise.
func writePullRequestReadError(
	ctx context.Context, logger *slog.Logger, w http.ResponseWriter, prErr *app.PullRequestReadError,
) {
	if errors.Is(prErr.Err, forge.ErrNotFound) {
		writeJSONError(ctx, w, http.StatusNotFound,
			fmt.Sprintf("pull request %s/%s#%d not found", prErr.Owner, prErr.Repo, prErr.Number))
		return
	}
	logger.ErrorContext(ctx, "deploy failed",
		slog.String("op", "read pull request"), slog.String("err", prErr.Err.Error()))
	writeJSONError(ctx, w, http.StatusBadGateway, fmt.Sprintf("read pull request: %v", prErr.Err))
}

// splitJoinedErr renders an errors.Join-style multi-error as one string per
// underlying cause so clients can show them in a list.
func splitJoinedErr(err error) []string {
	type unwrapMany interface{ Unwrap() []error }
	if u, ok := err.(unwrapMany); ok {
		errs := u.Unwrap()
		out := make([]string, 0, len(errs))
		for _, e := range errs {
			out = append(out, e.Error())
		}
		return out
	}
	return []string{err.Error()}
}

// writeUpstreamError maps store sentinel errors to HTTP status codes and
// emits a structured log line. ErrUnauthorized surfaces as 502 because it
// reflects upstream (GitHub) credentials, not the caller's.
func writeUpstreamError(ctx context.Context, logger *slog.Logger, w http.ResponseWriter, op string, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, lifecycle.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, lifecycle.ErrUnauthorized):
		status = http.StatusBadGateway
	}
	logger.ErrorContext(ctx, "deploy failed", slog.String("op", op), slog.String("err", err.Error()))
	writeJSONError(ctx, w, status, fmt.Sprintf("%s: %v", op, err))
}

func writeJSONError(ctx context.Context, w http.ResponseWriter, status int, msg string) {
	writeJSON(ctx, w, status, api.ErrorResponse{Error: msg})
}

func writeJSON(ctx context.Context, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.ErrorContext(ctx, "response encode failed", slog.String("err", err.Error()))
	}
}
