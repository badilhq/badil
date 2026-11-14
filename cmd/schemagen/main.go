// Command schemagen writes the JSON Schema of the preview-env chart's values,
// generated from internal/chartvalues.Values.
package main

//go:generate go run . -out ../../charts/preview-env/values.schema.json

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/invopop/jsonschema"

	"github.com/badilhq/badil/internal/chartvalues"
)

func main() {
	out := flag.String("out", "values.schema.json", "file to write the schema to")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}

// run writes the schema to path.
func run(path string) error {
	b, err := generate()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644) //nolint:gosec // a chart file, readable by all
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
	b, err := json.MarshalIndent(newReflector().Reflect(&chartvalues.Values{}), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return append(b, '\n'), nil
}
