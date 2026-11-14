// Package chartvalues holds the Go types of the preview-env chart's values,
// from which cmd/schemagen generates charts/preview-env/values.schema.json.
package chartvalues

import (
	"github.com/invopop/jsonschema"

	"github.com/badilhq/badil/internal/preview"
)

// NamespacePattern matches a Kubernetes namespace name (an RFC 1123 label).
const NamespacePattern = `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`

// Values is the merged input of the preview-env chart: a preview values file
// (apiVersion, services), the chart settings of the gitops repo's
// preview-env.yaml (serviceCharts, previewNamespace) and the ApplicationSet's
// argocdNamespace. No other root key is allowed.
type Values struct {
	APIVersion       string                  `yaml:"apiVersion"`
	Services         []preview.ServiceEntry  `yaml:"services"`
	ServiceCharts    map[string]ServiceChart `yaml:"serviceCharts"`
	PreviewNamespace PreviewNamespace        `yaml:"previewNamespace"`
	ArgocdNamespace  string                  `yaml:"argocdNamespace"`
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

// ServiceChart is a serviceCharts entry: how a service's chart is rendered.
// Every field is optional.
type ServiceChart struct {
	Path          string   `yaml:"path,omitempty"`
	ImageTagParam string   `yaml:"imageTagParam,omitempty"`
	HostParam     string   `yaml:"hostParam,omitempty"`
	ValueFiles    []string `yaml:"valueFiles,omitempty"`
}

// JSONSchemaExtend makes every field optional and every string non-empty.
func (ServiceChart) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = nil
	for _, name := range []string{"path", "imageTagParam", "hostParam"} {
		if p, ok := s.Properties.Get(name); ok {
			p.MinLength = nonEmpty()
		}
	}
	if p, ok := s.Properties.Get("valueFiles"); ok && p.Items != nil {
		p.Items.MinLength = nonEmpty()
	}
}

// PreviewNamespace is the extra metadata of the preview Namespace.
type PreviewNamespace struct {
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

// JSONSchemaExtend makes both fields optional and rejects, in each, the keys
// the chart sets on the Namespace. A reserved key maps to the false schema
// rather than to a propertyNames rule, whose failure Helm reports at a random
// location.
func (PreviewNamespace) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = nil
	if p, ok := s.Properties.Get("labels"); ok {
		reserve(p,
			"app.kubernetes.io/name",
			"app.kubernetes.io/instance",
			"app.kubernetes.io/part-of",
			"app.kubernetes.io/managed-by",
		)
	}
	if p, ok := s.Properties.Get("annotations"); ok {
		reserve(p, "argocd.argoproj.io/sync-wave")
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

// nonEmpty is a minLength of 1.
func nonEmpty() *uint64 {
	n := uint64(1)
	return &n
}
