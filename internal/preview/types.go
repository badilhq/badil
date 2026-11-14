package preview

// APIVersion is the apiVersion of the preview values contract badil writes.
const APIVersion = "badil.dev/v1"

// DefaultBranch is the branch a service tracks when the catalog sets none.
const DefaultBranch = "main"

// DeployInputs collects the per-deploy values needed to build or update a
// preview environment's values.yaml.
type DeployInputs struct {
	GitopsRepo     string // "owner/repo"
	ServiceName    string
	HeadRef        string
	CommitSha      string
	PRAuthor       string
	PRURL          string
	PRNumber       string
	Timestamp      string
	WorkflowRunURL string
}

// ServiceEntry is one item in PreviewValues.Services. RepoURL and Branch are
// the service's git URL and the branch it tracks when not pinned. CommitSha
// and Metadata are set together, only on a pinned entry: the service is then
// deployed at CommitSha, which is also its image tag. Host is the ingress
// host badil computed for an exposed service, and is empty otherwise.
type ServiceEntry struct {
	Name      string    `yaml:"name"`
	RepoURL   string    `yaml:"repoURL"`
	Branch    string    `yaml:"branch"`
	CommitSha string    `yaml:"commitSha,omitempty"`
	Host      string    `yaml:"host,omitempty"`
	Metadata  *Metadata `yaml:"metadata,omitempty"`
}

// PreviewValues is the root document persisted at previews/<slug>/values.yaml.
type PreviewValues struct {
	APIVersion string         `yaml:"apiVersion"`
	Services   []ServiceEntry `yaml:"services"`
}
