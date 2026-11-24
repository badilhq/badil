package preview_test

import (
	"testing"

	"github.com/badilhq/badil/internal/preview"
)

func TestValidStatus(t *testing.T) {
	for _, s := range []string{"ready", "progressing", "degraded", "sync-failed"} {
		if !preview.ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = false", s)
		}
	}
	for _, s := range []string{"", "healthy", "Ready", "unknown"} {
		if preview.ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = true", s)
		}
	}
}
