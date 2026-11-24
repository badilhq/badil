package main

import (
	"regexp"
	"testing"
)

func TestBootstrapAppCmd_NameDefault(t *testing.T) {
	t.Parallel()

	re := regexp.MustCompile(`^badil-dev-[0-9a-f]{6}$`)
	if flag := bootstrapAppCmd().Flags().Lookup("name"); !re.MatchString(flag.DefValue) {
		t.Fatalf("--name default = %q, want match %s", flag.DefValue, re)
	}
}
