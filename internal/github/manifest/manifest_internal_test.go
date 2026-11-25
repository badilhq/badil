package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestDefaultAppName(t *testing.T) {
	t.Parallel()

	re := regexp.MustCompile(`^badil-dev-[0-9a-f]{6}$`)
	name := DefaultAppName()
	if !re.MatchString(name) {
		t.Fatalf("DefaultAppName() = %q, want match %s", name, re)
	}
}

func TestPrintPlanListsEvents(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	printPlan(&out, Options{AppName: "badil-dev"}, "https://d.example.com/webhooks/github")
	if want := "pull_request, push"; !strings.Contains(out.String(), want) {
		t.Errorf("plan = %q, want it to contain %q", out.String(), want)
	}
}

// TestPrintPlanGitHubHost checks the plan's browser step names the App
// creation page on the host of GITHUB_URL.
func TestPrintPlanGitHubHost(t *testing.T) {
	t.Parallel()

	cases := []struct{ githubURL, want string }{
		{"https://github.com", "  2. Open a browser tab to github.com/settings/apps/new and submit the manifest\n"},
		{
			"http://gitea.local:3000",
			"  2. Open a browser tab to gitea.local:3000/settings/apps/new and submit the manifest\n",
		},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		printPlan(
			&out,
			Options{AppName: "badil-dev", GitHubURL: tc.githubURL},
			"https://d.example.com/webhooks/github",
		)
		if !strings.Contains(out.String(), "\n"+tc.want) {
			t.Errorf("plan with GITHUB_URL %s = %q, want the line %q", tc.githubURL, out.String(), tc.want)
		}
	}
}

// TestBootstrapMuxFormAction checks the manifest form posts to the App
// creation page of GITHUB_URL, for a personal account or an organization.
func TestBootstrapMuxFormAction(t *testing.T) {
	t.Parallel()

	cases := []struct{ githubURL, org, want string }{
		{"https://github.com", "", `action="https://github.com/settings/apps/new?state=s1"`},
		{"https://github.com", "acme", `action="https://github.com/organizations/acme/settings/apps/new?state=s1"`},
		{"http://gitea.local:3000", "", `action="http://gitea.local:3000/settings/apps/new?state=s1"`},
		{
			"http://gitea.local:3000", "acme",
			`action="http://gitea.local:3000/organizations/acme/settings/apps/new?state=s1"`,
		},
	}
	for _, tc := range cases {
		mux := bootstrapMux("{}", "s1", tc.org, tc.githubURL, make(chan string, 1), make(chan error, 1))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/start", nil))
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("start page with GITHUB_URL %s, org %q = %q, want it to contain %s",
				tc.githubURL, tc.org, rec.Body.String(), tc.want)
		}
	}
}

func TestRenderEnvGitopsRepoURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		value, want string
	}{
		{"https://github.com/acme/gitops.git", "export GITOPS_REPO_URL=https://github.com/acme/gitops.git\n"},
		{
			"",
			"export GITOPS_REPO_URL= # set this — the clone URL of your gitops repository, " +
				"e.g. https://github.com/<owner>/<repo>.git\n",
		},
	}
	for _, tc := range cases {
		env := renderEnv(envValues{AppID: 1, PEMPath: "dev.private-key.pem", GitOpsRepoURL: tc.value})
		if !strings.Contains(env, "\n"+tc.want) {
			t.Errorf("renderEnv(%q) = %q, want the line %q", tc.value, env, tc.want)
		}
	}
}

// TestRenderEnvGitopsRepoToken checks the .env holds the GITOPS_REPO_TOKEN
// placeholder line: bootstrap cannot know the token.
func TestRenderEnvGitopsRepoToken(t *testing.T) {
	t.Parallel()

	env := renderEnv(envValues{AppID: 1, PEMPath: "dev.private-key.pem"})
	want := "\nexport GITOPS_REPO_TOKEN= # set this — a token with Contents write on the gitops repo\n"
	if !strings.Contains(env, want) {
		t.Errorf("renderEnv = %q, want the line %q", env, want)
	}
}

// TestAppPermissions checks the manifest requests Contents read, not write:
// badil writes to the gitops repo with GITOPS_REPO_TOKEN.
func TestAppPermissions(t *testing.T) {
	t.Parallel()

	want := map[string]string{"contents": "read", "pull_requests": "write", "metadata": "read"}
	if got := appPermissions(); !maps.Equal(got, want) {
		t.Errorf("appPermissions() = %v, want %v", got, want)
	}
	var out bytes.Buffer
	printPlan(&out, Options{AppName: "badil-dev"}, "https://d.example.com/webhooks/github")
	if line := "permissions: contents:read, pull_requests:write, metadata:read\n"; !strings.Contains(
		out.String(),
		line,
	) {
		t.Errorf("plan = %q, want the line %q", out.String(), line)
	}
}

