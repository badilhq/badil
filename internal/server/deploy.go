package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/badilhq/badil/internal/app"
	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/preview"
	"github.com/badilhq/badil/internal/store"
)

// deployTimeout caps the upstream work for a single deploy. The catalog read,
// the gitops fetch and push (with its retries) and the fan-out comments add
// up to a few round-trips; 30s is generous without letting a stuck request
// pin a goroutine forever.
const deployTimeout = 30 * time.Second

// storeAPI is the subset of store.GitHubStore used by the deploy, reconcile,
// teardown, sync, push and read handlers. Defined here so tests can inject
// fakes.
type storeAPI interface {
	lifecycle.ReconcileStore
	lifecycle.SyncStore
	FullName() string
	// ListPreviewFiles lists the slugs of the previews whose values.yaml is
	// in the gitops repo, sorted.
	ListPreviewFiles(ctx context.Context) ([]string, error)
	// GetPreview reads previews/<slug>/values.yaml, store.ErrNotFound when
	// it does not exist.
	GetPreview(ctx context.Context, slug string) (*preview.Values, string, error)
	// Invalidate makes the next read check the gitops remote at once.
	Invalidate()
}

// DeployRequest is the body of POST /v1/deploy. All fields are required; the
// server stamps timestamp from time.Now() so retries don't drift.
type DeployRequest struct {
	Service        string `json:"service"`
	HeadRef        string `json:"head_ref"`
	CommitSha      string `json:"commit_sha"`
	PRAuthor       string `json:"pr_author"`
	PRURL          string `json:"pr_url"`
	PRNumber       string `json:"pr_number"`
	WorkflowRunURL string `json:"workflow_run_url"`
}

// DeployResponse is the success body. Warnings is populated when the values
// write succeeded but the comment fan-out had partial failures.
type DeployResponse struct {
	PreviewSlug     string   `json:"preview_slug"`
	GitOpsCommitURL string   `json:"gitops_commit_url"`
	Warnings        []string `json:"warnings,omitempty"`
}

// ErrorResponse is the failure body for every non-2xx response.
type ErrorResponse struct {
	Error string `json:"error"`
}

// validate returns the first missing field's JSON tag, or "" if all are set.
func (r DeployRequest) validate() string {
	switch {
	case r.Service == "":
		return "service"
	case r.HeadRef == "":
		return "head_ref"
	case r.CommitSha == "":
		return "commit_sha"
	case r.PRAuthor == "":
		return "pr_author"
	case r.PRURL == "":
		return "pr_url"
	case r.PRNumber == "":
		return "pr_number"
	case r.WorkflowRunURL == "":
		return "workflow_run_url"
	}
	return ""
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), deployTimeout)
	defer cancel()

	var req DeployRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONError(ctx, w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if missing := req.validate(); missing != "" {
		writeJSONError(ctx, w, http.StatusBadRequest, fmt.Sprintf("missing required field: %s", missing))
		return
	}

	dr := app.DeployRequest(req)
	slug := dr.Slug()
	logger := slog.With(
		slog.String("service", req.Service),
		slog.String("slug", slug),
		slog.String("commit_sha", req.CommitSha),
		slog.String("pr_number", req.PRNumber),
	)

	out, err := s.app.Deploy(ctx, dr)
	var catalogErr *app.CatalogReadError
	if errors.As(err, &catalogErr) {
		writeUpstreamError(ctx, logger, w, "fetch catalog", catalogErr.Err)
		return
	}
	if errors.Is(err, app.ErrServiceNotInCatalog) {
		writeJSONError(ctx, w, http.StatusNotFound,
			fmt.Sprintf("service %q not in catalog", req.Service))
		return
	}
	if errors.Is(err, lifecycle.ErrPreviewLimit) {
		msg := fmt.Sprintf("preview limit (%d) reached", out.MaxPreviews)
		logger.WarnContext(ctx, "deploy failed", slog.String("op", "preview limit"), slog.String("err", msg))
		writeJSONError(ctx, w, http.StatusConflict, msg)
		return
	}
	if errors.Is(err, lifecycle.ErrNamespaceInUse) {
		msg := fmt.Sprintf("namespace %s exists and is not a preview", slug)
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
	if err != nil {
		writeUpstreamError(ctx, logger, w, "write preview", err)
		return
	}

	resp := DeployResponse{PreviewSlug: slug, GitOpsCommitURL: out.Deployed.Commit.URL}
	if out.FanOutErr != nil {
		logger.WarnContext(ctx, "comment fan-out had failures", slog.String("err", out.FanOutErr.Error()))
		resp.Warnings = splitJoinedErr(out.FanOutErr)
	}

	logger.InfoContext(ctx, "deploy succeeded", slog.String("gitops_commit_url", out.Deployed.Commit.URL))
	writeJSON(ctx, w, http.StatusOK, resp)
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
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrUnauthorized):
		status = http.StatusBadGateway
	}
	logger.ErrorContext(ctx, "deploy failed", slog.String("op", op), slog.String("err", err.Error()))
	writeJSONError(ctx, w, status, fmt.Sprintf("%s: %v", op, err))
}

func writeJSONError(ctx context.Context, w http.ResponseWriter, status int, msg string) {
	writeJSON(ctx, w, status, ErrorResponse{Error: msg})
}

func writeJSON(ctx context.Context, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.ErrorContext(ctx, "response encode failed", slog.String("err", err.Error()))
	}
}
