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
		p.MinLength = new(uint64(1))
	}
}

// JSONSchemaExtend requires path, imageTagParam and hostParam, makes every
// string non-empty and valueFiles, when present, a non-empty list.
func (ChartSettings) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"path", "imageTagParam", "hostParam"}
	for _, name := range []string{"path", "imageTagParam", "hostParam"} {
		if p, ok := s.Properties.Get(name); ok {
			p.MinLength = new(uint64(1))
		}
	}
	nonEmptyList(s, "valueFiles")
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

// JSONSchemaExtend requires apiVersion, gitopsRepoURL, services and
// dependencies, rejects other root keys, fixes apiVersion to APIVersion and
// makes gitopsRepoURL non-empty: the published contract of a preview values
// file.
func (PreviewValues) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"apiVersion", "gitopsRepoURL", "services", "dependencies"}
	s.AdditionalProperties = jsonschema.FalseSchema
	if p, ok := s.Properties.Get("apiVersion"); ok {
		p.Const = APIVersion
	}
	if p, ok := s.Properties.Get("gitopsRepoURL"); ok {
		p.MinLength = new(uint64(1))
	}
}

// JSONSchemaExtend requires name and chart, makes every string non-empty and
// valueFiles, when present, a non-empty list, and requires host and
// hostParam together. values stays a free-form object.
func (DependencyEntry) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"name", "chart"}
	const host, hostParam = "host", "hostParam"
	s.DependentRequired = map[string][]string{
		host:      {hostParam},
		hostParam: {host},
	}
	for _, name := range []string{"name", hostParam, host} {
		if p, ok := s.Properties.Get(name); ok {
			p.MinLength = new(uint64(1))
		}
	}
	nonEmptyList(s, "valueFiles")
}

// JSONSchemaExtend requires repoURL and version, non-empty, with repoURL an
// oci:// or https:// URL. name is rejected with an OCIScheme repoURL and
// required otherwise.
func (DependencyChart) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"repoURL", "version"}
	for _, name := range []string{"repoURL", "name", "version"} {
		if p, ok := s.Properties.Get(name); ok {
			p.MinLength = new(uint64(1))
		}
	}
	if p, ok := s.Properties.Get("repoURL"); ok {
		p.Pattern = "^(oci|https)://"
	}
	oci := jsonschema.NewProperties()
	oci.Set("repoURL", &jsonschema.Schema{Pattern: "^" + OCIScheme})
	noName := jsonschema.NewProperties()
	noName.Set("name", jsonschema.FalseSchema)
	s.If = &jsonschema.Schema{Properties: oci}
	s.Then = &jsonschema.Schema{Properties: noName}
	s.Else = &jsonschema.Schema{Required: []string{"name"}}
}

// reserve maps each of keys to the false schema in the properties of s, so
// an object with any of them is rejected.
func reserve(s *jsonschema.Schema, keys ...string) {
	s.Properties = jsonschema.NewProperties()
	for _, k := range keys {
		s.Properties.Set(k, jsonschema.FalseSchema)
	}
}

// nonEmptyList makes the list property name of s, when present, a non-empty
// list of non-empty strings.
func nonEmptyList(s *jsonschema.Schema, name string) {
	if p, ok := s.Properties.Get(name); ok && p.Items != nil {
		p.MinItems = new(uint64(1))
		p.Items.MinLength = new(uint64(1))
	}
}
