package gitops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/preview"
)

// ServiceCatalog is the parsed badil.yaml of the gitops repo. Its Catalog
// holds the services and dependencies, in file order, and their settings;
// GitopsRepoURL is left empty, set by the client when it projects a preview
// file. A map or slice of it is nil when no entry sets it.
type ServiceCatalog struct {
	preview.Catalog

	// URLs is the validated `preview` block; its zero value means off.
	URLs preview.URLConfig
	// Label is the pull request label that requests a preview (preview.label,
	// default "preview"). GitHub label names compare case-insensitively.
	Label string
	// MaxPreviews is preview.maxPreviews, the most previews that may exist
	// before a deploy that would create a new one is refused. 0 means no cap.
	MaxPreviews int
	// ArgoCDURL is preview.argocdURL, the base URL of the Argo CD UI without
	// a trailing slash. When set, the preview summary links the preview's
	// Applications in Argo CD. It is empty when absent.
	ArgoCDURL string
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
// rejects any other. Services and Dependencies stay nodes to keep file order;
// their entries are checked by parseServiceEntries and parseDependencies,
// since yaml.Node.Decode ignores KnownFields. ImageTag and Namespace stay
// nodes so that a null is rejected rather than ignored.
type badilFile struct {
	Services     yaml.Node     `yaml:"services"`
	Dependencies yaml.Node     `yaml:"dependencies"`
	Preview      *previewBlock `yaml:"preview"`
	ImageTag     yaml.Node     `yaml:"imageTag"`
	Namespace    yaml.Node     `yaml:"namespace"`
}

type previewBlock struct {
	Domain      domainName              `yaml:"domain"`
	TLS         *tlsFlag                `yaml:"tls"`
	Expose      map[string]*exposeEntry `yaml:"expose"`
	Label       *labelName              `yaml:"label"`
	MaxPreviews *maxPreviews            `yaml:"maxPreviews"`
	ArgoCDURL   *argocdURL              `yaml:"argocdURL"`
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

// argocdURL is preview.argocdURL. It only accepts a YAML string holding an
// absolute http or https URL with a host, no credentials, no query or fragment
// and only URL characters other than parentheses; a trailing slash is removed.
// A YAML null leaves the field unset.
type argocdURL string

func (a *argocdURL) UnmarshalYAML(n *yaml.Node) error {
	const key = "preview.argocdURL"
	if n.Tag != strTag {
		return malformed(key, "must be a string")
	}
	u, err := url.Parse(n.Value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return malformed(key, "%q must be an absolute http or https URL with a host", n.Value)
	}
	if strings.ContainsAny(n.Value, "?#") {
		return malformed(key, "%q must not have a query or a fragment", n.Value)
	}
	if u.User != nil {
		return malformed(key, "%q must not contain credentials", n.Value)
	}
	if strings.ContainsFunc(n.Value, func(r rune) bool { return !isLinkURLChar(r) }) {
		return malformed(key, "%q must only contain URL characters, without spaces or parentheses", n.Value)
	}
	*a = argocdURL(strings.TrimSuffix(n.Value, "/"))
	return nil
}

// isLinkURLChar reports whether r may appear as written in a URL (RFC 3986
// unreserved and reserved characters and %) without breaking a Markdown link
// target, which a space or a parenthesis ends.
func isLinkURLChar(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return true
	default:
		return strings.ContainsRune("-._~:/[]@!$&'*+,;=%", r)
	}
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
	c, err := parseDocument(&doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	return c, nil
}

// parseDocument validates the decoded badil.yaml and returns its catalog.
func parseDocument(doc *badilFile) (*ServiceCatalog, error) {
	c := &ServiceCatalog{Label: DefaultLabel}
	if err := parseServiceEntries(&doc.Services, c); err != nil {
		return nil, err
	}
	if doc.Dependencies.Kind != 0 {
		if err := parseDependencies(&doc.Dependencies, c); err != nil {
			return nil, err
		}
	}
	if err := checkNeeds(c); err != nil {
		return nil, err
	}
	if doc.ImageTag.Kind != 0 {
		format, err := parseImageTag("imageTag", &doc.ImageTag)
		if err != nil {
			return nil, err
		}
		c.ImageTag = format
	}
	if doc.Namespace.Kind != 0 {
		ns, err := parseNamespace(&doc.Namespace)
		if err != nil {
			return nil, err
		}
		if len(ns.Labels) > 0 || len(ns.Annotations) > 0 {
			c.Namespace = &ns
		}
	}
	urls, err := previewURLs(doc.Preview)
	if err != nil {
		return nil, err
	}
	c.URLs = urls
	if err = checkExposedDependencies(c); err != nil {
		return nil, err
	}
	if doc.Preview != nil && doc.Preview.Label != nil {
		c.Label = string(*doc.Preview.Label)
	}
	if doc.Preview != nil && doc.Preview.MaxPreviews != nil {
		c.MaxPreviews = int(*doc.Preview.MaxPreviews)
	}
	if doc.Preview != nil && doc.Preview.ArgoCDURL != nil {
		c.ArgoCDURL = string(*doc.Preview.ArgoCDURL)
	}
	return c, nil
}

// parseServiceEntries validates the services mapping, in file order, and sets
// the services of c: their names, the repo of each, and the branch, image tag
// format and chart settings of each entry that sets them.
func parseServiceEntries(n *yaml.Node, c *ServiceCatalog) error {
	n = resolveAlias(n)
	switch {
	case n.Kind == 0 || n.Tag == "!!null":
		return errors.New("missing 'services' mapping")
	case n.Kind != yaml.MappingNode:
		return errors.New("'services' must be a mapping")
	}
	if len(n.Content) == 0 {
		return errors.New("'services' must not be empty")
	}
	c.Services = make([]string, 0, len(n.Content)/pairStride)
	c.Repos = make(map[string]string, len(n.Content)/pairStride)
	return eachKey("services", n, func(name string, val *yaml.Node) error {
		spec, err := serviceEntry(name, val)
		if err != nil {
			return err
		}
		c.Services = append(c.Services, name)
		c.Repos[name] = spec.repo
		if spec.branch != "" {
			c.Branches = setKey(c.Branches, name, spec.branch)
		}
		if spec.imageTag != "" {
			c.ImageTags = setKey(c.ImageTags, name, spec.imageTag)
		}
		if spec.chart != nil {
			c.Charts = setKey(c.Charts, name, *spec.chart)
		}
		if spec.needs != nil {
			c.Needs = setKey(c.Needs, name, spec.needs)
		}
		return nil
	})
}

// setKey sets m[key] to v, allocating m when nil, and returns m.
func setKey[V any](m map[string]V, key string, v V) map[string]V {
	if m == nil {
		m = make(map[string]V)
	}
	m[key] = v
	return m
}

// serviceSpec is one validated services entry. Every field but repo is empty
// (chart and needs nil) when the entry does not set it.
type serviceSpec struct {
	repo, branch, imageTag string
	chart                  *preview.ChartSettings
	needs                  []string
}

// serviceEntry validates one services entry: repo is required; branch,
// imageTag, chart and needs are optional; any other key or a repeated key is an
// error (decoding into a yaml.Node does not detect duplicate keys).
func serviceEntry(name string, n *yaml.Node) (serviceSpec, error) {
	prefix := "services." + name
	if n.Kind != yaml.MappingNode {
		return serviceSpec{}, fmt.Errorf("%s must be a mapping", prefix)
	}
	var spec serviceSpec
	var hasRepo bool
	err := eachKey(prefix, n, func(key string, val *yaml.Node) error {
		var err error
		path := prefix + "." + key
		switch key {
		case "repo":
			spec.repo, err = requireString(path, val)
			hasRepo = true
		case "branch":
			spec.branch, err = requireString(path, val)
		case "imageTag":
			spec.imageTag, err = parseImageTag(path, val)
		case "chart":
			var chart preview.ChartSettings
			chart, err = parseChart(path, val)
			spec.chart = &chart
		case "needs":
			spec.needs, err = parseNeeds(path, name, val)
		default:
			err = fmt.Errorf("%s: unknown key %q", prefix, key)
		}
		return err
	})
	if err != nil {
		return serviceSpec{}, err
	}
	if !hasRepo {
		return serviceSpec{}, fmt.Errorf("%s: missing 'repo'", prefix)
	}
	return spec, nil
}

// parseChart validates services.<name>.chart, at path: a mapping whose keys
// are all optional. path, imageTagParam and hostParam are non-empty strings;
// valueFiles is a non-empty list of non-empty strings. An absent key is left
// empty.
func parseChart(path string, n *yaml.Node) (preview.ChartSettings, error) {
	if n.Kind != yaml.MappingNode {
		return preview.ChartSettings{}, fmt.Errorf("%s must be a mapping", path)
	}
	var chart preview.ChartSettings
	err := eachKey(path, n, func(key string, val *yaml.Node) error {
		var err error
		keyPath := path + "." + key
		switch key {
		case "path":
			chart.Path, err = requireString(keyPath, val)
		case "imageTagParam":
			chart.ImageTagParam, err = requireString(keyPath, val)
		case "hostParam":
			chart.HostParam, err = requireString(keyPath, val)
		case "valueFiles":
			chart.ValueFiles, err = parseValueFiles(keyPath, val)
		default:
			err = fmt.Errorf("%s: unknown key %q", path, key)
		}
		return err
	})
	return chart, err
}

// parseValueFiles validates a valueFiles list, at path: a non-empty sequence
// of non-empty strings.
func parseValueFiles(path string, n *yaml.Node) ([]string, error) {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty list of non-empty strings", path)
	}
	files := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		item = resolveAlias(item)
		if !nonEmptyString(item) {
			return nil, fmt.Errorf("%s must be a non-empty list of non-empty strings", path)
		}
		files = append(files, item.Value)
	}
	return files, nil
}

