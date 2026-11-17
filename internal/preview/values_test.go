package preview_test

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/badilhq/badil/internal/preview"
)

func sampleInputs() preview.DeployInputs {
	return preview.DeployInputs{
		ServiceName:    "backend-1",
		HeadRef:        "feature/add-pricing",
		CommitSha:      "abc1234567890",
		PRAuthor:       "octocat",
		PRURL:          "https://github.com/acme/backend-1/pull/42",
		PRNumber:       "42",
		Timestamp:      "2026-05-26T10:00:00Z",
		WorkflowRunURL: "https://github.com/acme/backend-1/actions/runs/1",
	}
}

func TestBuildPreviewValues(t *testing.T) {
	catalog := []string{"backend-1", "backend-2", "front"}
	in := sampleInputs()
	vals := preview.BuildPreviewValues(in.ServiceName, in.CommitSha, catalog, in)

	if got := len(vals.Services); got != 3 {
		t.Fatalf("len(Services) = %d, want 3", got)
	}

	// Order must follow catalog.
	for i, want := range catalog {
		if vals.Services[i].Name != want {
			t.Errorf("Services[%d].Name = %q, want %q", i, vals.Services[i].Name, want)
		}
	}

	// Only the triggering service is pinned and carries metadata.
	if vals.Services[0].CommitSha != in.CommitSha {
		t.Errorf("Services[0].CommitSha = %q, want %q", vals.Services[0].CommitSha, in.CommitSha)
	}
	if vals.Services[0].Metadata == nil {
		t.Fatal("Services[0].Metadata is nil; want populated")
	}
	if got := vals.Services[0].Metadata.PRAuthor; got != in.PRAuthor {
		t.Errorf("metadata pr-author = %q, want %q", got, in.PRAuthor)
	}
	for i, svc := range vals.Services[1:] {
		if svc.CommitSha != "" {
			t.Errorf("Services[%d].CommitSha = %q, want empty (main-tracking)", i+1, svc.CommitSha)
		}
		if svc.Metadata != nil {
			t.Errorf("Services[%d].Metadata not nil; want nil for main-tracking entries", i+1)
		}
	}
}

func TestUpdatePreviewValues_PreservesCreatedAt(t *testing.T) {
	catalog := []string{"backend-1", "backend-2", "front"}
	in := sampleInputs()
	existing := preview.BuildPreviewValues(in.ServiceName, in.CommitSha, catalog, in)
	originalCreatedAt := existing.Services[0].Metadata.CreatedAt

	in2 := in
	in2.CommitSha = "newsha000"
	in2.Timestamp = "2026-05-26T11:00:00Z"

	updated := preview.UpdatePreviewValues(existing, in2.ServiceName, in2.CommitSha, in2)

	svc := updated.Services[0]
	if svc.CommitSha != in2.CommitSha {
		t.Errorf("CommitSha = %q, want %q", svc.CommitSha, in2.CommitSha)
	}
	if got := svc.Metadata.CreatedAt; got != originalCreatedAt {
		t.Errorf("created-at = %q, want preserved %q", got, originalCreatedAt)
	}
	if got := svc.Metadata.UpdatedAt; got != in2.Timestamp {
		t.Errorf("updated-at = %q, want %q", got, in2.Timestamp)
	}
}

func TestUpdatePreviewValues_AppendsNewService(t *testing.T) {
	catalog := []string{"backend-1", "backend-2", "front"}
	in := sampleInputs()
	existing := preview.BuildPreviewValues(in.ServiceName, in.CommitSha, catalog, in)

	in2 := in
	in2.ServiceName = "extra-svc"
	in2.CommitSha = "xyz0987"
	in2.Timestamp = "2026-05-26T12:00:00Z"

	updated := preview.UpdatePreviewValues(existing, in2.ServiceName, in2.CommitSha, in2)
	if got := len(updated.Services); got != 4 {
		t.Fatalf("len = %d, want 4 after append", got)
	}
	last := updated.Services[3]
	if last.Name != "extra-svc" || last.CommitSha != "xyz0987" {
		t.Errorf("appended entry = %+v, want extra-svc/xyz0987", last)
	}
	if last.Metadata.CreatedAt != in2.Timestamp {
		t.Errorf("new entry created-at = %q, want %q",
			last.Metadata.CreatedAt, in2.Timestamp)
	}
}

func TestUpdatePreviewValues_FreshTimestampsWhenNoExistingMetadata(t *testing.T) {
	// Service exists in the values doc but as a main-tracker (no metadata yet);
	// the update should populate metadata using the new timestamp for both
	// created-at and updated-at since there's nothing to preserve.
	catalog := []string{"backend-1", "backend-2"}
	in := sampleInputs()
	existing := preview.BuildPreviewValues("backend-2", "sha-other", catalog, in)

	in2 := in
	in2.ServiceName = "backend-1"
	in2.CommitSha = "newsha"
	in2.Timestamp = "2026-05-26T13:00:00Z"

	updated := preview.UpdatePreviewValues(existing, in2.ServiceName, in2.CommitSha, in2)
	svc := updated.Services[0]
	if svc.Metadata.CreatedAt != in2.Timestamp {
		t.Errorf("created-at = %q, want fresh %q",
			svc.Metadata.CreatedAt, in2.Timestamp)
	}
}

