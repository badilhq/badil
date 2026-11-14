package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"
)

// contractDir holds the conformance fixtures of the v1 contract.
const contractDir = "../../testdata/contract/v1"

// wantPrefix starts the line of an invalid fixture that names the error it
// must fail with.
const wantPrefix = "# want: "

// compileContract compiles the published contract schema with the validator
// Helm uses.
func compileContract(t *testing.T) *jsonschema.Schema {
	t.Helper()
	b, err := os.ReadFile("../../schema/preview.v1.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	const url = "file:///preview.v1.schema.json"
	c := jsonschema.NewCompiler()
	if err = c.AddResource(url, doc); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

// readFixture decodes a fixture as Helm reads a values file.
func readFixture(t *testing.T, path string) ([]byte, map[string]any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var vals map[string]any
	if err = yaml.Unmarshal(data, &vals); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return data, vals
}

// fixtures returns the fixture files of dir, failing when there is none.
func fixtures(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(contractDir, dir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixture in %s", dir)
	}
	return files
}

func TestContract_ValidFixtures(t *testing.T) {
	sch := compileContract(t)
	for _, f := range fixtures(t, "valid") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			_, vals := readFixture(t, f)
			if err := sch.Validate(vals); err != nil {
				t.Errorf("validate: %v", err)
			}
		})
	}
}

func TestContract_InvalidFixtures(t *testing.T) {
	sch := compileContract(t)
	for _, f := range fixtures(t, "invalid") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, vals := readFixture(t, f)
			var want string
			for line := range strings.Lines(string(data)) {
				if s, ok := strings.CutPrefix(line, wantPrefix); ok {
					want = strings.TrimSpace(s)
					break
				}
			}
			if want == "" {
				t.Fatalf("no %q line", strings.TrimSpace(wantPrefix))
			}
			err := sch.Validate(vals)
			if err == nil {
				t.Fatalf("validate: no error, want %q", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("validate: %v\nwant %q", err, want)
			}
		})
	}
}