// parseNeeds validates the needs of the entry name, at path: a non-empty
// sequence of non-empty strings, none repeated and none equal to name. Whether
// each names a service or a dependency is checked by checkNeeds.
func parseNeeds(path, name string, n *yaml.Node) ([]string, error) {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty list of service or dependency names", path)
	}
	needs := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		item = resolveAlias(item)
		switch {
		case !nonEmptyString(item):
			return nil, fmt.Errorf("%s must be a non-empty list of service or dependency names", path)
		case item.Value == name:
			return nil, fmt.Errorf("%s: %q cannot need itself", path, name)
		case slices.Contains(needs, item.Value):
			return nil, fmt.Errorf("%s: %q is listed more than once", path, item.Value)
		}
		needs = append(needs, item.Value)
	}
	return needs, nil
}

// checkNeeds rejects a needs entry, services first then dependencies in file
// order, that names neither a service nor a dependency of c.
func checkNeeds(c *ServiceCatalog) error {
	known := make(map[string]bool, len(c.Services)+len(c.Dependencies))
	for _, name := range c.Services {
		known[name] = true
	}
	for _, dep := range c.Dependencies {
		known[dep.Name] = true
	}
	check := func(kind, name string) error {
		for _, need := range c.Needs[name] {
			if !known[need] {
				return fmt.Errorf("%s.%s.needs: %q is neither a service nor a dependency", kind, name, need)
			}
		}
		return nil
	}
	for _, name := range c.Services {
		if err := check("services", name); err != nil {
			return err
		}
	}
	for _, dep := range c.Dependencies {
		if err := check("dependencies", dep.Name); err != nil {
			return err
		}
	}
	return nil
}

