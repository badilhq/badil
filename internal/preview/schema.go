package preview

import "github.com/invopop/jsonschema"

// JSONSchemaExtend sets the constraints of a service entry that reflection
// does not produce: name, repoURL and branch are required, and commitSha and
// metadata are set together (a pinned entry carries both).
func (ServiceEntry) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"name", "repoURL", "branch"}
	s.DependentRequired = map[string][]string{
		"commitSha": {"metadata"},
		"metadata":  {"commitSha"},
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
