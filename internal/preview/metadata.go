package preview

// Metadata is the per-service annotation bag. yaml.v3 emits the known keys in
// field order and quotes any value that would not read back as a string, so
// the values file is byte-stable and safe for YAML 1.1 readers such as Helm.
// Every key is always written: the contract requires all seven, and has no
// other key.
type Metadata struct {
	PRAuthor       string `yaml:"pr-author"`
	PRURL          string `yaml:"pr-url"`
	PRNumber       string `yaml:"pr-number"`
	CreatedAt      string `yaml:"created-at"`
	UpdatedAt      string `yaml:"updated-at"`
	VCSRefName     string `yaml:"vcs.ref.name"`
	PipelineRunURL string `yaml:"cicd.pipeline.run.url"`
}

// BuildServiceMetadata constructs the canonical Metadata for a deploy event.
// On a fresh create, both created-at and updated-at equal inputs.Timestamp;
// UpdatePreviewValues overwrites updated-at while preserving the existing
// created-at.
func BuildServiceMetadata(inputs DeployInputs) *Metadata {
	return &Metadata{
		PRAuthor:       inputs.PRAuthor,
		PRURL:          inputs.PRURL,
		PRNumber:       inputs.PRNumber,
		CreatedAt:      inputs.Timestamp,
		UpdatedAt:      inputs.Timestamp,
		VCSRefName:     inputs.HeadRef,
		PipelineRunURL: inputs.WorkflowRunURL,
	}
}
