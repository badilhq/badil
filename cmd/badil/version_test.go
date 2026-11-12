package main

import (
	"bytes"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, linked, module, want string
	}{
		{name: "link-time value first", linked: "v1.2.3", module: "v0.15.1", want: "1.2.3"},
		{name: "link-time value without v", linked: "1.2.3", module: "v0.15.1", want: "1.2.3"},
		{name: "build-info version", module: "v0.15.1", want: "0.15.1"},
		{name: "devel build info", module: "(devel)", want: "dev"},
		{name: "empty build info", want: "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := resolveVersion(tc.linked, tc.module); got != tc.want {
				t.Errorf("resolveVersion(%q, %q) = %q, want %q", tc.linked, tc.module, got, tc.want)
			}
		})
	}
}

func TestVersionOutput(t *testing.T) {
	t.Parallel()

	want := "badil version " + buildVersion() + "\n"
	for _, args := range [][]string{{"version"}, {"--version"}} {
		root := rootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out.String() != want {
			t.Errorf("%v printed %q, want %q", args, out.String(), want)
		}
	}
}
