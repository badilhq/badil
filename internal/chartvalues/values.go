// Package chartvalues holds the Go types of the preview-env chart's values,
// from which cmd/schemagen generates charts/preview-env/values.schema.json.
package chartvalues

import (
	"github.com/invopop/jsonschema"

	"github.com/badilhq/badil/internal/preview"
)

// NamespacePattern matches a Kubernetes namespace name (an RFC 1123 label).
const NamespacePattern = `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`

// Values is the input of the preview-env chart: a preview values file and
// the ApplicationSet's argocdNamespace. No other root key is allowed.
type Values struct {
	preview.PreviewValues `yaml:",inline"`

	ArgocdNamespace string `yaml:"argocdNamespace"`
}

// JSONSchemaExtend applies the preview values file's root constraints
// (preview.PreviewValues.JSONSchemaExtend), requires argocdNamespace (a null
// argocdNamespace in a values file removes the chart default before
// validation, so it must fail as missing) and makes it a namespace name.
func (Values) JSONSchemaExtend(s *jsonschema.Schema) {
	preview.PreviewValues{}.JSONSchemaExtend(s)
	s.Required = append(s.Required, "argocdNamespace")
	if p, ok := s.Properties.Get("argocdNamespace"); ok {
		p.Pattern = NamespacePattern
	}
}