func TestUpdatePreviewValues_DoesNotMutateInput(t *testing.T) {
	catalog := []string{"backend-1", "backend-2"}
	in := sampleInputs()
	existing := preview.BuildPreviewValues(in.ServiceName, in.CommitSha, catalog, in)
	oldMetadata := existing.Services[0].Metadata
	snapshot := *oldMetadata

	in2 := in
	in2.CommitSha = "newsha000"
	in2.Timestamp = "2026-05-26T11:00:00Z"
	preview.UpdatePreviewValues(existing, in2.ServiceName, in2.CommitSha, in2)
	in3 := in
	in3.ServiceName = "extra-svc"
	preview.UpdatePreviewValues(existing, in3.ServiceName, in3.CommitSha, in3)

	if got := existing.Services[0].CommitSha; got != in.CommitSha {
		t.Errorf("input CommitSha = %q, want %q", got, in.CommitSha)
	}
	if existing.Services[0].Metadata != oldMetadata || !reflect.DeepEqual(*oldMetadata, snapshot) {
		t.Errorf("input metadata changed: %+v, want %+v", existing.Services[0].Metadata, snapshot)
	}
	if got := len(existing.Services); got != 2 {
		t.Errorf("input has %d services, want 2", got)
	}
}

func TestEqualExceptUpdatedAt(t *testing.T) {
	in := sampleInputs()
	services := []string{"backend-1", "front"}
	base := preview.ApplyCatalog(
		preview.BuildPreviewValues(in.ServiceName, in.CommitSha, services, in),
		preview.Catalog{Services: services, Repos: catalogRepos(), Namespace: sampleNamespace()},
	).Values
	base.Services[1] = preview.ServiceEntry{
		Name: "front", RepoURL: "https://github.com/acme/front", Branch: "main",
		CommitSha: "f00", ImageTag: "f00", Chart: valueFilesChart(), Host: "front.example.com",
		Metadata: &preview.Metadata{PRURL: "https://github.com/acme/front/pull/7", UpdatedAt: "2026-05-26T09:00:00Z"},
	}
	clone := func() preview.PreviewValues {
		out := base
		out.Services = slices.Clone(base.Services)
		for i := range out.Services {
			if md := out.Services[i].Metadata; md != nil {
				cp := *md
				out.Services[i].Metadata = &cp
			}
		}
		return out
	}

	tests := []struct {
		name   string
		change func(*preview.PreviewValues)
		want   bool
	}{
		{name: "identical", change: func(*preview.PreviewValues) {}, want: true},
		{
			name:   "triggering updated-at",
			change: func(v *preview.PreviewValues) { v.Services[0].Metadata.UpdatedAt = "2026-05-26T11:00:00Z" },
			want:   true,
		},
		{
			name:   "other service updated-at",
			change: func(v *preview.PreviewValues) { v.Services[1].Metadata.UpdatedAt = "2026-05-26T11:00:00Z" },
		},
		{name: "commit sha", change: func(v *preview.PreviewValues) { v.Services[0].CommitSha = "new" }},
		{name: "host", change: func(v *preview.PreviewValues) { v.Services[1].Host = "" }},
		{name: "pr author", change: func(v *preview.PreviewValues) { v.Services[0].Metadata.PRAuthor = "x" }},
		{name: "head ref", change: func(v *preview.PreviewValues) { v.Services[0].Metadata.VCSRefName = "x" }},
		{name: "created-at", change: func(v *preview.PreviewValues) { v.Services[0].Metadata.CreatedAt = "x" }},
		{name: "api version", change: func(v *preview.PreviewValues) { v.APIVersion = "" }},
		{name: "repo url", change: func(v *preview.PreviewValues) { v.Services[1].RepoURL = "https://example.com/x" }},
		{name: "branch", change: func(v *preview.PreviewValues) { v.Services[0].Branch = "develop" }},
		{
			name:   "pipeline run url",
			change: func(v *preview.PreviewValues) { v.Services[0].Metadata.PipelineRunURL = "x" },
		},
		{name: "metadata removed", change: func(v *preview.PreviewValues) { v.Services[0].Metadata = nil }},
		{
			name: "entry appended",
			change: func(v *preview.PreviewValues) {
				v.Services = append(v.Services, preview.ServiceEntry{Name: "extra"})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := clone()
			tt.change(&changed)
			if got := preview.EqualExceptUpdatedAt(base, changed, "backend-1"); got != tt.want {
				t.Errorf("EqualExceptUpdatedAt = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEqual(t *testing.T) {
	base := func() preview.PreviewValues {
		return preview.PreviewValues{APIVersion: preview.APIVersion, Services: []preview.ServiceEntry{
			{
				Name: "backend-1", RepoURL: "https://github.com/acme/backend-1.git", Branch: "main",
				CommitSha: "abc", ImageTag: "abc", Chart: valueFilesChart(),
				Metadata: &preview.Metadata{UpdatedAt: "2026-05-26T09:00:00Z"},
			},
			{
				Name: "front", RepoURL: "https://github.com/acme/front", Branch: "main",
				ImageTag: "${ARGOCD_APP_REVISION}", Chart: defaultChart(), Host: "front.example.com",
			},
		}, Namespace: sampleNamespace()}
	}

	tests := []struct {
		name   string
		change func(*preview.PreviewValues)
		want   bool
	}{
		{name: "identical", change: func(*preview.PreviewValues) {}, want: true},
		{name: "updated-at", change: func(v *preview.PreviewValues) { v.Services[0].Metadata.UpdatedAt = "x" }},
		{name: "api version", change: func(v *preview.PreviewValues) { v.APIVersion = "" }},
		{name: "host", change: func(v *preview.PreviewValues) { v.Services[1].Host = "" }},
		{name: "branch", change: func(v *preview.PreviewValues) { v.Services[1].Branch = "develop" }},
		{name: "repo url", change: func(v *preview.PreviewValues) { v.Services[1].RepoURL = "x" }},
		{name: "commit sha", change: func(v *preview.PreviewValues) { v.Services[0].CommitSha = "x" }},
		{name: "image tag", change: func(v *preview.PreviewValues) { v.Services[1].ImageTag = "x" }},
		{name: "chart path", change: func(v *preview.PreviewValues) { v.Services[1].Chart.Path = "x" }},
		{
			name:   "chart image tag param",
			change: func(v *preview.PreviewValues) { v.Services[1].Chart.ImageTagParam = "x" },
		},
		{name: "chart host param", change: func(v *preview.PreviewValues) { v.Services[1].Chart.HostParam = "x" }},
		{name: "chart value files", change: func(v *preview.PreviewValues) { v.Services[0].Chart.ValueFiles = nil }},
		{
			name:   "chart value files empty",
			change: func(v *preview.PreviewValues) { v.Services[1].Chart.ValueFiles = []string{} },
			want:   true,
		},
		{name: "namespace removed", change: func(v *preview.PreviewValues) { v.Namespace = nil }},
		{name: "namespace annotation", change: func(v *preview.PreviewValues) { v.Namespace.Annotations = nil }},
		{
			name:   "namespace copy",
			change: func(v *preview.PreviewValues) { v.Namespace = sampleNamespace() },
			want:   true,
		},
		{name: "metadata removed", change: func(v *preview.PreviewValues) { v.Services[0].Metadata = nil }},
		{
			name:   "order",
			change: func(v *preview.PreviewValues) { v.Services[0], v.Services[1] = v.Services[1], v.Services[0] },
		},
		{name: "entry removed", change: func(v *preview.PreviewValues) { v.Services = v.Services[:1] }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base()
			tt.change(&changed)
			if got := preview.Equal(base(), changed); got != tt.want {
				t.Errorf("Equal = %v, want %v", got, tt.want)
			}
		})
	}
}

// pinned returns an entry pinned to sha by the PR at prURL.
func pinned(name, sha, prURL string) preview.ServiceEntry {
	return preview.ServiceEntry{Name: name, CommitSha: sha, Metadata: &preview.Metadata{PRURL: prURL}}
}

func TestUnpinPR(t *testing.T) {
	const (
		pr42 = "https://github.com/acme/backend-1/pull/42"
		pr7  = "https://github.com/acme/backend-2/pull/7"
	)
	cases := []struct {
		name        string
		services    []preview.ServiceEntry
		owner, repo string
		number      int
		wantRemoved []string
		wantPinned  bool
	}{
		{
			name:       "same number in another repo",
			services:   []preview.ServiceEntry{pinned("backend-1", "a", pr42), {Name: "front"}},
			owner:      "acme",
			repo:       "backend-2",
			number:     42,
			wantPinned: true,
		},
		{
			name:       "another number in the same repo",
			services:   []preview.ServiceEntry{pinned("backend-1", "a", pr42), {Name: "front"}},
			owner:      "acme",
			repo:       "backend-1",
			number:     43,
			wantPinned: true,
		},
		{
			name:        "owner and repo differ only by case",
			services:    []preview.ServiceEntry{pinned("backend-1", "a", pr42), {Name: "front"}},
			owner:       "ACME",
			repo:        "Backend-1",
			number:      42,
			wantRemoved: []string{"backend-1"},
		},
		{
			name: "no metadata",
			services: []preview.ServiceEntry{
				{Name: "backend-1", CommitSha: "a"},
				{Name: "front"},
			},
			owner:      "acme",
			repo:       "backend-1",
			number:     42,
			wantPinned: true,
		},
		{
			name:       "unparsable pr-url",
			services:   []preview.ServiceEntry{pinned("backend-1", "a", "not-a-url/pull/42")},
			owner:      "acme",
			repo:       "backend-1",
			number:     42,
			wantPinned: true,
		},
		{
			name: "one PR pinning two services",
			services: []preview.ServiceEntry{
				pinned("api", "a", pr42),
				{Name: "front"},
				pinned("worker", "a", pr42),
			},
			owner:       "acme",
			repo:        "backend-1",
			number:      42,
			wantRemoved: []string{"api", "worker"},
		},
		{
			name: "two PRs, one closes",
			services: []preview.ServiceEntry{
				pinned("backend-1", "a", pr42),
				pinned("backend-2", "b", pr7),
				{Name: "front"},
			},
			owner:       "acme",
			repo:        "backend-1",
			number:      42,
			wantRemoved: []string{"backend-1"},
			wantPinned:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := preview.PreviewValues{Services: tc.services}
			got, removed, stillPinned := preview.UnpinPR(in, tc.owner, tc.repo, tc.number)
			if !slices.Equal(removed, tc.wantRemoved) {
				t.Errorf("removed = %v, want %v", removed, tc.wantRemoved)
			}
			if stillPinned != tc.wantPinned {
				t.Errorf("pinned = %v, want %v", stillPinned, tc.wantPinned)
			}
			assertUnpinned(t, got, tc.services, tc.wantRemoved)
		})
	}
}

// assertUnpinned checks that got keeps every entry of orig in order and that
// the removed services are bare main-tracking entries.
func assertUnpinned(t *testing.T, got preview.PreviewValues, orig []preview.ServiceEntry, removed []string) {
	t.Helper()
	if len(got.Services) != len(orig) {
		t.Fatalf("len(Services) = %d, want %d (entries are kept)", len(got.Services), len(orig))
	}
	for i, svc := range got.Services {
		if svc.Name != orig[i].Name {
			t.Errorf("Services[%d].Name = %q, want %q", i, svc.Name, orig[i].Name)
		}
		if slices.Contains(removed, svc.Name) && (svc.CommitSha != "" || svc.Metadata != nil) {
			t.Errorf("Services[%d] = %+v, want bare main-tracking entry", i, svc)
		}
	}
}

func TestUnpinPR_DoesNotMutateInput(t *testing.T) {
	const prURL = "https://github.com/acme/backend-1/pull/42"
	in := preview.PreviewValues{Services: []preview.ServiceEntry{
		pinned("backend-1", "abc", prURL),
		{Name: "front"},
	}}
	got, removed, _ := preview.UnpinPR(in, "acme", "backend-1", 42)
	if len(removed) != 1 || got.Services[0].CommitSha != "" {
		t.Fatalf("UnpinPR did not unpin: removed = %v, got = %+v", removed, got)
	}
	if in.Services[0].CommitSha != "abc" || in.Services[0].Metadata == nil || in.Services[0].Metadata.PRURL != prURL {
		t.Errorf("input mutated: %+v", in.Services[0])
	}
}

func TestLastDeploy(t *testing.T) {
	pinned := func(name, updatedAt string) preview.ServiceEntry {
		return preview.ServiceEntry{Name: name, CommitSha: "abc", Metadata: &preview.Metadata{UpdatedAt: updatedAt}}
	}
	cases := []struct {
		name   string
		vals   preview.PreviewValues
		want   string
		wantOK bool
	}{
		{
			name: "max over pins",
			vals: preview.PreviewValues{Services: []preview.ServiceEntry{
				pinned("backend-1", "2026-05-26T10:00:00Z"),
				pinned("backend-2", "2026-05-27T09:00:00Z"),
				pinned("front", "2026-05-25T12:00:00Z"),
			}},
			want:   "2026-05-27T09:00:00Z",
			wantOK: true,
		},
		{
			name: "pinless entries ignored",
			vals: preview.PreviewValues{Services: []preview.ServiceEntry{
				pinned("backend-1", "2026-05-26T10:00:00Z"),
				{Name: "backend-2", Metadata: &preview.Metadata{UpdatedAt: "2026-06-01T00:00:00Z"}},
				{Name: "front"},
			}},
			want:   "2026-05-26T10:00:00Z",
			wantOK: true,
		},
		{
			name: "unparseable updated-at ignored",
			vals: preview.PreviewValues{Services: []preview.ServiceEntry{
				pinned("backend-1", "not-a-time"),
				pinned("backend-2", "2026-05-26T10:00:00Z"),
				{Name: "front", CommitSha: "def"},
			}},
			want:   "2026-05-26T10:00:00Z",
			wantOK: true,
		},
		{
			name: "none parseable",
			vals: preview.PreviewValues{Services: []preview.ServiceEntry{
				pinned("backend-1", "2026-05-26"),
				{Name: "backend-2", CommitSha: "def"},
				{Name: "front"},
			}},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := preview.LastDeploy(tc.vals)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if s := got.Format(time.RFC3339); s != tc.want {
				t.Errorf("LastDeploy = %s, want %s", s, tc.want)
			}
		})
	}
}

func hostURLs() preview.URLConfig {
	return preview.URLConfig{
		Domain: "127.0.0.1.nip.io",
		Expose: map[string]string{"front": "/", "backend-1": "/docs"},
	}
}

func TestApplyHosts(t *testing.T) {
	in := preview.PreviewValues{Services: []preview.ServiceEntry{
		{Name: "backend-1", CommitSha: "abc"},
		{Name: "backend-2", Host: "stale.example.com"},
		{Name: "front"},
	}}

	got := preview.ApplyHosts(in, "feature-pricing", hostURLs())

	want := []string{
		"backend-1-feature-pricing.127.0.0.1.nip.io",
		"",
		"front-feature-pricing.127.0.0.1.nip.io",
	}
	if len(got.Services) != len(want) {
		t.Fatalf("len(Services) = %d, want %d", len(got.Services), len(want))
	}
	for i, svc := range got.Services {
		if svc.Host != want[i] {
			t.Errorf("Services[%d].Host = %q, want %q", i, svc.Host, want[i])
		}
	}
	if got.Services[0].CommitSha != "abc" {
		t.Errorf("Services[0].CommitSha = %q, want abc (kept)", got.Services[0].CommitSha)
	}
}

func TestApplyHosts_FeatureOff(t *testing.T) {
	in := preview.PreviewValues{Services: []preview.ServiceEntry{
		{Name: "backend-1", Host: "backend-1-feature-pricing.127.0.0.1.nip.io"},
		{Name: "front"},
	}}
	urls := hostURLs()
	urls.Domain = ""

	got := preview.ApplyHosts(in, "feature-pricing", urls)

	for i, svc := range got.Services {
		if svc.Host != "" {
			t.Errorf("Services[%d].Host = %q, want empty with the feature off", i, svc.Host)
		}
	}
}

func TestApplyHosts_DoesNotMutateInput(t *testing.T) {
	in := preview.PreviewValues{Services: []preview.ServiceEntry{
		{Name: "backend-2", Host: "stale.example.com"},
		{Name: "front"},
	}}

	got := preview.ApplyHosts(in, "feature-pricing", hostURLs())

	if got.Services[1].Host == "" {
		t.Fatalf("ApplyHosts did not set the front host: %+v", got)
	}
	if in.Services[0].Host != "stale.example.com" || in.Services[1].Host != "" {
		t.Errorf("input mutated: %+v", in.Services)
	}
}

func TestUnpinPR_KeepsHost(t *testing.T) {
	const host = "backend-1-feature-pricing.127.0.0.1.nip.io"
	entry := pinned("backend-1", "abc", "https://github.com/acme/backend-1/pull/42")
	entry.Host = host
	in := preview.PreviewValues{Services: []preview.ServiceEntry{entry}}

	got, removed, _ := preview.UnpinPR(in, "acme", "backend-1", 42)

	if len(removed) != 1 {
		t.Fatalf("removed = %v, want [backend-1]", removed)
	}
	if got.Services[0].Host != host {
		t.Errorf("Host = %q, want %q (kept by unpin)", got.Services[0].Host, host)
	}
}

func TestApplyHosts_YAML(t *testing.T) {
	in := preview.PreviewValues{APIVersion: preview.APIVersion, Services: []preview.ServiceEntry{
		{
			Name: "backend-1", RepoURL: "https://github.com/acme/backend-1.git", Branch: "main",
			CommitSha: "abc", ImageTag: "abc", Chart: defaultChart(), Metadata: &preview.Metadata{PRAuthor: "octocat"},
		},
		{
			Name: "backend-2", RepoURL: "https://github.com/acme/backend-2.git", Branch: "develop",
			ImageTag: "${ARGOCD_APP_REVISION}", Chart: valueFilesChart(),
		},
	}, Namespace: sampleNamespace(), GitopsRepoURL: "https://github.com/acme/gitops.git"}
	in.Dependencies = sampleDependencies()
	urls := hostURLs()
	urls.Expose["mailpit"] = "/"
	vals := preview.ApplyHosts(in, "feature-pricing", urls)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(vals); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_ = enc.Close()

	want := "apiVersion: badil.dev/v1beta1\n" +
		"gitopsRepoURL: https://github.com/acme/gitops.git\n" +
		"namespace:\n" +
		"  labels:\n" +
		"    team: platform\n" +
		"  annotations:\n" +
		"    owner: platform\n" +
		"services:\n" +
		"  - name: backend-1\n" +
		"    repoURL: https://github.com/acme/backend-1.git\n" +
		"    branch: main\n" +
		"    commitSha: abc\n" +
		"    imageTag: abc\n" +
		"    chart:\n" +
		"      path: k8s/\n" +
		"      imageTagParam: app.image.tag\n" +
		"      hostParam: app.ingress.host\n" +
		"    host: backend-1-feature-pricing.127.0.0.1.nip.io\n" +
		"    metadata:\n" +
		"      pr-author: octocat\n" +
		"      pr-url: \"\"\n" +
		"      pr-number: \"\"\n" +
		"      created-at: \"\"\n" +
		"      updated-at: \"\"\n" +
		"      vcs.ref.name: \"\"\n" +
		"      cicd.pipeline.run.url: \"\"\n" +
		"  - name: backend-2\n" +
		"    repoURL: https://github.com/acme/backend-2.git\n" +
		"    branch: develop\n" +
		"    imageTag: ${ARGOCD_APP_REVISION}\n" +
		"    chart:\n" +
		"      path: k8s/\n" +
		"      imageTagParam: app.image.tag\n" +
		"      hostParam: app.ingress.host\n" +
		"      valueFiles:\n" +
		"        - values-preview.yaml\n" +
		"dependencies:\n" +
		"  - name: postgres\n" +
		"    chart:\n" +
		"      repoURL: oci://registry-1.docker.io/bitnamicharts/postgresql\n" +
		"      version: 16.4.2\n" +
		"    values:\n" +
		"      auth:\n" +
		"        password: preview\n" +
		"    valueFiles:\n" +
		"      - deps/postgres.yaml\n" +
		"  - name: mailpit\n" +
		"    chart:\n" +
		"      repoURL: https://jouve.github.io/charts\n" +
		"      name: mailpit\n" +
		"      version: 0.20.0\n" +
		"    hostParam: ingress.hostname\n" +
		"    host: mailpit-feature-pricing.127.0.0.1.nip.io\n"
	if got := buf.String(); got != want {
		t.Errorf("yaml mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

// sampleDependencies returns postgres, an OCI chart with values, value files
// and a hostParam, and mailpit, a chart of a Helm repository with a
// hostParam, as parsed from badil.yaml.
func sampleDependencies() []preview.DependencyEntry {
	return []preview.DependencyEntry{
		{
			Name: "postgres",
			Chart: preview.DependencyChart{
				RepoURL: "oci://registry-1.docker.io/bitnamicharts/postgresql", Version: "16.4.2",
			},
			Values:     map[string]any{"auth": map[string]any{"password": "preview"}},
			ValueFiles: []string{"deps/postgres.yaml"},
			HostParam:  "postgresql.host",
		},
		{
			Name: "mailpit",
			Chart: preview.DependencyChart{
				RepoURL: "https://jouve.github.io/charts", Name: "mailpit", Version: "0.20.0",
			},
			HostParam: "ingress.hostname",
		},
	}
}

// catalogServices is the catalog order of catalogRepos.
func catalogServices() []string {
	return []string{"backend-1", "backend-2", "front"}
}

// catalogRepos is a catalog's service repos for backend-1, backend-2 and front.
func catalogRepos() map[string]string {
	return map[string]string{
		"backend-1": "https://github.com/acme/backend-1.git",
		"backend-2": "https://github.com/acme/backend-2.git",
		"front":     "https://github.com/acme/front",
	}
}

func TestBuildPreviewValues_SetsAPIVersion(t *testing.T) {
	in := sampleInputs()
	vals := preview.BuildPreviewValues(in.ServiceName, in.CommitSha, []string{"backend-1"}, in)
	if vals.APIVersion != preview.APIVersion {
		t.Errorf("APIVersion = %q, want %q", vals.APIVersion, preview.APIVersion)
	}
}

func TestUpdatePreviewValues_KeepsAPIVersion(t *testing.T) {
	in := sampleInputs()
	existing := preview.PreviewValues{APIVersion: preview.APIVersion, Services: []preview.ServiceEntry{
		{Name: "backend-1", RepoURL: "https://github.com/acme/backend-1.git", Branch: "main"},
	}}
	for _, svc := range []string{"backend-1", "extra-svc"} {
		got := preview.UpdatePreviewValues(existing, svc, in.CommitSha, in)
		if got.APIVersion != preview.APIVersion {
			t.Errorf("%s: APIVersion = %q, want %q", svc, got.APIVersion, preview.APIVersion)
		}
		if e := got.Services[0]; e.RepoURL != existing.Services[0].RepoURL || e.Branch != "main" {
			t.Errorf("%s: Services[0] = %+v, want repoURL and branch kept", svc, e)
		}
	}
}

func TestUnpinPR_KeepsAPIVersionAndFacts(t *testing.T) {
	const pr42 = "https://github.com/acme/backend-1/pull/42"
	b1 := pinned("backend-1", "a", pr42)
	b1.RepoURL, b1.Branch = "https://github.com/acme/backend-1.git", "main"
	b2 := pinned("backend-2", "b", "https://github.com/acme/backend-2/pull/7")
	b2.RepoURL, b2.Branch = "https://github.com/acme/backend-2.git", "develop"
	in := preview.PreviewValues{APIVersion: preview.APIVersion, Services: []preview.ServiceEntry{b1, b2}}

	got, removed, stillPinned := preview.UnpinPR(in, "acme", "backend-1", 42)

	if !slices.Equal(removed, []string{"backend-1"}) || !stillPinned {
		t.Fatalf("removed = %v, pinned = %v, want [backend-1], true", removed, stillPinned)
	}
	if got.APIVersion != preview.APIVersion {
		t.Errorf("APIVersion = %q, want %q", got.APIVersion, preview.APIVersion)
	}
	want := preview.ServiceEntry{Name: "backend-1", RepoURL: b1.RepoURL, Branch: "main"}
	if !reflect.DeepEqual(got.Services[0], want) {
		t.Errorf("Services[0] = %+v, want %+v", got.Services[0], want)
	}
	if got.Services[1].RepoURL != b2.RepoURL || got.Services[1].Branch != "develop" ||
		got.Services[1].CommitSha != "b" {
		t.Errorf("Services[1] = %+v, want kept", got.Services[1])
	}
}

func TestApplyHosts_KeepsAPIVersion(t *testing.T) {
	in := preview.PreviewValues{APIVersion: preview.APIVersion, Services: []preview.ServiceEntry{{Name: "front"}}}
	got := preview.ApplyHosts(in, "feature-pricing", hostURLs())
	if got.APIVersion != preview.APIVersion {
		t.Errorf("APIVersion = %q, want %q", got.APIVersion, preview.APIVersion)
	}
}

// defaultChart is the chart settings of a service the catalog sets none for.
func defaultChart() preview.ChartSettings {
	return preview.ChartSettings{Path: "k8s/", ImageTagParam: "app.image.tag", HostParam: "app.ingress.host"}
}

// valueFilesChart is defaultChart with one values file.
func valueFilesChart() preview.ChartSettings {
	c := defaultChart()
	c.ValueFiles = []string{"values-preview.yaml"}
	return c
}

// sampleNamespace is a namespace block with one label and one annotation.
func sampleNamespace() *preview.NamespaceMetadata {
	return &preview.NamespaceMetadata{
		Labels:      map[string]string{"team": "platform"},
		Annotations: map[string]string{"owner": "platform"},
	}
}

func TestEqual_NamespaceNilAndEmpty(t *testing.T) {
	for name, ns := range map[string]*preview.NamespaceMetadata{
		"empty":      {},
		"empty maps": {Labels: map[string]string{}, Annotations: map[string]string{}},
	} {
		t.Run(name, func(t *testing.T) {
			a := preview.PreviewValues{APIVersion: preview.APIVersion}
			b := preview.PreviewValues{APIVersion: preview.APIVersion, Namespace: ns}
			if !preview.Equal(a, b) || !preview.Equal(b, a) {
				t.Errorf("Equal(nil, %+v) = false, want true", ns)
			}
		})
	}
}

func TestExpandImageTag(t *testing.T) {
	const sha = "f913b6cb1a2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f"
	tests := []struct {
		format, commitSha, want string
	}{
		{"{sha}", sha, sha},
		{"{sha7}", sha, "f913b6c"},
		{"{sha8}", sha, "f913b6cb"},
		{"sha-{sha7}", sha, "sha-f913b6c"},
		{"{sha8}-amd64", sha, "f913b6cb-amd64"},
		{"{sha7}", "abc12", "abc12"},
		{"{sha}", "", "${ARGOCD_APP_REVISION}"},
		{"{sha7}", "", "${ARGOCD_APP_REVISION_SHORT}"},
		{"{sha8}", "", "${ARGOCD_APP_REVISION_SHORT_8}"},
	}
	for _, tt := range tests {
		t.Run(tt.format+"/"+tt.commitSha, func(t *testing.T) {
			if got := preview.ExpandImageTag(tt.format, tt.commitSha); got != tt.want {
				t.Errorf("ExpandImageTag(%q, %q) = %q, want %q", tt.format, tt.commitSha, got, tt.want)
			}
		})
	}
}

func TestValidImageTagFormat(t *testing.T) {
	for format, want := range map[string]bool{
		"{sha}":                           true,
		"sha-{sha7}":                      true,
		"{sha8}-amd64":                    true,
		strings.Repeat("a", 88) + "{sha}": true,
		strings.Repeat("a", 89) + "{sha}": false,
		"":                                false,
		"latest":                          false,
		"{sha}{sha}":                      false,
		"{SHA}":                           false,
		"-{sha}":                          false,
		"a:{sha}":                         false,
		"{sha9}":                          false,
		"a/{sha}":                         false,
		".{sha}":                          false,
	} {
		if got := preview.ValidImageTagFormat(format); got != want {
			t.Errorf("ValidImageTagFormat(%q) = %v, want %v", format, got, want)
		}
	}
}

func TestApplyCatalog(t *testing.T) {
	md := &preview.Metadata{PRURL: "https://github.com/acme/backend-1/pull/42"}
	goneMD := &preview.Metadata{PRURL: "https://github.com/acme/gone/pull/9", UpdatedAt: "2026-05-26T09:00:00Z"}
	goneChart := preview.ChartSettings{Path: "old/", ImageTagParam: "old.tag", HostParam: "old.host"}
	in := preview.PreviewValues{Services: []preview.ServiceEntry{
		{
			Name: "gone-pinned", RepoURL: "https://github.com/acme/gone-pinned", Branch: "release",
			CommitSha: "x", ImageTag: "old-x", Chart: goneChart, Metadata: goneMD,
		},
		{
			Name: "front", RepoURL: "https://stale.example.com/front", Branch: "stale", Host: "front.example.com",
			ImageTag: "stale", Chart: goneChart,
		},
		{Name: "gone"},
		{Name: "gone-pinned-no-repo", CommitSha: "y", Metadata: md},
		{Name: "backend-1", CommitSha: "abc1234567890", Metadata: md},
	}}

	got := preview.ApplyCatalog(in, preview.Catalog{
		Services:  catalogServices(),
		Repos:     catalogRepos(),
		Branches:  map[string]string{"backend-2": "develop"},
		ImageTag:  "sha-{sha7}",
		ImageTags: map[string]string{"backend-2": "{sha8}-amd64"},
		Charts: map[string]preview.ChartSettings{
			"backend-1": {Path: "deploy/helm", ValueFiles: []string{"values-preview.yaml"}},
			"front":     {ImageTagParam: "image.tag", HostParam: "ingress.hostname"},
		},
		Namespace: sampleNamespace(),
	}).Values

	if got.APIVersion != preview.APIVersion {
		t.Errorf("APIVersion = %q, want %q", got.APIVersion, preview.APIVersion)
	}
	if !reflect.DeepEqual(got.Namespace, sampleNamespace()) {
		t.Errorf("Namespace = %+v, want %+v", got.Namespace, sampleNamespace())
	}
	want := []preview.ServiceEntry{
		{
			Name: "backend-1", RepoURL: "https://github.com/acme/backend-1.git", Branch: "main",
			CommitSha: "abc1234567890", ImageTag: "sha-abc1234", Metadata: md,
			Chart: preview.ChartSettings{
				Path: "deploy/helm", ImageTagParam: "app.image.tag", HostParam: "app.ingress.host",
				ValueFiles: []string{"values-preview.yaml"},
			},
		},
		{
			Name: "backend-2", RepoURL: "https://github.com/acme/backend-2.git", Branch: "develop",
			ImageTag: "${ARGOCD_APP_REVISION_SHORT_8}-amd64", Chart: defaultChart(),
		},
		{
			Name: "front", RepoURL: "https://github.com/acme/front", Branch: "main",
			ImageTag: "sha-${ARGOCD_APP_REVISION_SHORT}", Host: "front.example.com",
			Chart: preview.ChartSettings{Path: "k8s/", ImageTagParam: "image.tag", HostParam: "ingress.hostname"},
		},
		{
			Name: "gone-pinned", RepoURL: "https://github.com/acme/gone-pinned", Branch: "release",
			CommitSha: "x", ImageTag: "old-x", Chart: goneChart, Metadata: goneMD,
		},
	}
	if !reflect.DeepEqual(got.Services, want) {
		t.Errorf("Services = %+v, want %+v", got.Services, want)
	}
	if in.APIVersion != "" || in.Namespace != nil || len(in.Services) != 5 ||
		in.Services[1].RepoURL != "https://stale.example.com/front" || in.Services[1].ImageTag != "stale" ||
		in.Services[0].Name != "gone-pinned" {
		t.Errorf("input mutated: %+v", in)
	}
}

func TestApplyCatalog_Defaults(t *testing.T) {
	in := preview.PreviewValues{Namespace: sampleNamespace(), Services: []preview.ServiceEntry{
		{Name: "backend-1", CommitSha: "abc1234567890"},
	}}

	cat := preview.Catalog{Services: []string{"backend-1", "front"}, Repos: catalogRepos()}
	got := preview.ApplyCatalog(in, cat).Values

	if got.Namespace != nil {
		t.Errorf("Namespace = %+v, want nil when the catalog sets none", got.Namespace)
	}
	want := []preview.ServiceEntry{
		{
			Name: "backend-1", RepoURL: "https://github.com/acme/backend-1.git", Branch: "main",
			CommitSha: "abc1234567890", ImageTag: "abc1234567890", Chart: defaultChart(),
		},
		{
			Name: "front", RepoURL: "https://github.com/acme/front", Branch: "main",
			ImageTag: "${ARGOCD_APP_REVISION}", Chart: defaultChart(),
		},
	}
	if !reflect.DeepEqual(got.Services, want) {
		t.Errorf("Services = %+v, want %+v", got.Services, want)
	}
}

func TestApplyCatalog_AddsMissingServicesInCatalogOrder(t *testing.T) {
	in := preview.PreviewValues{Services: []preview.ServiceEntry{{Name: "front"}}}

	got := preview.ApplyCatalog(in, preview.Catalog{Services: catalogServices(), Repos: catalogRepos()}).Values

	names := make([]string, 0, len(got.Services))
	for _, svc := range got.Services {
		names = append(names, svc.Name)
	}
	if !slices.Equal(names, catalogServices()) {
		t.Fatalf("names = %v, want %v", names, catalogServices())
	}
	want := preview.ServiceEntry{
		Name: "backend-2", RepoURL: "https://github.com/acme/backend-2.git", Branch: "main",
		ImageTag: "${ARGOCD_APP_REVISION}", Chart: defaultChart(),
	}
	if !reflect.DeepEqual(got.Services[1], want) {
		t.Errorf("Services[1] = %+v, want %+v", got.Services[1], want)
	}
	if len(in.Services) != 1 {
		t.Errorf("input mutated: %+v", in)
	}
}

func TestApplyCatalog_KeepsFirstEntryOfDuplicates(t *testing.T) {
	first := preview.ServiceEntry{
		Name:      "gone",
		RepoURL:   "https://github.com/acme/gone.git",
		Branch:    "main",
		CommitSha: "aaa",
	}
	in := preview.PreviewValues{Services: []preview.ServiceEntry{
		{Name: "front", CommitSha: "f1"},
		{Name: "front", CommitSha: "f2"},
		first,
		{Name: "gone", RepoURL: "https://github.com/acme/gone.git", Branch: "main", CommitSha: "bbb"},
	}}

	got := preview.ApplyCatalog(in, preview.Catalog{
		Services: []string{"front"},
		Repos:    map[string]string{"front": "https://github.com/acme/front.git"},
	}).Values

	want := []preview.ServiceEntry{
		{
			Name: "front", RepoURL: "https://github.com/acme/front.git", Branch: "main", CommitSha: "f1",
			ImageTag: "f1", Chart: defaultChart(),
		},
		first,
	}
	if !reflect.DeepEqual(got.Services, want) {
		t.Errorf("Services = %+v, want %+v", got.Services, want)
	}
}

func TestApplyCatalog_KeepsAPIVersion(t *testing.T) {
	in := preview.PreviewValues{APIVersion: preview.APIVersion, Services: []preview.ServiceEntry{{Name: "front"}}}
	got := preview.ApplyCatalog(in, preview.Catalog{Services: catalogServices(), Repos: catalogRepos()}).Values
	if got.APIVersion != preview.APIVersion {
		t.Errorf("APIVersion = %q, want %q", got.APIVersion, preview.APIVersion)
	}
}

func TestUnpinPRServices(t *testing.T) {
	const (
		pr42 = "https://github.com/acme/platform/pull/42"
		pr7  = "https://github.com/acme/platform/pull/7"
		dep  = "https://github.com/acme/other/pull/42"
	)
	in := preview.PreviewValues{Services: []preview.ServiceEntry{
		pinned("api", "a", pr42),
		pinned("web", "b", pr42),
		pinned("worker", "c", pr7),
		pinned("billing", "d", dep),
		{Name: "front"},
	}}
	cases := []struct {
		name        string
		services    []string
		wantRemoved []string
		wantPinned  bool
	}{
		{name: "one service of the PR", services: []string{"web"}, wantRemoved: []string{"web"}, wantPinned: true},
		{
			name:        "file order",
			services:    []string{"web", "api"},
			wantRemoved: []string{"api", "web"},
			wantPinned:  true,
		},
		{name: "another PR's entry", services: []string{"worker"}, wantPinned: true},
		{name: "unpinned entry", services: []string{"front"}, wantPinned: true},
		{name: "no service", wantPinned: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, removed, stillPinned := preview.UnpinPRServices(in, "acme", "platform", 42, tc.services)
			if !reflect.DeepEqual(removed, tc.wantRemoved) {
				t.Errorf("removed = %v, want %v", removed, tc.wantRemoved)
			}
			if stillPinned != tc.wantPinned {
				t.Errorf("pinned = %v, want %v", stillPinned, tc.wantPinned)
			}
			want := slices.Clone(in.Services)
			for i := range want {
				if slices.Contains(tc.wantRemoved, want[i].Name) {
					want[i] = preview.ServiceEntry{Name: want[i].Name}
				}
			}
			if !reflect.DeepEqual(got.Services, want) {
				t.Errorf("services = %+v, want %+v", got.Services, want)
			}
		})
	}
}
