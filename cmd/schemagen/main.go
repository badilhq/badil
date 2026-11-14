// Command schemagen writes the JSON Schema of the preview-env chart's values,
// generated from internal/chartvalues.Values, and the JSON Schema of the
// preview values contract, generated from internal/preview.PreviewValues.
package main

//go:generate go run . -out ../../charts/preview-env/values.schema.json -contract-out ../../schema/preview.v1.schema.json

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/invopop/jsonschema"

	"github.com/badilhq/badil/internal/chartvalues"
	"github.com/badilhq/badil/internal/preview"
)

func main() {
	out := flag.String("out", "values.schema.json", "file to write the chart values schema to")
	contractOut := flag.String("contract-out", "preview.v1.schema.json", "file to write the preview contract schema to")
	flag.Parse()
	if err := run(*out, *contractOut); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}

// run writes the chart values schema to path and the contract schema to
// contractPath.
func run(path, contractPath string) error {
	for _, f := range []struct {
		path string
		gen  func() ([]byte, error)
	}{
		{path, generate},
		{contractPath, generateContract},
	} {
		b, err := f.gen()
		if err != nil {
			return err
		}
		if err = os.WriteFile(f.path, b, 0o644); err != nil { //nolint:gosec // a published schema, readable by all
			return err
		}
	}
	return nil
}

// newReflector returns the reflector of every badil schema: yaml field names,
// every type inlined, and no $id.
func newReflector() *jsonschema.Reflector {
	return &jsonschema.Reflector{
		FieldNameTag:   "yaml",
		DoNotReference: true,
		ExpandedStruct: true,
		Anonymous:      true,
	}
}

// generate returns the schema of chartvalues.Values as indented JSON with a
// trailing newline.
func generate() ([]byte, error) {
	return marshal(newReflector().Reflect(&chartvalues.Values{}))
}

// generateContract returns the schema of preview.PreviewValues, the preview
// values contract, as indented JSON with a trailing newline.
func generateContract() ([]byte, error) {
	return marshal(newReflector().Reflect(&preview.PreviewValues{}))
}

// marshal returns s as indented JSON with a trailing newline.
func marshal(s *jsonschema.Schema) ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return append(b, '\n'), nil
}
