package store_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/badilhq/badil/internal/store"
)

// TestParseCatalog_ShippedFiles parses the badil.yaml files badil ships: the
// example and the gitops repo template's.
func TestParseCatalog_ShippedFiles(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{path: "../../examples/argocd/badil.yaml", want: []string{"backend-1", "backend-2", "front"}},
		{path: "../../template/badil.yaml", want: []string{"my-service"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			raw, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			c, err := store.ParseCatalog(raw)
			if err != nil {
				t.Fatalf("ParseCatalog: %v", err)
			}
			if !reflect.DeepEqual(c.Services, tt.want) {
				t.Errorf("services = %v, want %v", c.Services, tt.want)
			}
		})
	}
}