// parseImageTag validates an image tag format, at path (imageTag or
// services.<name>.imageTag): preview.ValidImageTagFormat.
func parseImageTag(path string, n *yaml.Node) (string, error) {
	n = resolveAlias(n)
	if !nonEmptyString(n) || !preview.ValidImageTagFormat(n.Value) {
		return "", fmt.Errorf(
			"%s must be a quoted string made of an optional prefix, one of {sha}, {sha7} or {sha8}, "+
				"and an optional suffix ([a-zA-Z0-9_.-], the prefix not starting with . or -), "+
				"at most %d characters", path, preview.MaxImageTagFormatLen)
	}
	return n.Value, nil
}

// parseNamespace validates the root namespace block: a mapping with the
// optional keys labels and annotations.
func parseNamespace(n *yaml.Node) (preview.NamespaceMetadata, error) {
	n = resolveAlias(n)
	if n.Kind != yaml.MappingNode {
		return preview.NamespaceMetadata{}, errors.New("namespace must be a mapping")
	}
	var ns preview.NamespaceMetadata
	err := eachKey("namespace", n, func(key string, val *yaml.Node) error {
		var err error
		switch key {
		case "labels":
			ns.Labels, err = parseStringMap("namespace.labels", val, preview.ReservedNamespaceLabels())
		case "annotations":
			ns.Annotations, err = parseStringMap("namespace.annotations", val, preview.ReservedNamespaceAnnotations())
		default:
			err = fmt.Errorf("namespace: unknown key %q", key)
		}
		return err
	})
	return ns, err
}

