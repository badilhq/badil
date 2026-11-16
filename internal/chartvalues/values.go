// Package chartvalues holds the Go types of the preview-env chart's values,
// from which cmd/schemagen generates charts/preview-env/values.schema.json.
package chartvalues

import (
	"github.com/invopop/jsonschema"

	"github.com/badilhq/badil/internal/preview"
)

// NamespacePattern matches a Kubernetes namespace name (an RFC 1123 label).
const NamespacePattern = `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`

// Values is the input of the preview-env chart: a preview values file
// (apiVersion, namespace, services) and the ApplicationSet's
// argocdNamespace. No other root key is allowed.
type Values struct {
	APIVersion      string                     `yaml:"apiVersion"`
	Namespace       *preview.NamespaceMetadata `yaml:"namespace,omitempty"`
	Services        []preview.ServiceEntry     `yaml:"services"`
	ArgocdNamespace string                     `yaml:"argocdNamespace"`
}

// JSONSchemaExtend requires apiVersion, services and argocdNamespace (a null
// argocdNamespace in a values file removes the chart default before
// validation, so it must fail as missing), rejects other root keys,
// fixes apiVersion to preview.APIVersion and argocdNamespace to a namespace
// name.
func (Values) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"apiVersion", "services", "argocdNamespace"}
	s.AdditionalProperties = jsonschema.FalseSchema
	if p, ok := s.Properties.Get("apiVersion"); ok {
		p.Const = preview.APIVersion
	}
	if p, ok := s.Properties.Get("argocdNamespace"); ok {
		p.Pattern = NamespacePattern
	}
}
