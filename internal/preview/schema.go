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