// parseStringMap validates a mapping of non-empty string keys to string
// values, at path, none of whose keys is in reserved.
func parseStringMap(path string, n *yaml.Node, reserved []string) (map[string]string, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", path)
	}
	out := make(map[string]string, len(n.Content)/pairStride)
	err := eachKey(path, n, func(key string, val *yaml.Node) error {
		keyPath := path + "." + key
		switch {
		case key == "":
			return fmt.Errorf("%s: keys must be non-empty strings", path)
		case slices.Contains(reserved, key):
			return fmt.Errorf("%s is set by the preview-apps chart and cannot be overridden", keyPath)
		case !stringScalar(val):
			return fmt.Errorf("%s must be a string", keyPath)
		}
		out[key] = val.Value
		return nil
	})
	return out, err
}

// requireString returns the value of n, a non-empty string at path.
func requireString(path string, n *yaml.Node) (string, error) {
	if !nonEmptyString(n) {
		return "", fmt.Errorf("%s must be a non-empty string", path)
	}
	return n.Value, nil
}

// eachKey calls fn with each key of the mapping n, at path, and its value
// (aliases resolved), in file order, stopping at the first error. A key that
// is not a string or is repeated is an error.
func eachKey(path string, n *yaml.Node, fn func(key string, val *yaml.Node) error) error {
	seen := make(map[string]bool, len(n.Content)/pairStride)
	for i := 0; i+1 < len(n.Content); i += pairStride {
		k := resolveAlias(n.Content[i])
		if !stringScalar(k) {
			return fmt.Errorf("%s: keys must be strings, got %q", path, k.Value)
		}
		if seen[k.Value] {
			return fmt.Errorf("%s.%s is defined more than once", path, k.Value)
		}
		seen[k.Value] = true
		if err := fn(k.Value, resolveAlias(n.Content[i+1])); err != nil {
			return err
		}
	}
	return nil
}

// nonEmptyString reports whether n is a non-empty stringScalar.
func nonEmptyString(n *yaml.Node) bool {
	return stringScalar(n) && n.Value != ""
}

