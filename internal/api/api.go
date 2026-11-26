// Package api holds the JSON bodies of badil's HTTP API, shared by the server and its clients.
package api

// DeployRequest is the body of POST /v1/deploy. All fields are required;
// PRNumber is a positive integer. The server reads the PR's head branch,
// author and URL from the forge, and stamps timestamp from time.Now() so
// retries don't drift.
type DeployRequest struct {
	Service        string `json:"service"`
	CommitSha      string `json:"commit_sha"`
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

// PreviewList is the body of GET /v1/previews.
type PreviewList struct {
	Previews []PreviewJSON `json:"previews"`
}

// PreviewJSON is a preview in the read API: its file in the gitops clone and
// its status in memory. CreatedAt and UpdatedAt are the oldest created-at and
// the newest updated-at of its pinned entries, and ExpiresAt is UpdatedAt
// plus the in-server sweep's TTL; each is omitted when unknown.
type PreviewJSON struct {
	Slug      string             `json:"slug"`
	CreatedAt string             `json:"createdAt,omitempty"`
	UpdatedAt string             `json:"updatedAt,omitempty"`
	ExpiresAt string             `json:"expiresAt,omitempty"`
	Services  []PreviewEntryJSON `json:"services"`
}

// PreviewEntryJSON is a service or dependency entry of a preview.
type PreviewEntryJSON struct {
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Pinned   *PinnedJSON `json:"pinned,omitempty"`
	Branch   string      `json:"branch,omitempty"`
	URL      string      `json:"url,omitempty"`
	Status   string      `json:"status"`
	Revision string      `json:"revision,omitempty"`
	StatusAt string      `json:"statusAt,omitempty"`
}

// PinnedJSON is the pin of a pinned entry: its commit and the PR that pinned
// it.
type PinnedJSON struct {
	SHA    string `json:"sha"`
	PR     PRJSON `json:"pr"`
	Author string `json:"author"`
}

// PRJSON is a pull request: owner/repo, number and URL, from the pr-url of
// the entry.
type PRJSON struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// StatusEvent is the body of POST /webhooks/status: the status a renderer
// reports for one service of a preview, at a revision.
type StatusEvent struct {
	APIVersion string `json:"apiVersion"`
	Preview    string `json:"preview"`
	Service    string `json:"service"`
	Revision   string `json:"revision"`
	Status     string `json:"status"`
	At         string `json:"at"`
}

// SyncRequest is the optional body of POST /v1/sync. Without a slug, every
// preview is synced.
type SyncRequest struct {
	Slug *string `json:"slug"`
}

// SyncResponse is the success body of POST /v1/sync: the slugs rewritten, the
// number of previews left unchanged and the slugs that failed.
type SyncResponse struct {
	Synced    []string `json:"synced"`
	Unchanged int      `json:"unchanged"`
	Failed    []string `json:"failed"`
}

// WaitResponse is the body of a wait's answer: its state, the reason of a
// failed, refused or skipped state, the preview and the SHA waited for when
// known, and the wait set's entries with the URLs of those that have one.
type WaitResponse struct {
	State    string            `json:"state"`
	Reason   string            `json:"reason,omitempty"`
	Slug     string            `json:"slug,omitempty"`
	SHA      string            `json:"sha,omitempty"`
	Services []WaitEntryJSON   `json:"services"`
	URLs     map[string]string `json:"urls"`
}

// WaitEntryJSON is an entry of a wait set: its latest status ("unknown"
// when none) and revision, whether it is ready, and its URL.
type WaitEntryJSON struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
	Ready    bool   `json:"ready"`
	URL      string `json:"url,omitempty"`
}
