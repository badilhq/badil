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
// (apiVersion, services) and the chart settings of the gitops repo's
// services.yaml. Other root keys are allowed.
type Values struct {
	APIVersion       string                  `yaml:"apiVersion"`
	Services         []preview.ServiceEntry  `yaml:"services"`
	ServiceCharts    map[string]ServiceChart `yaml:"serviceCharts"`
	PreviewNamespace PreviewNamespace        `yaml:"previewNamespace"`
	ArgocdNamespace  string                  `yaml:"argocdNamespace"`
}

// JSONSchemaExtend requires apiVersion and services, allows other root keys,
// fixes apiVersion to preview.APIVersion and argocdNamespace to a namespace
// name.
func (Values) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = []string{"apiVersion", "services"}
	s.AdditionalProperties = jsonschema.TrueSchema
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
	Branch        string   `yaml:"branch,omitempty"`
	Path          string   `yaml:"path,omitempty"`
	ImageTagParam string   `yaml:"imageTagParam,omitempty"`
	HostParam     string   `yaml:"hostParam,omitempty"`
	ValueFiles    []string `yaml:"valueFiles,omitempty"`
}

// JSONSchemaExtend makes every field optional and every string non-empty.
func (ServiceChart) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = nil
	for _, name := range []string{"branch", "path", "imageTagParam", "hostParam"} {
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
// the chart sets on the Namespace.
func (PreviewNamespace) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Required = nil
	if p, ok := s.Properties.Get("labels"); ok {
		p.PropertyNames = notOneOf(
			"app.kubernetes.io/name",
			"app.kubernetes.io/instance",
			"app.kubernetes.io/part-of",
			"app.kubernetes.io/managed-by",
		)
	}
	if p, ok := s.Properties.Get("annotations"); ok {
		p.PropertyNames = notOneOf("argocd.argoproj.io/sync-wave")
	}
}

// notOneOf is a schema that rejects each of keys.
func notOneOf(keys ...string) *jsonschema.Schema {
	enum := make([]any, len(keys))
	for i, k := range keys {
		enum[i] = k
	}
	return &jsonschema.Schema{Not: &jsonschema.Schema{Enum: enum}}
}

// nonEmpty is a minLength of 1.
func nonEmpty() *uint64 {
	n := uint64(1)
	return &n
}
