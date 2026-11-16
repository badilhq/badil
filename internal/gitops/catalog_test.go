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

func TestParseCatalog_ArgoCDURL(t *testing.T) {
	tests := []struct {
		name    string
		preview string
		want    string
	}{
		{name: "absent block"},
		{name: "absent key", preview: "preview:\n  tls: false\n"},
		{name: "null", preview: "preview:\n  argocdURL: ~\n"},
		{
			name: "https", preview: "preview:\n  argocdURL: https://argocd.example.com\n",
			want: "https://argocd.example.com",
		},
		{name: "http", preview: "preview:\n  argocdURL: http://argocd.local\n", want: "http://argocd.local"},
		{
			name: "trailing slash removed", preview: "preview:\n  argocdURL: https://argocd.example.com/\n",
			want: "https://argocd.example.com",
		},
		{
			name: "percent-encoded path", preview: "preview:\n  argocdURL: https://ops.example.com/argo%20cd\n",
			want: "https://ops.example.com/argo%20cd",
		},
		{
			name: "port and path", preview: "preview:\n  argocdURL: https://ops.example.com:8443/argocd/\n",
			want: "https://ops.example.com:8443/argocd",
		},
		{
			name:    "set without a domain",
			preview: "preview:\n  label: deploy\n  argocdURL: https://argocd.example.com\n",
			want:    "https://argocd.example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitops.ParseCatalog([]byte(catalogServices + tt.preview))
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			if got.ArgoCDURL != tt.want {
				t.Errorf("ArgoCDURL = %q, want %q", got.ArgoCDURL, tt.want)
			}
		})
	}
}