// stringScalar reports whether n is a scalar usable as a string. Any scalar
// counts (yaml.v3 tags `yes` or `on` !!str, `2024-01-01` !!timestamp) except
// one tagged !!int, !!float, !!bool or !!null; a mapping or a sequence does
// not.
func stringScalar(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode {
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

// parseDependencies validates the dependencies mapping, in file order, and
// sets c.Dependencies. A dependency name must be unique and must not be a
// service of c.
func parseDependencies(n *yaml.Node, c *ServiceCatalog) error {
	n = resolveAlias(n)
	if n.Kind != yaml.MappingNode {
		return errors.New("dependencies must be a mapping")
	}
	return eachKey("dependencies", n, func(name string, val *yaml.Node) error {
		if _, isService := c.Repos[name]; isService {
			return fmt.Errorf("dependencies.%s: %q is already a service", name, name)
		}
		dep, needs, err := dependencyEntry(name, val)
		if err != nil {
			return err
		}
		c.Dependencies = append(c.Dependencies, dep)
		if needs != nil {
			c.Needs = setKey(c.Needs, name, needs)
		}
		return nil
	})
}

// dependencyEntry validates one dependencies entry. chart and version are
// required. A chart starting with preview.OCIScheme is an OCI chart reference,
// and repo must then be absent; any other chart is a chart name, and repo, an
// https:// Helm repository URL, is then required. values (a mapping),
// valueFiles, hostParam and needs are optional. Any other key is an error. It
// returns the entry and its needs, nil when absent.
func dependencyEntry(name string, n *yaml.Node) (preview.DependencyEntry, []string, error) {
	prefix := "dependencies." + name
	if n.Kind != yaml.MappingNode {
		return preview.DependencyEntry{}, nil, fmt.Errorf("%s must be a mapping", prefix)
	}
	dep := preview.DependencyEntry{Name: name}
	var chart, repo string
	var hasRepo bool
	var needs []string
	err := eachKey(prefix, n, func(key string, val *yaml.Node) error {
		var err error
		path := prefix + "." + key
		switch key {
		case "chart":
			chart, err = requireString(path, val)
		case "repo":
			repo, err = requireString(path, val)
			hasRepo = true
		case "version":
			dep.Chart.Version, err = requireString(path, val)
		case "values":
			if dep.Values, err = parseValues(path, val); len(dep.Values) == 0 {
				dep.Values = nil
			}
		case "valueFiles":
			dep.ValueFiles, err = parseValueFiles(path, val)
		case "hostParam":
			dep.HostParam, err = requireString(path, val)
		case "needs":
			needs, err = parseNeeds(path, name, val)
		default:
			err = fmt.Errorf("%s: unknown key %q", prefix, key)
		}
		return err
	})
	if err != nil {
		return preview.DependencyEntry{}, nil, err
	}
	switch {
	case chart == "":
		return preview.DependencyEntry{}, nil, fmt.Errorf("%s: missing 'chart'", prefix)
	case dep.Chart.Version == "":
		return preview.DependencyEntry{}, nil, fmt.Errorf("%s: missing 'version'", prefix)
	case strings.HasPrefix(chart, preview.OCIScheme):
		if hasRepo {
			return preview.DependencyEntry{}, nil, fmt.Errorf(
				"%s: 'repo' must be absent when 'chart' is an %s reference", prefix, preview.OCIScheme)
		}
		if !hasHost(chart, "oci") {
			return preview.DependencyEntry{}, nil, fmt.Errorf(
				"%s.chart must be an %s reference with a host", prefix, preview.OCIScheme)
		}
		dep.Chart.RepoURL = chart
	case !hasRepo:
		return preview.DependencyEntry{}, nil, fmt.Errorf(
			"%s: missing 'repo' ('chart' is not an %s reference)", prefix, preview.OCIScheme)
	case !hasHost(repo, "https"):
		return preview.DependencyEntry{}, nil, fmt.Errorf("%s.repo must be an https:// Helm repository URL", prefix)
	default:
		dep.Chart.RepoURL, dep.Chart.Name = repo, chart
	}
	return dep, needs, nil
}

// hasHost reports whether raw starts with scheme:// and parses as a URL with
// a host.
func hasHost(raw, scheme string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasPrefix(raw, scheme+"://") && u.Host != ""
}

// parseValues validates a dependency's values, at path: a mapping, returned
// as yaml.v3 decodes it into a map, with every timestamp kept as the string
// written.
func parseValues(path string, n *yaml.Node) (map[string]any, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", path)
	}
	timestampsAsStrings(n)
	var values map[string]any
	if err := n.Decode(&values); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return values, nil
}

// timestampsAsStrings retags every timestamp scalar under n as a string, so
// that decoding keeps the text written rather than a time.Time, which yaml.v3
// would encode in another form.
func timestampsAsStrings(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!timestamp" {
		n.Tag = strTag
	}
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		timestampsAsStrings(n.Alias)
	}
	for _, child := range n.Content {
		timestampsAsStrings(child)
	}
}

// checkExposedDependencies rejects a dependency named in preview.expose
// without a hostParam: no Helm parameter is known to set its host.
func checkExposedDependencies(c *ServiceCatalog) error {
	for _, dep := range c.Dependencies {
		if _, exposed := c.URLs.Expose[dep.Name]; exposed && dep.HostParam == "" {
			return fmt.Errorf("dependencies.%s: missing 'hostParam' (required when preview.expose.%s is set)",
				dep.Name, dep.Name)
		}
	}
	return nil
}
