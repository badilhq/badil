package archtest_test

import (
	"bytes"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// module is the import path prefix of the module's packages.
const module = "github.com/badilhq/badil/"

// layer is a layer of docs/ARCHITECTURE.md#code-layers.
type layer string

const (
	domain  layer = "domain"
	useCase layer = "use case"
	adapter layer = "adapter"
	tooling layer = "tooling"
	root    layer = "root"
)

// placement is a package's layer and, for an adapter, its component.
type placement struct {
	layer     layer
	component string
}

// layerMap returns every package of the module, by import path, with its
// layer and, for adapters, its component.
func layerMap() map[string]placement {
	return map[string]placement{
		module + "internal/preview":                 {layer: domain},
		module + "internal/catalog":                 {layer: domain},
		module + "internal/forge":                   {layer: useCase},
		module + "internal/lifecycle":               {layer: useCase},
		module + "internal/app":                     {layer: useCase},
		module + "internal/store":                   {layer: adapter, component: "store"},
		module + "internal/store/storetest":         {layer: adapter, component: "store"},
		module + "internal/forge/github":            {layer: adapter, component: "forge github"},
		module + "internal/forge/github/githubtest": {layer: adapter, component: "forge github"},
		module + "internal/github":                  {layer: adapter, component: "github"},
		module + "internal/github/manifest":         {layer: adapter, component: "github manifest"},
		module + "internal/namespace":               {layer: adapter, component: "namespace"},
		module + "internal/status":                  {layer: adapter, component: "status"},
		module + "internal/status/kube":             {layer: adapter, component: "status kube"},
		module + "internal/metrics":                 {layer: adapter, component: "metrics"},
		module + "internal/server":                  {layer: adapter, component: "server"},
		module + "internal/server/mocks":            {layer: adapter, component: "server"},
		module + "internal/api":                     {layer: adapter, component: "api"},
		module + "internal/apiclient":               {layer: adapter, component: "apiclient"},
		module + "internal/config":                  {layer: adapter, component: "config"},
		module + "internal/chartvalues":             {layer: tooling},
		module + "internal/fakegithub":              {layer: tooling},
		module + "internal/archtest":                {layer: tooling},
		module + "cmd/schemagen":                    {layer: tooling},
		module + "cmd/fakegithub":                   {layer: tooling},
		module + "cmd/badil":                        {layer: root},
	}
}

// allowedAdapterImports returns, by adapter component, the other adapter
// components it may import.
func allowedAdapterImports() map[string][]string {
	return map[string][]string{
		"forge github":    {"github"},
		"github manifest": {"github"},
		"server":          {"api"},
		"apiclient":       {"api"},
		"status kube":     {"status"},
		"config":          {"github"},
	}
}

// infrastructurePrefixes returns the import path prefixes of the libraries
// domain and use-case packages never reach.
func infrastructurePrefixes() []string {
	return []string{
		"github.com/go-git/",
		"github.com/google/go-github/",
		"github.com/bradleyfalzon/ghinstallation/",
		"k8s.io/",
		"github.com/prometheus/",
		"github.com/go-chi/",
		"github.com/spf13/cobra",
	}
}

// goList runs go with args and returns the lines it prints, failing t with
// the command's output when it fails.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go %s: %v\n%s%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return strings.Fields(stdout.String())
}

func TestLayerMapCoversEveryPackage(t *testing.T) {
	t.Parallel()
	listed := goList(t, "list", module+"...")
	mapped := layerMap()
	for _, pkg := range listed {
		if _, ok := mapped[pkg]; !ok {
			t.Errorf("%s is not in the layer map", pkg)
		}
	}
	for pkg := range mapped {
		if !slices.Contains(listed, pkg) {
			t.Errorf("%s is in the layer map but not a package of the module", pkg)
		}
	}
}

func TestDependencyRule(t *testing.T) {
	t.Parallel()
	mapped := layerMap()
	for _, pkg := range slices.Sorted(maps.Keys(mapped)) {
		if l := mapped[pkg].layer; l != domain && l != useCase && l != adapter {
			continue
		}
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()
			for _, dep := range goList(t, "list", "-deps", pkg) {
				if dep != pkg && !allowed(mapped, pkg, dep) {
					t.Errorf("%s depends on %s: a %s package may not, even through another package "+
						"(docs/ARCHITECTURE.md#code-layers)", pkg, dep, mapped[pkg].layer)
				}
			}
		})
	}
}

// allowed reports whether package pkg may depend on dep, directly or not.
func allowed(mapped map[string]placement, pkg, dep string) bool {
	from := mapped[pkg]
	if !strings.HasPrefix(dep, module) {
		return from.layer == adapter || !slices.ContainsFunc(infrastructurePrefixes(), func(prefix string) bool {
			return strings.HasPrefix(dep, prefix)
		})
	}
	to, ok := mapped[dep]
	if !ok {
		return false
	}
	switch from.layer {
	case domain:
		return to.layer == domain
	case useCase:
		return to.layer == domain || to.layer == useCase
	case adapter:
		return to.layer == domain || to.layer == useCase ||
			to.component == from.component || slices.Contains(allowedAdapterImports()[from.component], to.component)
	case tooling, root:
	}
	return false
}