// TestPrintNextSteps checks bootstrap's next steps tell the user to set
// GITOPS_REPO_TOKEN in the .env file.
func TestPrintNextSteps(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	printNextSteps(&out, "https://github.com/apps/badil-dev/installations/new", ".env")
	want := "\nset GITOPS_REPO_TOKEN in .env to a token with Contents write on the gitops repo,\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("next steps = %q, want the line %q", out.String(), want)
	}
}

func TestResolveWebhookURL(t *testing.T) {
	t.Parallel()

	errNgrok := errors.New("ngrok down")
	const tunnel = "https://abc.ngrok-free.app"

	tests := []resolveCase{
		{name: "bare host", flag: "https://d.example.com", want: "https://d.example.com/webhooks/github"},
		{name: "trailing slash", flag: "https://d.example.com/", want: "https://d.example.com/webhooks/github"},
		{name: "double slash", flag: "https://d.example.com//", want: "https://d.example.com/webhooks/github"},
		{
			name: "full endpoint",
			flag: "https://d.example.com/webhooks/github",
			want: "https://d.example.com/webhooks/github",
		},
		{
			name: "full endpoint trailing slash",
			flag: "https://d.example.com/webhooks/github/",
			want: "https://d.example.com/webhooks/github",
		},
		{
			name: "base path",
			flag: "https://x.example.com/badil",
			want: "https://x.example.com/badil/webhooks/github",
		},
		{
			name: "encoded segment",
			flag: "https://x.example.com/a%2Fb",
			want: "https://x.example.com/a%2Fb/webhooks/github",
		},
		{name: "uppercase scheme", flag: "HTTPS://d.example.com", want: "https://d.example.com/webhooks/github"},
		{name: "port", flag: "https://d.example.com:8443", want: "https://d.example.com:8443/webhooks/github"},
		{name: "ipv6", flag: "https://[::1]", want: "https://[::1]/webhooks/github"},
		{
			name: "endpoint with suffix is a base",
			flag: "https://d.example.com/webhooks/github/x",
			want: "https://d.example.com/webhooks/github/x/webhooks/github",
		},
		{name: "ngrok tunnel", flag: "", want: "https://abc.ngrok-free.app/webhooks/github"},
		{name: "ngrok error", flag: "", ngrokErr: errNgrok, wantErr: errNgrok},
		{name: "http", flag: "http://d.example.com"},
		{name: "no scheme", flag: "d.example.com"},
		{name: "opaque", flag: "https:d.example.com"},
		{name: "empty host", flag: "https://"},
		{name: "port only", flag: "https://:443"},
		{name: "empty port", flag: "https://d.example.com:"},
		{name: "credentials", flag: "https://u:p@d.example.com"},
		{name: "empty userinfo", flag: "https://@d.example.com"},
		{name: "query", flag: "https://d.example.com?x=1"},
		{name: "empty query", flag: "https://d.example.com?"},
		{name: "fragment", flag: "https://d.example.com#f"},
		{name: "empty fragment", flag: "https://d.example.com#"},
		{name: "leading space", flag: " https://d.example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			ngrok := func() (string, error) {
				calls++
				if tc.ngrokErr != nil {
					return "", tc.ngrokErr
				}
				return tunnel, nil
			}
			got, err := resolveWebhookURL(tc.flag, ngrok)

			wantCalls := 0
			if tc.flag == "" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("ngrok called %d times, want %d", calls, wantCalls)
			}
			assertResolve(t, tc, got, err)
		})
	}
}

// resolveCase is one resolveWebhookURL input. An empty want with a nil
// wantErr means the flag must be rejected with the validation error.
type resolveCase struct {
	name     string
	flag     string
	ngrokErr error
	want     string
	wantErr  error
}

func assertResolve(t *testing.T, tc resolveCase, got string, err error) {
	t.Helper()

	switch {
	case tc.wantErr != nil:
		if err != tc.wantErr { //nolint:errorlint // the ngrok error must be returned unchanged
			t.Fatalf("err = %v, want %v unchanged", err, tc.wantErr)
		}
	case tc.want == "":
		want := fmt.Sprintf(
			"--webhook-url must be an absolute https URL without credentials, query or fragment, got %q", tc.flag)
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	case err != nil:
		t.Fatalf("unexpected error: %v", err)
	}
	if got != tc.want {
		t.Fatalf("got %q, want %q", got, tc.want)
	}
}
