package preview

import "github.com/invopop/jsonschema"

// JSONSchemaExtend sets the constraints of a service entry that reflection
// does not produce: name, repoURL, branch, imageTag and chart are required,
// imageTag is non-empty, and commitSha and metadata are set together (a
// pinned entry carries both).
func (ServiceEntry) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"name", "repoURL", "branch", "imageTag", "chart"}
	s.DependentRequired = map[string][]string{
		"commitSha": {"metadata"},
		"metadata":  {"commitSha"},
	}
	if p, ok := s.Properties.Get("imageTag"); ok {
		p.MinLength = nonEmpty()
	}
}

// JSONSchemaExtend requires path, imageTagParam and hostParam, makes every
// string non-empty and valueFiles, when present, a non-empty list.
func (ChartSettings) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"path", "imageTagParam", "hostParam"}
	for _, name := range []string{"path", "imageTagParam", "hostParam"} {
		if p, ok := s.Properties.Get(name); ok {
			p.MinLength = nonEmpty()
		}
	}
	if p, ok := s.Properties.Get("valueFiles"); ok && p.Items != nil {
		p.MinItems = nonEmpty()
		p.Items.MinLength = nonEmpty()
	}
}

// JSONSchemaExtend makes both fields optional and rejects, in each, the keys
// the preview-env chart sets on the Namespace (ReservedNamespaceLabels,
// ReservedNamespaceAnnotations). A reserved key maps to the false schema
// rather than to a propertyNames rule, whose failure Helm reports at a random
// location.
func (NamespaceMetadata) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = nil
	if p, ok := s.Properties.Get("labels"); ok {
		reserve(p, ReservedNamespaceLabels()...)
	}
	if p, ok := s.Properties.Get("annotations"); ok {
		reserve(p, ReservedNamespaceAnnotations()...)
	}
}

// JSONSchemaExtend requires every metadata key.
func (Metadata) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{
		"pr-author", "pr-url", "pr-number", "created-at", "updated-at", "vcs.ref.name", "cicd.pipeline.run.url",
	}
}

// JSONSchemaExtend requires apiVersion and services, rejects other root keys
// and fixes apiVersion to APIVersion: the published contract of a preview
// values file.
func (PreviewValues) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"apiVersion", "services"}
	s.AdditionalProperties = jsonschema.FalseSchema
	if p, ok := s.Properties.Get("apiVersion"); ok {
		p.Const = APIVersion
	}
}

// reserve maps each of keys to the false schema in the properties of s, so
// an object with any of them is rejected.
func reserve(s *jsonschema.Schema, keys ...string) {
	s.Properties = jsonschema.NewProperties()
	for _, k := range keys {
		s.Properties.Set(k, jsonschema.FalseSchema)
	}
}

// nonEmpty is a minimum of 1, for minLength or minItems.
func nonEmpty() *uint64 {
	n := uint64(1)
	return &n
}