func TestParseCatalog_MalformedArgoCDURL(t *testing.T) {
	const prefix = "badil.yaml: malformed preview block: preview.argocdURL: "
	tests := map[string]struct {
		preview string
		want    string
	}{
		"number":       {"preview:\n  argocdURL: 42\n", "must be a string"},
		"boolean":      {"preview:\n  argocdURL: true\n", "must be a string"},
		"sequence":     {"preview:\n  argocdURL: [https://argocd.example.com]\n", "must be a string"},
		"mapping":      {"preview:\n  argocdURL: {host: argocd.example.com}\n", "must be a string"},
		"empty string": {"preview:\n  argocdURL: \"\"\n", `"" must be an absolute http or https URL with a host`},
		"no scheme": {
			"preview:\n  argocdURL: argocd.example.com\n",
			`"argocd.example.com" must be an absolute http or https URL with a host`,
		},
		"other scheme": {
			"preview:\n  argocdURL: ftp://argocd.example.com\n",
			`"ftp://argocd.example.com" must be an absolute http or https URL with a host`,
		},
		"missing host": {
			"preview:\n  argocdURL: https:///applications\n",
			`"https:///applications" must be an absolute http or https URL with a host`,
		},
		"unparsable": {
			"preview:\n  argocdURL: \"https://argo cd.example.com\"\n",
			`"https://argo cd.example.com" must be an absolute http or https URL with a host`,
		},
		"query": {
			"preview:\n  argocdURL: https://argocd.example.com/?a=b\n",
			`"https://argocd.example.com/?a=b" must not have a query or a fragment`,
		},
		"empty query": {
			"preview:\n  argocdURL: https://argocd.example.com?\n",
			`"https://argocd.example.com?" must not have a query or a fragment`,
		},
		"fragment": {
			"preview:\n  argocdURL: \"https://argocd.example.com/#apps\"\n",
			`"https://argocd.example.com/#apps" must not have a query or a fragment`,
		},
		"credentials": {
			"preview:\n  argocdURL: https://admin:s3cret@argocd.example.com\n",
			`"https://admin:s3cret@argocd.example.com" must not contain credentials`,
		},
		"user only": {
			"preview:\n  argocdURL: https://admin@argocd.example.com\n",
			`"https://admin@argocd.example.com" must not contain credentials`,
		},
		"space in path": {
			"preview:\n  argocdURL: \"https://argocd.example.com/a b\"\n",
			`"https://argocd.example.com/a b" must only contain URL characters, without spaces or parentheses`,
		},
		"closing parenthesis": {
			"preview:\n  argocdURL: https://argocd.example.com/a)b\n",
			`"https://argocd.example.com/a)b" must only contain URL characters, without spaces or parentheses`,
		},
		"opening parenthesis": {
			"preview:\n  argocdURL: https://argocd.example.com/a(b\n",
			`"https://argocd.example.com/a(b" must only contain URL characters, without spaces or parentheses`,
		},
		"angle bracket": {
			"preview:\n  argocdURL: https://argocd.example.com/a<b\n",
			`"https://argocd.example.com/a<b" must only contain URL characters, without spaces or parentheses`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := gitops.ParseCatalog([]byte(catalogServices + tt.preview))
			if err == nil || err.Error() != prefix+tt.want {
				t.Fatalf("err = %v, want %q", err, prefix+tt.want)
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
		{
			name: "date-like is the string", entry: "{repo: r, branch: 2024-01-01}",
			want: map[string]string{"front": "2024-01-01"},
		},
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
			"duplicate repo in an entry",
			"services:\n  front:\n    repo: a\n    repo: b\n",
			"badil.yaml: services.front.repo is defined more than once",
		},
		{
			"duplicate branch in an entry",
			"services:\n  front: {repo: r, branch: a, branch: b}\n",
			"badil.yaml: services.front.branch is defined more than once",
		},
		{
			"duplicate service",
			"services:\n  front: {repo: r}\n  front: {repo: s}\n",
			"badil.yaml: services.front is defined more than once",
		},
		{
			"service name a number",
			"services:\n  1: {repo: r}\n",
			`badil.yaml: services: keys must be strings, got "1"`,
		},
		{
			"service name a mapping",
			"services:\n  ? {a: b}\n  : {repo: r}\n",
			`badil.yaml: services: keys must be strings, got ""`,
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

func TestParseCatalog_RendererSettings(t *testing.T) {
	const raw = `imageTag: "sha-{sha7}"
namespace:
  labels: {pod-security.kubernetes.io/enforce: baseline, team: ""}
  annotations: {owner: platform}
services:
  backend:
    repo: https://github.com/acme/backend.git
    imageTag: "{sha8}-amd64"
    chart:
      path: deploy/helm
      imageTagParam: image.tag
      hostParam: ingress.hostname
      valueFiles: [values-preview.yaml, /shared/values.yaml]
  front:
    repo: https://github.com/acme/front.git
    chart: {}
  worker:
    repo: https://github.com/acme/worker.git
    chart: {hostParam: ingress.host}
`
	got, err := gitops.ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if got.ImageTag != "sha-{sha7}" {
		t.Errorf("ImageTag = %q, want sha-{sha7}", got.ImageTag)
	}
	if want := map[string]string{"backend": "{sha8}-amd64"}; !reflect.DeepEqual(got.ImageTags, want) {
		t.Errorf("ImageTags = %#v, want %#v", got.ImageTags, want)
	}
	wantCharts := map[string]preview.ChartSettings{
		"backend": {
			Path: "deploy/helm", ImageTagParam: "image.tag", HostParam: "ingress.hostname",
			ValueFiles: []string{"values-preview.yaml", "/shared/values.yaml"},
		},
		"front":  {},
		"worker": {HostParam: "ingress.host"},
	}
	if !reflect.DeepEqual(got.Charts, wantCharts) {
		t.Errorf("Charts = %#v, want %#v", got.Charts, wantCharts)
	}
	wantNS := &preview.NamespaceMetadata{
		Labels:      map[string]string{"pod-security.kubernetes.io/enforce": "baseline", "team": ""},
		Annotations: map[string]string{"owner": "platform"},
	}
	if !reflect.DeepEqual(got.Namespace, wantNS) {
		t.Errorf("Namespace = %#v, want %#v", got.Namespace, wantNS)
	}
}

func TestParseCatalog_RendererDefaults(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "absent", raw: catalogServices},
		{name: "empty namespace", raw: catalogServices + "namespace: {}\n"},
		{name: "empty labels and annotations", raw: catalogServices + "namespace: {labels: {}, annotations: {}}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitops.ParseCatalog([]byte(tt.raw))
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			if got.ImageTag != "" || got.ImageTags != nil || got.Charts != nil || got.Namespace != nil {
				t.Errorf(`ImageTag, ImageTags, Charts, Namespace = %q, %v, %v, %v; want "", nil, nil, nil`,
					got.ImageTag, got.ImageTags, got.Charts, got.Namespace)
			}
		})
	}
}

func TestParseCatalog_MalformedRendererSettings(t *testing.T) {
	const badTag = " must be a quoted string made of an optional prefix, one of {sha}, {sha7} or {sha8}, " +
		"and an optional suffix ([a-zA-Z0-9_.-], the prefix not starting with . or -), at most 93 characters"
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"root imageTag null", catalogServices + "imageTag:\n", "badil.yaml: imageTag" + badTag},
		{"root imageTag empty", catalogServices + "imageTag: \"\"\n", "badil.yaml: imageTag" + badTag},
		{"root imageTag unquoted", catalogServices + "imageTag: {sha}\n", "badil.yaml: imageTag" + badTag},
		{"root imageTag a number", catalogServices + "imageTag: 1\n", "badil.yaml: imageTag" + badTag},
		{"root imageTag no placeholder", catalogServices + "imageTag: latest\n", "badil.yaml: imageTag" + badTag},
		{
			"service imageTag bad",
			"services:\n  front: {repo: r, imageTag: \"x:{sha}\"}\n",
			"badil.yaml: services.front.imageTag" + badTag,
		},
		{
			"service imageTag null",
			"services:\n  front: {repo: r, imageTag: ~}\n",
			"badil.yaml: services.front.imageTag" + badTag,
		},
		{"namespace null", catalogServices + "namespace:\n", "badil.yaml: namespace must be a mapping"},
		{"namespace a list", catalogServices + "namespace: [a]\n", "badil.yaml: namespace must be a mapping"},
		{
			"namespace unknown key",
			catalogServices + "namespace: {label: {a: b}}\n",
			`badil.yaml: namespace: unknown key "label"`,
		},
		{
			"namespace labels null",
			catalogServices + "namespace: {labels: ~}\n",
			"badil.yaml: namespace.labels must be a mapping",
		},
		{
			"namespace annotations a list",
			catalogServices + "namespace: {annotations: [a]}\n",
			"badil.yaml: namespace.annotations must be a mapping",
		},
		{
			"namespace label reserved",
			catalogServices + "namespace: {labels: {app.kubernetes.io/part-of: x}}\n",
			"badil.yaml: namespace.labels.app.kubernetes.io/part-of is set by the preview-apps chart " +
				"and cannot be overridden",
		},
		{
			"namespace annotation reserved",
			catalogServices + "namespace: {annotations: {argocd.argoproj.io/sync-wave: \"0\"}}\n",
			"badil.yaml: namespace.annotations.argocd.argoproj.io/sync-wave is set by the preview-apps chart " +
				"and cannot be overridden",
		},
		{
			"namespace label a boolean",
			catalogServices + "namespace: {labels: {enabled: true}}\n",
			"badil.yaml: namespace.labels.enabled must be a string",
		},
		{
			"namespace label null",
			catalogServices + "namespace: {labels: {team: ~}}\n",
			"badil.yaml: namespace.labels.team must be a string",
		},
		{
			"namespace annotation a list",
			catalogServices + "namespace: {annotations: {owner: [a]}}\n",
			"badil.yaml: namespace.annotations.owner must be a string",
		},
		{
			"namespace label empty key",
			catalogServices + "namespace: {labels: {\"\": a}}\n",
			"badil.yaml: namespace.labels: keys must be non-empty strings",
		},
		{
			"namespace label duplicate",
			catalogServices + "namespace:\n  labels:\n    team: a\n    team: b\n",
			"badil.yaml: namespace.labels.team is defined more than once",
		},
		{
			"namespace duplicate key",
			catalogServices + "namespace:\n  labels: {}\n  labels: {}\n",
			"badil.yaml: namespace.labels is defined more than once",
		},
		{
			"chart null",
			"services:\n  front: {repo: r, chart: ~}\n",
			"badil.yaml: services.front.chart must be a mapping",
		},
		{
			"chart a string",
			"services:\n  front: {repo: r, chart: k8s/}\n",
			"badil.yaml: services.front.chart must be a mapping",
		},
		{
			"chart unknown key",
			"services:\n  front: {repo: r, chart: {branch: main}}\n",
			`badil.yaml: services.front.chart: unknown key "branch"`,
		},
		{
			"chart path empty",
			"services:\n  front: {repo: r, chart: {path: \"\"}}\n",
			"badil.yaml: services.front.chart.path must be a non-empty string",
		},
		{
			"chart path null",
			"services:\n  front: {repo: r, chart: {path: ~}}\n",
			"badil.yaml: services.front.chart.path must be a non-empty string",
		},
		{
			"chart imageTagParam a number",
			"services:\n  front: {repo: r, chart: {imageTagParam: 1}}\n",
			"badil.yaml: services.front.chart.imageTagParam must be a non-empty string",
		},
		{
			"chart hostParam a boolean",
			"services:\n  front: {repo: r, chart: {hostParam: false}}\n",
			"badil.yaml: services.front.chart.hostParam must be a non-empty string",
		},
		{
			"chart valueFiles empty",
			"services:\n  front: {repo: r, chart: {valueFiles: []}}\n",
			"badil.yaml: services.front.chart.valueFiles must be a non-empty list of non-empty strings",
		},
		{
			"chart valueFiles null",
			"services:\n  front: {repo: r, chart: {valueFiles: ~}}\n",
			"badil.yaml: services.front.chart.valueFiles must be a non-empty list of non-empty strings",
		},
		{
			"chart valueFiles a string",
			"services:\n  front: {repo: r, chart: {valueFiles: values.yaml}}\n",
			"badil.yaml: services.front.chart.valueFiles must be a non-empty list of non-empty strings",
		},
		{
			"chart valueFiles empty item",
			"services:\n  front: {repo: r, chart: {valueFiles: [a, \"\"]}}\n",
			"badil.yaml: services.front.chart.valueFiles must be a non-empty list of non-empty strings",
		},
		{
			"chart valueFiles null item",
			"services:\n  front: {repo: r, chart: {valueFiles: [~]}}\n",
			"badil.yaml: services.front.chart.valueFiles must be a non-empty list of non-empty strings",
		},
		{
			"chart duplicate key",
			"services:\n  front: {repo: r, chart: {path: a, path: b}}\n",
			"badil.yaml: services.front.chart.path is defined more than once",
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

func TestParseCatalog_Dependencies(t *testing.T) {
	const raw = `services:
  api: {repo: https://github.com/acme/api.git}
dependencies:
  postgres:
    chart: oci://registry-1.docker.io/bitnamicharts/postgresql
    version: 16.4.2
    values:
      auth: {password: preview}
      replicas: 1.0
      since: 2024-01-01
    valueFiles: [deps/postgres.yaml]
  mailpit:
    repo: https://jouve.github.io/charts
    chart: mailpit
    version: "0.20"
    hostParam: ingress.hostname
    values: {}
preview:
  domain: example.com
  expose: {api: {}, mailpit: {}}
`
	got, err := gitops.ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	want := []preview.DependencyEntry{
		{
			Name: "postgres",
			Chart: preview.DependencyChart{
				RepoURL: "oci://registry-1.docker.io/bitnamicharts/postgresql", Version: "16.4.2",
			},
			Values: map[string]any{
				"auth": map[string]any{"password": "preview"}, "replicas": 1.0, "since": "2024-01-01",
			},
			ValueFiles: []string{"deps/postgres.yaml"},
		},
		{
			Name: "mailpit",
			Chart: preview.DependencyChart{
				RepoURL: "https://jouve.github.io/charts",
				Name:    "mailpit",
				Version: "0.20",
			},
			HostParam: "ingress.hostname",
		},
	}
	if !reflect.DeepEqual(got.Dependencies, want) {
		t.Errorf("Dependencies = %#v, want %#v", got.Dependencies, want)
	}
	if want := []string{"api"}; !reflect.DeepEqual(got.Services, want) {
		t.Errorf("Services = %v, want %v", got.Services, want)
	}
}

func TestParseCatalog_NoDependencies(t *testing.T) {
	for _, raw := range []string{catalogServices, catalogServices + "dependencies: {}\n"} {
		got, err := gitops.ParseCatalog([]byte(raw))
		if err != nil {
			t.Fatalf("ParseCatalog: %v", err)
		}
		if got.Dependencies != nil {
			t.Errorf("Dependencies = %#v, want nil", got.Dependencies)
		}
	}
}

func TestParseCatalog_MalformedDependencies(t *testing.T) {
	const oci = "chart: oci://r/c, version: 1.0.0"
	const helmRepo = "repo: https://charts.example.com, chart: c, version: 1.0.0"
	tests := []struct {
		name string
		deps string
		want string
	}{
		{"null", "dependencies:\n", "badil.yaml: dependencies must be a mapping"},
		{"a list", "dependencies: [a]\n", "badil.yaml: dependencies must be a mapping"},
		{"entry null", "dependencies: {db: ~}\n", "badil.yaml: dependencies.db must be a mapping"},
		{
			"name of a service",
			"dependencies: {front: {" + oci + "}}\n",
			`badil.yaml: dependencies.front: "front" is already a service`,
		},
		{
			"duplicate name",
			"dependencies:\n  db: {" + oci + "}\n  db: {" + oci + "}\n",
			"badil.yaml: dependencies.db is defined more than once",
		},
		{
			"repo with an oci chart",
			"dependencies: {db: {" + oci + ", repo: https://charts.example.com}}\n",
			"badil.yaml: dependencies.db: 'repo' must be absent when 'chart' is an oci:// reference",
		},
		{"missing chart", "dependencies: {db: {version: 1.0.0}}\n", "badil.yaml: dependencies.db: missing 'chart'"},
		{
			"missing version",
			"dependencies: {db: {chart: oci://r/c}}\n",
			"badil.yaml: dependencies.db: missing 'version'",
		},
		{
			"version a number",
			"dependencies: {db: {chart: oci://r/c, version: 1.0}}\n",
			"badil.yaml: dependencies.db.version must be a non-empty string",
		},
		{
			"missing repo",
			"dependencies: {db: {chart: c, version: 1.0.0}}\n",
			"badil.yaml: dependencies.db: missing 'repo' ('chart' is not an oci:// reference)",
		},
		{
			"repo not https",
			"dependencies: {db: {repo: http://charts.example.com, chart: c, version: 1.0.0}}\n",
			"badil.yaml: dependencies.db.repo must be an https:// Helm repository URL",
		},
		{
			"repo without a host",
			"dependencies: {db: {repo: \"https://\", chart: c, version: 1.0.0}}\n",
			"badil.yaml: dependencies.db.repo must be an https:// Helm repository URL",
		},
		{
			"oci chart without a host",
			"dependencies: {db: {chart: \"oci://\", version: 1.0.0}}\n",
			"badil.yaml: dependencies.db.chart must be an oci:// reference with a host",
		},
		{
			"unknown key",
			"dependencies: {db: {" + helmRepo + ", path: k8s/}}\n",
			`badil.yaml: dependencies.db: unknown key "path"`,
		},
		{
			"values a list",
			"dependencies: {db: {" + oci + ", values: [a]}}\n",
			"badil.yaml: dependencies.db.values must be a mapping",
		},
		{
			"valueFiles empty",
			"dependencies: {db: {" + oci + ", valueFiles: []}}\n",
			"badil.yaml: dependencies.db.valueFiles must be a non-empty list of non-empty strings",
		},
		{
			"hostParam empty",
			"dependencies: {db: {" + oci + ", hostParam: \"\"}}\n",
			"badil.yaml: dependencies.db.hostParam must be a non-empty string",
		},
		{
			"exposed without hostParam",
			"dependencies: {db: {" + oci + "}}\npreview: {domain: example.com, expose: {db: {}}}\n",
			"badil.yaml: dependencies.db: missing 'hostParam' (required when preview.expose.db is set)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := gitops.ParseCatalog([]byte(catalogServices + tt.deps))
			if err == nil || err.Error() != tt.want {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// needsBadil is a badil.yaml whose services declare needs.
const needsBadil = `services:
  front: {repo: https://github.com/acme/front.git, needs: [backend]}
  backend: {repo: https://github.com/acme/backend.git, needs: [postgres, redis]}
  payments: {repo: https://github.com/acme/payments.git, needs: [backend, kafka, postgres]}
  search: {repo: https://github.com/acme/search.git, needs: [elasticsearch]}
dependencies:
  postgres: {chart: oci://r/postgresql, version: 1.0.0}
  redis: {chart: oci://r/redis, version: 1.0.0}
  kafka: {chart: oci://r/kafka, version: 1.0.0}
  elasticsearch: {chart: oci://r/elasticsearch, version: 1.0.0}
`

func TestParseCatalog_Needs(t *testing.T) {
	raw := strings.Replace(
		needsBadil,
		"version: 1.0.0}\n  elasticsearch",
		"version: 1.0.0, needs: [zookeeper]}\n  zookeeper: {chart: oci://r/zookeeper, version: 1.0.0}\n  elasticsearch",
		1,
	)
	got, err := gitops.ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	want := map[string][]string{
		"front":    {"backend"},
		"backend":  {"postgres", "redis"},
		"payments": {"backend", "kafka", "postgres"},
		"search":   {"elasticsearch"},
		"kafka":    {"zookeeper"},
	}
	if !reflect.DeepEqual(got.Needs, want) {
		t.Errorf("Needs = %v, want %v", got.Needs, want)
	}
}

func TestParseCatalog_NeedsCycle(t *testing.T) {
	const raw = `services:
  a: {repo: r, needs: [b]}
  b: {repo: s, needs: [a]}
`
	got, err := gitops.ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if want := map[string][]string{"a": {"b"}, "b": {"a"}}; !reflect.DeepEqual(got.Needs, want) {
		t.Errorf("Needs = %v, want %v", got.Needs, want)
	}
}

func TestParseCatalog_NoNeeds(t *testing.T) {
	got, err := gitops.ParseCatalog(
		[]byte(catalogServices + "dependencies: {db: {chart: oci://r/c, version: 1.0.0}}\n"),
	)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if got.Needs != nil {
		t.Errorf("Needs = %v, want nil", got.Needs)
	}
}

func TestParseCatalog_MalformedNeeds(t *testing.T) {
	const db = "dependencies: {db: {chart: oci://r/c, version: 1.0.0, needs: %s}}\n"
	svc := func(needs string) string {
		return "services:\n  front: {repo: r, needs: " + needs + "}\n  api: {repo: s}\n" +
			"dependencies: {db: {chart: oci://r/c, version: 1.0.0}}\n"
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			"unknown name",
			svc("[api, cache]"),
			`badil.yaml: services.front.needs: "cache" is neither a service nor a dependency`,
		},
		{"self", svc("[api, front]"), `badil.yaml: services.front.needs: "front" cannot need itself`},
		{"duplicate", svc("[db, api, db]"), `badil.yaml: services.front.needs: "db" is listed more than once`},
		{
			"empty list",
			svc("[]"),
			"badil.yaml: services.front.needs must be a non-empty list of service or dependency names",
		},
		{
			"not a list",
			svc("api"),
			"badil.yaml: services.front.needs must be a non-empty list of service or dependency names",
		},
		{
			"empty name",
			svc(`[""]`),
			"badil.yaml: services.front.needs must be a non-empty list of service or dependency names",
		},
		{
			"dependency unknown name",
			catalogServices + strings.Replace(db, "%s", "[cache]", 1),
			`badil.yaml: dependencies.db.needs: "cache" is neither a service nor a dependency`,
		},
		{
			"dependency self",
			catalogServices + strings.Replace(db, "%s", "[db]", 1),
			`badil.yaml: dependencies.db.needs: "db" cannot need itself`,
		},
		{
			"dependency duplicate",
			catalogServices + strings.Replace(db, "%s", "[front, front]", 1),
			`badil.yaml: dependencies.db.needs: "front" is listed more than once`,
		},
		{
			"dependency empty list",
			catalogServices + strings.Replace(db, "%s", "[]", 1),
			"badil.yaml: dependencies.db.needs must be a non-empty list of service or dependency names",
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

func TestParseCatalog_Paths(t *testing.T) {
	const raw = `services:
  api:
    repo: https://github.com/acme/platform.git
    paths: ["api/**", "libs/common/**", "go.mod"]
  web: {repo: https://github.com/acme/platform.git, paths: ["web/**"]}
  docs: {repo: https://github.com/acme/platform.git}
`
	got, err := gitops.ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	want := map[string][]string{
		"api": {"api/**", "libs/common/**", "go.mod"},
		"web": {"web/**"},
	}
	if !reflect.DeepEqual(got.Paths, want) {
		t.Errorf("Paths = %v, want %v", got.Paths, want)
	}
}

func TestParseCatalog_NoPaths(t *testing.T) {
	got, err := gitops.ParseCatalog([]byte(catalogServices))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if got.Paths != nil {
		t.Errorf("Paths = %v, want nil", got.Paths)
	}
}

func TestParseCatalog_MalformedPaths(t *testing.T) {
	const list = "badil.yaml: services.api.paths must be a non-empty list of non-empty strings"
	svc := func(paths string) string {
		return "services:\n  api: {repo: r, paths: " + paths + "}\n"
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"null", svc("null"), list},
		{"not a list", svc(`"api/**"`), list},
		{"mapping", svc("{a: b}"), list},
		{"empty list", svc("[]"), list},
		{"empty string", svc(`["api/**", ""]`), list},
		{"non-string item", svc("[api/**, 42]"), list},
		{"list item", svc("[[api/**]]"), list},
		{"invalid pattern", svc(`["api/**", "api/["]`), `badil.yaml: services.api.paths[1]: invalid pattern "api/["`},
		{"invalid first pattern", svc(`["{a,b"]`), `badil.yaml: services.api.paths[0]: invalid pattern "{a,b"`},
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
