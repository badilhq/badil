package preview

import "path"

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
// GitopsRepoURL is the git URL of the gitops repo, from which a dependency
// reads its valueFiles. Namespace is the extra metadata of the preview
// Namespace, nil when badil.yaml sets none. Dependencies are the upstream
// charts of badil.yaml, in its order.
type PreviewValues struct {
	APIVersion    string             `yaml:"apiVersion"`
	GitopsRepoURL string             `yaml:"gitopsRepoURL"`
	Namespace     *NamespaceMetadata `yaml:"namespace,omitempty"`
	Services      []ServiceEntry     `yaml:"services"`
	Dependencies  []DependencyEntry  `yaml:"dependencies"`
}

// DependencyEntry is one item in PreviewValues.Dependencies: an upstream chart
// deployed in every preview, never pinned and without an image tag. Values
// are passed to the chart as an object and ValueFiles are paths in the gitops
// repo. HostParam is the Helm parameter set to Host, the ingress host badil
// computed for an exposed dependency: in a preview file both are set or both
// are empty.
type DependencyEntry struct {
	Name       string          `yaml:"name"`
	Chart      DependencyChart `yaml:"chart"`
	Values     map[string]any  `yaml:"values,omitempty"`
	ValueFiles []string        `yaml:"valueFiles,omitempty"`
	HostParam  string          `yaml:"hostParam,omitempty"`
	Host       string          `yaml:"host,omitempty"`
}

// DependencyChart is where a dependency's chart comes from: an OCI chart
// reference (RepoURL starts with OCIScheme, Name is empty) or a chart Name in
// the Helm repository at RepoURL, at Version.
type DependencyChart struct {
	RepoURL string `yaml:"repoURL"`
	Name    string `yaml:"name,omitempty"`
	Version string `yaml:"version"`
}

// OCIScheme starts the RepoURL of a dependency chart in an OCI registry.
const OCIScheme = "oci://"

// ChartName returns the name of the chart: Name, or the last path segment of
// an OCI reference.
func (c DependencyChart) ChartName() string {
	if c.Name != "" {
		return c.Name
	}
	return path.Base(c.RepoURL)
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
// preview-apps chart sets, which NamespaceMetadata.Labels cannot hold.
func ReservedNamespaceLabels() []string {
	return []string{
		"app.kubernetes.io/name",
		"app.kubernetes.io/instance",
		"app.kubernetes.io/part-of",
		"app.kubernetes.io/managed-by",
	}
}

// ReservedNamespaceAnnotations returns the preview Namespace annotations the
// preview-apps chart sets, which NamespaceMetadata.Annotations cannot hold.
func ReservedNamespaceAnnotations() []string {
	return []string{"argocd.argoproj.io/sync-wave"}
}
