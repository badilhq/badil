package gitops_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/badilhq/badil/internal/gitops"
	"github.com/badilhq/badil/internal/preview"
)

// catalogServices is a services block of four lines, so that a preview
// block appended to it starts at line 5.
const catalogServices = `services:
  backend-1: {repo: https://github.com/acme/backend-1.git}
  backend-2: {repo: https://github.com/acme/backend-2.git}
  front: {repo: https://github.com/acme/front.git}
`

func TestParseCatalog_Preview(t *testing.T) {
	tests := []struct {
		name    string
		preview string
		want    preview.URLConfig
	}{
		{
			name: "absent block",
			want: preview.URLConfig{},
		},
		{
			name:    "null block",
			preview: "preview:\n",
			want:    preview.URLConfig{},
		},
		{
			name:    "absent domain",
			preview: "preview:\n  tls: false\n",
			want:    preview.URLConfig{},
		},
		{
			name: "full block",
			preview: `preview:
  domain: 127.0.0.1.nip.io
  tls: false
  expose:
    front: {}
    backend-1:
      path: /docs
    backend-2:
`,
			want: preview.URLConfig{
				Domain: "127.0.0.1.nip.io",
				TLS:    false,
				Expose: map[string]string{"front": "/", "backend-1": "/docs", "backend-2": "/"},
			},
		},
		{
			name:    "tls defaults to true",
			preview: "preview:\n  domain: example.com\n  expose:\n    front: {}\n",
			want: preview.URLConfig{
				Domain: "example.com",
				TLS:    true,
				Expose: map[string]string{"front": "/"},
			},
		},
		{
			name:    "expose entry for a service outside the catalog",
			preview: "preview:\n  domain: example.com\n  expose:\n    unknown: {}\n",
			want: preview.URLConfig{
				Domain: "example.com",
				TLS:    true,
				Expose: map[string]string{"unknown": "/"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitops.ParseCatalog([]byte(catalogServices + tt.preview))
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			if !reflect.DeepEqual(got.URLs, tt.want) {
				t.Errorf("URLs = %+v, want %+v", got.URLs, tt.want)
			}
			if want := []string{"backend-1", "backend-2", "front"}; !reflect.DeepEqual(got.Services, want) {
				t.Errorf("Services = %v, want %v", got.Services, want)
			}
		})
	}
}

func TestParseCatalog_PreviewAliases(t *testing.T) {
	want := preview.URLConfig{
		Domain: "example.com",
		TLS:    false,
		Expose: map[string]string{"front": "/docs", "backend-1": "/docs"},
	}
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "aliased expose entry",
			raw: `preview:
  domain: example.com
  tls: false
  expose:
    backend-1: &e
      path: /docs
    front: *e
`,
		},
		{
			name: "merge key",
			raw: `preview:
  domain: example.com
  tls: false
  expose:
    backend-1: &e
      path: /docs
    front:
      <<: *e
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitops.ParseCatalog([]byte(catalogServices + tt.raw))
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			if !reflect.DeepEqual(got.URLs, want) {
				t.Errorf("URLs = %+v, want %+v", got.URLs, want)
			}
		})
	}
}

func TestParseCatalog_MalformedPreview(t *testing.T) {
	tests := []struct {
		name    string
		preview string
		key     string
	}{
		{"preview not a map", "preview: [a]\n", "line 5: cannot unmarshal !!seq into gitops.previewBlock"},
		{"preview scalar", "preview: on\n", "line 5: cannot unmarshal !!str `on` into gitops.previewBlock"},
		{"domain not a string", "preview:\n  domain: [a]\n", "preview.domain"},
		{"domain number", "preview:\n  domain: 42\n", "preview.domain"},
		{"domain with scheme", "preview:\n  domain: https://example.com\n", "preview.domain"},
		{"domain with path", "preview:\n  domain: example.com/x\n", "preview.domain"},
		{"domain leading dot", "preview:\n  domain: .example.com\n", "preview.domain"},
		{"domain trailing dot", "preview:\n  domain: example.com.\n", "preview.domain"},
		{"domain uppercase", "preview:\n  domain: Example.com\n", "preview.domain"},
		{"domain empty", "preview:\n  domain: \"\"\n", "preview.domain"},
		{"tls not a boolean", "preview:\n  domain: example.com\n  tls: \"false\"\n", "preview.tls"},
		{
			"expose not a map",
			"preview:\n  domain: example.com\n  expose: [front]\n",
			"line 7: cannot unmarshal !!seq into map[string]*gitops.exposeEntry",
		},
		{
			"expose entry not null or map",
			"preview:\n  domain: example.com\n  expose:\n    front: yes\n",
			"line 8: cannot unmarshal !!str `yes` into gitops.exposeEntry",
		},
		{
			"path not a string",
			"preview:\n  domain: example.com\n  expose:\n    front:\n      path: 1\n",
			"preview.expose.front.path",
		},
		{
			"path without leading slash",
			"preview:\n  domain: example.com\n  expose:\n    front:\n      path: docs\n",
			"preview.expose.front.path",
		},
		{"expose without domain", "preview:\n  expose:\n    front: {}\n", "preview.expose"},
		{"unknown preview key", "preview:\n  domian: example.com\n", "line 6: field domian not found"},
		{
			"unknown preview key next to valid ones",
			"preview:\n  domain: example.com\n  expsoe:\n    front: {}\n",
			"line 7: field expsoe not found",
		},
		{
			"unknown expose entry key",
			"preview:\n  domain: example.com\n  expose:\n    front:\n      paht: /docs\n",
			"line 9: field paht not found",
		},
		{
			"duplicate preview key",
			"preview:\n  domain: example.com\n  domain: example.org\n",
			`line 7: mapping key "domain" already defined at line 6`,
		},
		{
			"duplicate expose entry",
			"preview:\n  domain: example.com\n  expose:\n    front: {}\n    front: {}\n",
			`line 9: mapping key "front" already defined at line 8`,
		},
		{
			"duplicate path key",
			"preview:\n  domain: example.com\n  expose:\n    front:\n      path: /a\n      path: /b\n",
			`line 10: mapping key "path" already defined at line 9`,
		},
		{
			"merge of a scalar",
			"preview:\n  <<: 1\n  domain: example.com\n",
			"map merge requires map or sequence of maps",
		},
		{"aliased domain not a string", "preview:\n  maxPreviews: &n 42\n  domain: *n\n", "preview.domain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := gitops.ParseCatalog([]byte(catalogServices + tt.preview))
			if err == nil {
				t.Fatal("ParseCatalog: want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q does not name key %q", err, tt.key)
			}
		})
	}
}

func TestParseCatalog_Label(t *testing.T) {
	tests := []struct {
		name    string
		preview string
		want    string
	}{
		{name: "absent block", want: "preview"},
		{name: "absent key", preview: "preview:\n  tls: false\n", want: "preview"},
		{name: "empty value", preview: "preview:\n  label:\n", want: "preview"},
		{name: "null", preview: "preview:\n  label: ~\n", want: "preview"},
		{name: "set", preview: "preview:\n  label: deploy-preview\n", want: "deploy-preview"},
		{name: "set next to a domain", preview: "preview:\n  domain: example.com\n  label: Preview\n", want: "Preview"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitops.ParseCatalog([]byte(catalogServices + tt.preview))
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			if got.Label != tt.want {
				t.Errorf("Label = %q, want %q", got.Label, tt.want)
			}
		})
	}
}

func TestParseCatalog_MalformedLabel(t *testing.T) {
	const want = "badil.yaml: malformed preview block: preview.label: must be a non-empty string"
	for name, preview := range map[string]string{
		"empty string": "preview:\n  label: \"\"\n",
		"number":       "preview:\n  label: 42\n",
		"boolean":      "preview:\n  label: true\n",
		"sequence":     "preview:\n  label: [preview]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gitops.ParseCatalog([]byte(catalogServices + preview))
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}

func TestParseCatalog_MaxPreviews(t *testing.T) {
	tests := []struct {
		name    string
		preview string
		want    int
	}{
		{name: "absent block", want: 0},
		{name: "absent key", preview: "preview:\n  tls: false\n", want: 0},
		{name: "empty value", preview: "preview:\n  maxPreviews:\n", want: 0},
		{name: "null", preview: "preview:\n  maxPreviews: ~\n", want: 0},
		{name: "set", preview: "preview:\n  maxPreviews: 3\n", want: 3},
		{name: "set without a domain", preview: "preview:\n  label: deploy\n  maxPreviews: 1\n", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitops.ParseCatalog([]byte(catalogServices + tt.preview))
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			if got.MaxPreviews != tt.want {
				t.Errorf("MaxPreviews = %d, want %d", got.MaxPreviews, tt.want)
			}
		})
	}
}

func TestParseCatalog_MalformedMaxPreviews(t *testing.T) {
	const want = "badil.yaml: malformed preview block: preview.maxPreviews: must be a positive integer"
	for name, preview := range map[string]string{
		"string":   "preview:\n  maxPreviews: \"3\"\n",
		"zero":     "preview:\n  maxPreviews: 0\n",
		"negative": "preview:\n  maxPreviews: -1\n",
		"float":    "preview:\n  maxPreviews: 1.5\n",
		"boolean":  "preview:\n  maxPreviews: true\n",
		"sequence": "preview:\n  maxPreviews: [3]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gitops.ParseCatalog([]byte(catalogServices + preview))
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}

func TestParseCatalog_Services(t *testing.T) {
	const raw = `services:
  front:
    repo: https://github.com/acme/front.git
  backend-2:
    repo: https://github.com/acme/backend-2.git
    branch: develop
  backend-1:
    repo: https://github.com/acme/backend-1.git
`
	got, err := gitops.ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if want := []string{"front", "backend-2", "backend-1"}; !reflect.DeepEqual(got.Services, want) {
		t.Errorf("Services = %v, want %v (file order)", got.Services, want)
	}
	wantRepos := map[string]string{
		"front":     "https://github.com/acme/front.git",
		"backend-2": "https://github.com/acme/backend-2.git",
		"backend-1": "https://github.com/acme/backend-1.git",
	}
	if !reflect.DeepEqual(got.Repos, wantRepos) {
		t.Errorf("Repos = %v, want %v", got.Repos, wantRepos)
	}
	if want := map[string]string{"backend-2": "develop"}; !reflect.DeepEqual(got.Branches, want) {
		t.Errorf("Branches = %#v, want %#v", got.Branches, want)
	}
}

func TestParseCatalog_Branch(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  map[string]string
	}{
		{name: "absent defaults to main downstream", entry: "{repo: r}"},
		{name: "set", entry: "{repo: r, branch: master}", want: map[string]string{"front": "master"}},
		{name: "yes is the string yes", entry: "{repo: r, branch: yes}", want: map[string]string{"front": "yes"}},
		{name: "on is the string on", entry: "{repo: r, branch: on}", want: map[string]string{"front": "on"}},
		{name: "quoted", entry: `{repo: r, branch: "no"}`, want: map[string]string{"front": "no"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitops.ParseCatalog([]byte("services:\n  front: " + tt.entry + "\n"))
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			if !reflect.DeepEqual(got.Branches, tt.want) {
				t.Errorf("Branches = %#v, want %#v", got.Branches, tt.want)
			}
		})
	}
}

func TestParseCatalog_ServiceAliases(t *testing.T) {
	const raw = `services:
  backend-1: &s
    repo: https://github.com/acme/monorepo.git
    branch: develop
  backend-2: *s
`
	got, err := gitops.ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if want := []string{"backend-1", "backend-2"}; !reflect.DeepEqual(got.Services, want) {
		t.Errorf("Services = %v, want %v", got.Services, want)
	}
	want := map[string]string{"backend-1": "develop", "backend-2": "develop"}
	if !reflect.DeepEqual(got.Branches, want) {
		t.Errorf("Branches = %#v, want %#v", got.Branches, want)
	}
}

func TestParseCatalog_MalformedServices(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty file", "", "badil.yaml: missing 'services' mapping"},
		{"missing services", "preview:\n  label: deploy\n", "badil.yaml: missing 'services' mapping"},
		{"null services", "services:\n", "badil.yaml: missing 'services' mapping"},
		{"services a sequence", "services: [front]\n", "badil.yaml: 'services' must be a mapping"},
		{"services a scalar", "services: front\n", "badil.yaml: 'services' must be a mapping"},
		{"empty services", "services: {}\n", "badil.yaml: 'services' must not be empty"},
		{
			"unknown top-level key",
			"services:\n  front: {repo: r}\nserviceRepos:\n  front: r\n",
			"badil.yaml: yaml: unmarshal errors:\n  line 3: field serviceRepos not found in type gitops.badilFile",
		},
		{
			"entry a string",
			"services:\n  front: https://github.com/acme/front.git\n",
			"badil.yaml: services.front must be a mapping",
		},
		{"entry null", "services:\n  front:\n", "badil.yaml: services.front must be a mapping"},
		{"entry a sequence", "services:\n  front: [r]\n", "badil.yaml: services.front must be a mapping"},
		{
			"unknown key in an entry",
			"services:\n  front:\n    repo: r\n    image: x\n",
			`badil.yaml: services.front: unknown key "image"`,
		},
		{"repo missing", "services:\n  front:\n    branch: main\n", "badil.yaml: services.front: missing 'repo'"},
		{"entry empty", "services:\n  front: {}\n", "badil.yaml: services.front: missing 'repo'"},
		{
			"repo a number",
			"services:\n  front:\n    repo: 1\n",
			"badil.yaml: services.front.repo must be a non-empty string",
		},
		{
			"repo empty",
			"services:\n  front:\n    repo: \"\"\n",
			"badil.yaml: services.front.repo must be a non-empty string",
		},
		{
			"repo null",
			"services:\n  front:\n    repo: ~\n",
			"badil.yaml: services.front.repo must be a non-empty string",
		},
		{
			"repo a boolean",
			"services:\n  front:\n    repo: true\n",
			"badil.yaml: services.front.repo must be a non-empty string",
		},
		{
			"repo a mapping",
			"services:\n  front:\n    repo: {url: r}\n",
			"badil.yaml: services.front.repo must be a non-empty string",
		},
		{
			"repo a sequence",
			"services:\n  front:\n    repo: [r]\n",
			"badil.yaml: services.front.repo must be a non-empty string",
		},
		{
			"branch null",
			"services:\n  front:\n    repo: r\n    branch: null\n",
			"badil.yaml: services.front.branch must be a non-empty string",
		},
		{
			"branch empty",
			"services:\n  front:\n    repo: r\n    branch: \"\"\n",
			"badil.yaml: services.front.branch must be a non-empty string",
		},
		{
			"branch a number",
			"services:\n  front:\n    repo: r\n    branch: 123\n",
			"badil.yaml: services.front.branch must be a non-empty string",
		},
		{
			"branch a float",
			"services:\n  front:\n    repo: r\n    branch: 1.5\n",
			"badil.yaml: services.front.branch must be a non-empty string",
		},
		{
			"branch a boolean",
			"services:\n  front:\n    repo: r\n    branch: true\n",
			"badil.yaml: services.front.branch must be a non-empty string",
		},
		{
			"first invalid entry in file order",
			"services:\n  front:\n    repo: 1\n  backend-1:\n    image: x\n",
			"badil.yaml: services.front.repo must be a non-empty string",
		},
		{
			"duplicate service",
			"services:\n  front: {repo: r}\n  front: {repo: s}\n",
			"badil.yaml: services.front is defined more than once",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := gitops.ParseCatalog([]byte(tt.raw))
			if err == nil || err.Error() != tt.want {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestServicesForRepo(t *testing.T) {
	const raw = `services:
  api: {repo: https://github.com/acme/monorepo.git}
  worker: {repo: https://github.com/Acme/MonoRepo/}
  front: {repo: https://github.com/acme/front}
  web: {repo: HTTPS://GITHUB.COM/ACME/WEB.GIT}
  ssh: {repo: "git@github.com:acme/ssh.git"}
  ghe: {repo: https://github.acme.example/acme/ghe.git}
`
	catalog, err := gitops.ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	tests := []struct {
		name, owner, repo string
		want              []string
	}{
		{"two services on one repo, .git and trailing slash", "acme", "monorepo", []string{"api", "worker"}},
		{"case-insensitive owner and repo", "ACME", "MonoRepo", []string{"api", "worker"}},
		{"no .git", "acme", "front", []string{"front"}},
		{"uppercase url", "acme", "web", []string{"web"}},
		{"ssh url never matches", "acme", "ssh", nil},
		{"enterprise url never matches", "acme", "ghe", nil},
		{"unknown repo", "acme", "unknown", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := catalog.ServicesForRepo(tt.owner, tt.repo); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ServicesForRepo(%q, %q) = %v, want %v", tt.owner, tt.repo, got, tt.want)
			}
		})
	}
}
