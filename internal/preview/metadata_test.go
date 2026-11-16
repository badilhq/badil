package preview_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/preview"
)

// singleQuotedValues is a values file whose metadata is single-quoted, as a
// hand edit or another YAML writer may leave it, so it must still decode.
const singleQuotedValues = `apiVersion: badil.dev/v1beta1
gitopsRepoURL: https://github.com/acme/gitops.git
services:
  - name: backend-1
    repoURL: https://github.com/acme/backend-1.git
    branch: main
    commitSha: abc1234567890
    imageTag: abc1234567890
    chart:
      path: k8s/
      imageTagParam: app.image.tag
      hostParam: app.ingress.host
    metadata:
      pr-author: octocat
      pr-url: https://github.com/acme/backend-1/pull/42
      pr-number: '42'
      created-at: '2026-05-26T10:00:00Z'
      updated-at: '2026-05-26T10:00:00Z'
      vcs.ref.name: feature/add-pricing
      cicd.pipeline.run.url: https://github.com/acme/backend-1/actions/runs/1
  - name: backend-2
    repoURL: https://github.com/acme/backend-2.git
    branch: develop
    imageTag: ${ARGOCD_APP_REVISION}
    chart:
      path: k8s/
      imageTagParam: app.image.tag
      hostParam: app.ingress.host
  - name: front
    repoURL: https://github.com/acme/front
    branch: main
    imageTag: ${ARGOCD_APP_REVISION}
    chart:
      path: deploy/chart
      imageTagParam: app.image.tag
      hostParam: app.ingress.host
      valueFiles:
        - values-preview.yaml
dependencies: []
`

func encodeValues(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close encoder: %v", err)
	}
	return buf.Bytes()
}

func TestMetadata_DecodesSingleQuotedValues(t *testing.T) {
	var vals preview.PreviewValues
	if err := yaml.Unmarshal([]byte(singleQuotedValues), &vals); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := vals.Services[0].Metadata.PRNumber; got != "42" {
		t.Errorf("pr-number = %q, want 42", got)
	}
	want, err := os.ReadFile("testdata/values.golden.yaml")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got := encodeValues(t, vals); !bytes.Equal(got, want) {
		t.Errorf("re-encode drift\n--- want (golden) ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestMetadata_NonStringValueIsError(t *testing.T) {
	const in = "pr-author:\n  name: octocat\n"
	var md preview.Metadata
	if err := yaml.Unmarshal([]byte(in), &md); err == nil {
		t.Fatalf("decode succeeded with %+v, want error for mapping value", md)
	}
}

// TestMetadata_QuotesYAML11Ambiguous checks that values YAML 1.1 readers
// (Helm) would turn into a bool, number or date are written quoted.
func TestMetadata_QuotesYAML11Ambiguous(t *testing.T) {
	md := preview.Metadata{
		PRAuthor:   "Y",
		PRNumber:   "0x1F",
		CreatedAt:  "2026-05-26",
		VCSRefName: "on",
	}
	got := string(encodeValues(t, md))
	for _, line := range []string{
		`pr-author: "Y"`,
		`pr-number: "0x1F"`,
		`created-at: "2026-05-26"`,
		`vcs.ref.name: "on"`,
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("missing quoted line %q in:\n%s", line, got)
		}
	}
}
