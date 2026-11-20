package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

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
// teardown, sync and push handlers. Defined here so tests can inject fakes.
type storeAPI interface {
	lifecycle.ReconcileStore
	lifecycle.SyncStore
	FullName() string
	// GetPreview reads previews/<slug>/values.yaml, store.ErrNotFound when
	// it does not exist.
	GetPreview(ctx context.Context, slug string) (*preview.PreviewValues, string, error)
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

	slug := preview.Slugify(req.HeadRef)
	logger := slog.With(
		slog.String("service", req.Service),
		slog.String("slug", slug),
		slog.String("commit_sha", req.CommitSha),
		slog.String("pr_number", req.PRNumber),
	)

	services, err := s.store.FetchServiceCatalog(ctx)
	if err != nil {
		writeUpstreamError(ctx, logger, w, "fetch catalog", err)
		return
	}
	if !slices.Contains(services.Services, req.Service) {
		writeJSONError(ctx, w, http.StatusNotFound,
			fmt.Sprintf("service %q not in catalog", req.Service))
		return
	}

	inputs := preview.DeployInputs{
		GitopsRepo:     "", // not used downstream; carry-over from earlier shape
		ServiceName:    req.Service,
		HeadRef:        req.HeadRef,
		CommitSha:      req.CommitSha,
		PRAuthor:       req.PRAuthor,
		PRURL:          req.PRURL,
		PRNumber:       req.PRNumber,
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		WorkflowRunURL: req.WorkflowRunURL,
	}

	// A manual deploy always writes: it refreshes updated-at (the TTL clock)
	// and always returns a gitops commit URL. It is refused with 409 when the
	// preview does not exist and preview.maxPreviews previews already do.
	result, fanErr, err := lifecycle.DeployPR(ctx, s.deployAPI(), services, slug, inputs)
	if errors.Is(err, lifecycle.ErrPreviewLimit) {
		msg := fmt.Sprintf("preview limit (%d) reached", services.MaxPreviews)
		logger.WarnContext(ctx, "deploy failed", slog.String("op", "preview limit"), slog.String("err", msg))
		writeJSONError(ctx, w, http.StatusConflict, msg)
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

	resp := DeployResponse{PreviewSlug: slug, GitOpsCommitURL: result.Commit.URL}
	if fanErr != nil {
		logger.WarnContext(ctx, "comment fan-out had failures", slog.String("err", fanErr.Error()))
		resp.Warnings = splitJoinedErr(fanErr)
	}

	logger.InfoContext(ctx, "deploy succeeded", slog.String("gitops_commit_url", result.Commit.URL))
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
