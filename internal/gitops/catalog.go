package gitops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/preview"
)

// ServiceCatalog is the parsed badil.yaml of the gitops repo.
type ServiceCatalog struct {
	// Services lists the keys of services, in file order.
	Services []string
	// URLs is the validated `preview` block; its zero value means off.
	URLs preview.URLConfig
	// Label is the pull request label that requests a preview (preview.label,
	// default "preview"). GitHub label names compare case-insensitively.
	Label string
	// Repos maps each service to its git URL, as written in services.<name>.repo.
	Repos map[string]string
	// Branches maps each service whose entry sets branch to that branch; a
	// service without one uses main. It is nil when no entry sets one.
	Branches map[string]string
	// MaxPreviews is preview.maxPreviews, the most previews that may exist
	// before a deploy that would create a new one is refused. 0 means no cap.
	MaxPreviews int
}

// DefaultLabel is the preview label when preview.label is absent.
const DefaultLabel = "preview"

// ServicesForRepo returns, in catalog order, the services whose git URL is
// https://github.com/<owner>/<repo>, compared lowercased and with a trailing
// "/" then ".git" removed. Other URL forms (SSH, GitHub Enterprise) never
// match.
func (c *ServiceCatalog) ServicesForRepo(owner, repo string) []string {
	want := strings.ToLower("https://github.com/" + owner + "/" + repo)
	var out []string
	for _, name := range c.Services {
		if normalizeRepoURL(c.Repos[name]) == want {
			out = append(out, name)
		}
	}
	return out
}

// normalizeRepoURL lowercases url and removes a trailing "/" then ".git".
func normalizeRepoURL(url string) string {
	u := strings.ToLower(url)
	u = strings.TrimSuffix(u, "/")
	return strings.TrimSuffix(u, ".git")
}

// errMalformedPreview is wrapped by every preview block validation error.
var errMalformedPreview = errors.New("malformed preview block")

// domainPattern matches a lowercase bare domain: dot-separated labels of
// [a-z0-9-] that neither start nor end with a hyphen. No scheme, no path, no
// leading or trailing dot.
var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// strTag is the resolved YAML tag of a string scalar.
const strTag = "!!str"

// pairStride is the step between keys in a mapping node's Content, which
// alternates key and value nodes.
const pairStride = 2

// configFile is the path of badil's configuration in the gitops repo.
const configFile = "badil.yaml"

// badilFile is badil.yaml. Every key is known: decoding with KnownFields
// rejects any other. Services stays a node to keep catalog order; its entries
// are checked by parseServiceEntries, since yaml.Node.Decode ignores
// KnownFields.
type badilFile struct {
	Services yaml.Node     `yaml:"services"`
	Preview  *previewBlock `yaml:"preview"`
}

type previewBlock struct {
	Domain      domainName              `yaml:"domain"`
	TLS         *tlsFlag                `yaml:"tls"`
	Expose      map[string]*exposeEntry `yaml:"expose"`
	Label       *labelName              `yaml:"label"`
	MaxPreviews *maxPreviews            `yaml:"maxPreviews"`
}

type exposeEntry struct {
	Path *string `yaml:"path"`
}

// domainName is preview.domain. It only accepts a YAML string, not a scalar
// yaml.v3 would convert (`domain: 42`).
type domainName string

func (d *domainName) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag != strTag {
		return malformed("preview.domain", "must be a string")
	}
	if !domainPattern.MatchString(n.Value) {
		return malformed("preview.domain",
			"%q must be a lowercase bare domain (no scheme, no path, no leading or trailing dot)", n.Value)
	}
	*d = domainName(n.Value)
	return nil
}

// labelName is preview.label. It only accepts a non-empty YAML string; a YAML
// null leaves the field unset.
type labelName string

func (l *labelName) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag != strTag || n.Value == "" {
		return malformed("preview.label", "must be a non-empty string")
	}
	*l = labelName(n.Value)
	return nil
}

// maxPreviews is preview.maxPreviews. It only accepts a YAML integer of at
// least 1, not a string, a float or a non-positive integer; a YAML null leaves
// the field unset.
type maxPreviews int

func (m *maxPreviews) UnmarshalYAML(n *yaml.Node) error {
	var v int
	if n.Tag != "!!int" || n.Decode(&v) != nil || v < 1 {
		return malformed("preview.maxPreviews", "must be a positive integer")
	}
	*m = maxPreviews(v)
	return nil
}

// tlsFlag is preview.tls. It only accepts a YAML boolean, not the YAML 1.1
// forms yaml.v3 converts (`yes`, `on`).
type tlsFlag bool

func (t *tlsFlag) UnmarshalYAML(n *yaml.Node) error {
	var v bool
	if n.Tag != "!!bool" || n.Decode(&v) != nil {
		return malformed("preview.tls", "must be a boolean")
	}
	*t = tlsFlag(v)
	return nil
}

func malformed(key, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", errMalformedPreview, key, fmt.Sprintf(format, args...))
}

