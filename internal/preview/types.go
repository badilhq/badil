package preview

// APIVersion is the apiVersion of the preview values contract badil writes.
const APIVersion = "badil.dev/v1beta1"

// DefaultBranch is the branch a service tracks when the catalog sets none.
const DefaultBranch = "main"

// Chart settings a service gets when the catalog does not set them.
const (
	DefaultChartPath     = "k8s/"
	DefaultImageTagParam = "app.image.tag"
	DefaultHostParam     = "app.ingress.host"
)

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
// deployed at CommitSha. ImageTag is the image tag of the service, its
// format expanded (ExpandImageTag), and Chart how its chart is rendered.
// Host is the ingress host badil computed for an exposed service, and is
// empty otherwise.
type ServiceEntry struct {
	Name      string        `yaml:"name"`
	RepoURL   string        `yaml:"repoURL"`
	Branch    string        `yaml:"branch"`
	CommitSha string        `yaml:"commitSha,omitempty"`
	ImageTag  string        `yaml:"imageTag"`
	Chart     ChartSettings `yaml:"chart"`
	Host      string        `yaml:"host,omitempty"`
	Metadata  *Metadata     `yaml:"metadata,omitempty"`
}

// PreviewValues is the root document persisted at previews/<slug>/values.yaml.
// Namespace is the extra metadata of the preview Namespace, nil when
// badil.yaml sets none.
type PreviewValues struct {
	APIVersion string             `yaml:"apiVersion"`
	Namespace  *NamespaceMetadata `yaml:"namespace,omitempty"`
	Services   []ServiceEntry     `yaml:"services"`
}

// ChartSettings is how a service's chart is rendered: the chart path in the
// service repo, the Helm parameters set to the image tag and to the preview
// host, and extra values files applied in order. In a preview file every
// field but ValueFiles is set.
type ChartSettings struct {
	Path          string   `yaml:"path"`
	ImageTagParam string   `yaml:"imageTagParam"`
	HostParam     string   `yaml:"hostParam"`
	ValueFiles    []string `yaml:"valueFiles,omitempty"`
}

// NamespaceMetadata is the extra labels and annotations of the preview
// Namespace.
type NamespaceMetadata struct {
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

// ReservedNamespaceLabels returns the preview Namespace labels the
// preview-env chart sets, which NamespaceMetadata.Labels cannot hold.
func ReservedNamespaceLabels() []string {
	return []string{
		"app.kubernetes.io/name",
		"app.kubernetes.io/instance",
		"app.kubernetes.io/part-of",
		"app.kubernetes.io/managed-by",
	}
}

// ReservedNamespaceAnnotations returns the preview Namespace annotations the
// preview-env chart sets, which NamespaceMetadata.Annotations cannot hold.
func ReservedNamespaceAnnotations() []string {
	return []string{"argocd.argoproj.io/sync-wave"}
}
