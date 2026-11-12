package main

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// version is the badil version set at link time with
// -ldflags "-X main.version=<v>"; it is empty in a build that does not set it.
var version string

// develVersion is the main module version Go records when it has none.
const develVersion = "(devel)"

// resolveVersion returns the version badil reports: linked when not empty,
// else module (the main module version of the build info) when it is neither
// empty nor (devel), else dev. A leading v is removed.
func resolveVersion(linked, module string) string {
	v := linked
	if v == "" && module != develVersion {
		v = module
	}
	if v == "" {
		v = "dev"
	}
	return strings.TrimPrefix(v, "v")
}

// buildVersion is resolveVersion for this binary.
func buildVersion() string {
	module := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		module = info.Main.Version
	}
	return resolveVersion(version, module)
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the badil version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "badil version %s\n", buildVersion())
			return err
		},
	}
}