// ParseCatalog parses and validates the content of badil.yaml.
func ParseCatalog(raw []byte) (*ServiceCatalog, error) {
	var doc badilFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	services, repos, branches, err := parseServiceEntries(&doc.Services)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	urls, err := previewURLs(doc.Preview)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	label := DefaultLabel
	if doc.Preview != nil && doc.Preview.Label != nil {
		label = string(*doc.Preview.Label)
	}
	maxPrev := 0
	if doc.Preview != nil && doc.Preview.MaxPreviews != nil {
		maxPrev = int(*doc.Preview.MaxPreviews)
	}
	return &ServiceCatalog{
		Services: services, URLs: urls, Label: label, Repos: repos, Branches: branches,
		MaxPreviews: maxPrev,
	}, nil
}

// parseServiceEntries validates the services mapping, in file order, and
// returns the service names, the repo of each, and the branch of each entry
// that sets one (nil when none does).
func parseServiceEntries(n *yaml.Node) ([]string, map[string]string, map[string]string, error) {
	n = resolveAlias(n)
	switch {
	case n.Kind == 0 || n.Tag == "!!null":
		return nil, nil, nil, errors.New("missing 'services' mapping")
	case n.Kind != yaml.MappingNode:
		return nil, nil, nil, errors.New("'services' must be a mapping")
	}
	if len(n.Content) == 0 {
		return nil, nil, nil, errors.New("'services' must not be empty")
	}
	services := make([]string, 0, len(n.Content)/pairStride)
	repos := make(map[string]string, len(n.Content)/pairStride)
	var branches map[string]string
	for i := 0; i+1 < len(n.Content); i += pairStride {
		name := n.Content[i].Value
		if _, dup := repos[name]; dup {
			return nil, nil, nil, fmt.Errorf("services.%s is defined more than once", name)
		}
		repo, branch, err := serviceEntry(name, resolveAlias(n.Content[i+1]))
		if err != nil {
			return nil, nil, nil, err
		}
		services = append(services, name)
		repos[name] = repo
		if branch == "" {
			continue
		}
		if branches == nil {
			branches = make(map[string]string)
		}
		branches[name] = branch
	}
	return services, repos, branches, nil
}

// serviceEntry validates one services entry and returns its repo and its
// branch ("" when absent). Each key is checked on the node: repo is required,
// branch optional, any other key or a repeated key an error (decoding into a
// yaml.Node does not detect duplicate keys).
func serviceEntry(name string, n *yaml.Node) (string, string, error) {
	if n.Kind != yaml.MappingNode {
		return "", "", fmt.Errorf("services.%s must be a mapping", name)
	}
	var repo, branch string
	var hasRepo bool
	seen := make(map[string]bool, len(n.Content)/pairStride)
	for i := 0; i+1 < len(n.Content); i += pairStride {
		key, val := n.Content[i].Value, resolveAlias(n.Content[i+1])
		if seen[key] {
			return "", "", fmt.Errorf("services.%s.%s is defined more than once", name, key)
		}
		seen[key] = true
		switch key {
		case "repo":
			if !nonEmptyString(val) {
				return "", "", fmt.Errorf("services.%s.repo must be a non-empty string", name)
			}
			repo, hasRepo = val.Value, true
		case "branch":
			if !nonEmptyString(val) {
				return "", "", fmt.Errorf("services.%s.branch must be a non-empty string", name)
			}
			branch = val.Value
		default:
			return "", "", fmt.Errorf("services.%s: unknown key %q", name, key)
		}
	}
	if !hasRepo {
		return "", "", fmt.Errorf("services.%s: missing 'repo'", name)
	}
	return repo, branch, nil
}

// nonEmptyString reports whether n is a non-empty scalar usable as a string.
// Any scalar counts (yaml.v3 tags `yes` or `on` !!str, `2024-01-01`
// !!timestamp) except one tagged !!int, !!float, !!bool or !!null; a mapping
// or a sequence does not.
func nonEmptyString(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode || n.Value == "" {
		return false
	}
	switch n.Tag {
	case "!!int", "!!float", "!!bool", "!!null":
		return false
	}
	return true
}

// resolveAlias returns the node an alias points to, or n itself.
func resolveAlias(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

// previewURLs validates the decoded preview block. An absent block or an
// absent domain yields the zero URLConfig (feature off).
func previewURLs(p *previewBlock) (preview.URLConfig, error) {
	if p == nil {
		return preview.URLConfig{}, nil
	}
	if p.Domain == "" {
		if p.Expose != nil {
			return preview.URLConfig{}, malformed("preview.expose", "is set but preview.domain is absent")
		}
		return preview.URLConfig{}, nil
	}
	expose := make(map[string]string, len(p.Expose))
	for name, e := range p.Expose {
		path := "/"
		if e != nil && e.Path != nil {
			path = *e.Path
		}
		if !strings.HasPrefix(path, "/") {
			return preview.URLConfig{}, malformed("preview.expose."+name+".path",
				"must be a string starting with \"/\"")
		}
		expose[name] = path
	}
	return preview.URLConfig{Domain: string(p.Domain), TLS: p.TLS == nil || bool(*p.TLS), Expose: expose}, nil
}
