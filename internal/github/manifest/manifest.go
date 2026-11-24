// Package manifest creates a GitHub App through the manifest flow and writes its
// credentials to a PEM file and a .env file.
package manifest

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/badilhq/badil/internal/github"
)

// Manifest flow constants. Values reflect dev defaults; nothing here is a
// secret. The listener picks an ephemeral port at startup and threads it
// through redirectURL; ngrokAPI is the well-known local URL of the ngrok
// inspector.
const (
	manifestServerHost        = "127.0.0.1"
	ngrokAPI                  = "http://127.0.0.1:4040/api/tunnels"
	webhookPath               = "/webhooks/github"
	stateBytes                = 16 // random bytes of the manifest flow's state
	deployTokenBytes          = 32 // random bytes of BADIL_DEPLOY_TOKEN
	callbackReadHeaderTimeout = 5 * time.Second
	ngrokTimeout              = 2 * time.Second
)

// Timeout bounds the whole manifest flow, from the plan to the code exchange.
const Timeout = 5 * time.Minute

// Options configures Run.
type Options struct {
	AppName       string
	GitopsRepoURL string
	EnvPath       string
	PEMPath       string
	Owner         string
	WebhookURL    string
	AssumeYes     bool
	// GitHubURL is GITHUB_URL as the canonical base github.ParseWebURL
	// returns and GitHubAPIURL is GITHUB_API_URL, validated
	// (config.LoadGitHubURLs).
	GitHubURL    string
	GitHubAPIURL string
}

// Run creates a GitHub App through the manifest flow: it prints the plan, asks
// for confirmation unless opts.AssumeYes, serves the manifest form, exchanges the
// callback code for the App's credentials and writes the PEM and .env files.
//
// Documented at https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest.
func Run(ctx context.Context, stdin io.Reader, stdout io.Writer, opts Options) error {
	webhookURL, err := resolveWebhookURL(opts.WebhookURL, readNgrokTunnel)
	if err != nil {
		return err
	}

	printPlan(stdout, opts, webhookURL)
	if !opts.AssumeYes {
		ok, confirmErr := confirm(stdin, stdout)
		if confirmErr != nil {
			return confirmErr
		}
		if !ok {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
	}
	fmt.Fprintln(stdout)

	state := randomHex(stateBytes)

	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", manifestServerHost+":0")
	if err != nil {
		return fmt.Errorf("listening: %w", err)
	}
	defer ln.Close()
	localAddr := ln.Addr().String()
	redirectURL := fmt.Sprintf("http://%s/callback", localAddr)
	startURL := fmt.Sprintf("http://%s/start", localAddr)

	manifest := map[string]any{
		"name": opts.AppName,
		"url":  "http://localhost:8080",
		"hook_attributes": map[string]any{
			"url":    webhookURL,
			"active": true,
		},
		"redirect_url": redirectURL,
		"public":       false,
		"default_permissions": map[string]string{
			"contents":      "write",
			"pull_requests": "write",
			"metadata":      "read",
		},
		"default_events": appEvents(),
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{
		Handler:           bootstrapMux(string(manifestJSON), state, opts.Owner, opts.GitHubURL, codeCh, errCh),
		ReadHeaderTimeout: callbackReadHeaderTimeout,
	}
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	fmt.Fprintf(stdout, "Opening browser: %s\n", startURL)
	if openErr := openBrowser(startURL); openErr != nil {
		fmt.Fprintf(stdout, "  (could not open browser automatically: %v)\n  Open this URL manually.\n", openErr)
	}

	var code string
	select {
	case code = <-codeCh:
	case bErr := <-errCh:
		return bErr
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for GitHub callback: %w", ctx.Err())
	}

	return completeApp(ctx, stdout, opts, code)
}

// completeApp exchanges the manifest code for the App's credentials, writes
// the PEM and .env files and prints the next steps.
func completeApp(ctx context.Context, stdout io.Writer, opts Options, code string) error {
	client, err := github.NewClient(opts.GitHubAPIURL)
	if err != nil {
		return fmt.Errorf("GITHUB_API_URL: %w", err)
	}
	conv, _, err := client.Apps.CompleteAppManifest(ctx, code)
	if err != nil {
		return fmt.Errorf("exchanging manifest code: %w", err)
	}
	if conv.GetPEM() == "" || conv.GetID() == 0 {
		return errors.New("manifest exchange returned empty credentials")
	}

	if writeErr := os.WriteFile(opts.PEMPath, []byte(conv.GetPEM()), 0o600); writeErr != nil {
		return fmt.Errorf("writing PEM: %w", writeErr)
	}
	deployToken := randomHex(deployTokenBytes)
	envBody := renderEnv(envValues{
		AppID:         conv.GetID(),
		PEMPath:       opts.PEMPath,
		WebhookSecret: conv.GetWebhookSecret(),
		GitOpsRepoURL: opts.GitopsRepoURL,
		DeployToken:   deployToken,
	})
	if writeErr := os.WriteFile(opts.EnvPath, []byte(envBody), 0o600); writeErr != nil {
		return fmt.Errorf("writing .env: %w", writeErr)
	}

	fmt.Fprintln(stdout, "\nApp created.")
	fmt.Fprintf(stdout, "  id:    %d\n", conv.GetID())
	fmt.Fprintf(stdout, "  slug:  %s\n", conv.GetSlug())
	fmt.Fprintf(stdout, "  url:   %s\n", conv.GetHTMLURL())
	fmt.Fprintf(stdout, "  pem:   %s\n", opts.PEMPath)
	fmt.Fprintf(stdout, "  env:   %s\n", opts.EnvPath)

	installURL := conv.GetHTMLURL() + "/installations/new"
	fmt.Fprintf(stdout, "\nNext: install the App on the gitops repo + service repos:\n  %s\n", installURL)
	_ = openBrowser(installURL)
	fmt.Fprintln(stdout, "\nAfter install, paste the installation ID into .env (GITHUB_INSTALLATION_ID),")
	fmt.Fprintln(stdout, "then run `badil verify-app` to confirm.")
	return nil
}

// appEvents returns the events the App subscribes to. `installation` and
// `installation_repositories` are App-level events delivered automatically
// regardless of subscriptions — including them makes GitHub reject the
// manifest. Only repository-scoped events belong here. `push` syncs previews
// when badil.yaml changes on the gitops default branch.
func appEvents() []string {
	return []string{"pull_request", "push"}
}

// printPlan emits a preflight summary describing every state change the
// command is about to make: the App being created, the URL it points at, the
// files that will be written, and whether any of them will be overwritten.
// Reads cleanly in a terminal without color so it's safe in any environment.
func printPlan(w io.Writer, opts Options, webhookURL string) {
	fmt.Fprintln(w, "Preflight — `badil bootstrap-app` will:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  1. Create a new GitHub App")
	fmt.Fprintf(w, "       name:        %s\n", opts.AppName)
	fmt.Fprintf(w, "       webhook URL: %s\n", webhookURL)
	if opts.Owner != "" {
		fmt.Fprintf(w, "       owner:       %s (organization)\n", opts.Owner)
	} else {
		fmt.Fprintln(w, "       owner:       your personal GitHub account")
	}
	fmt.Fprintln(w, "       permissions: contents:write, pull_requests:write, metadata:read")
	fmt.Fprintf(w, "       events:      %s (installation events fire automatically)\n", strings.Join(appEvents(), ", "))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  2. Open a browser tab to %s/settings/apps/new and submit the manifest\n", webHost(opts.GitHubURL))
	fmt.Fprintln(w, "     (one click on \"Create GitHub App\" required from you)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  3. Write files locally (mode 0600):")
	fmt.Fprintf(w, "       %s   %s\n", opts.PEMPath, overwriteNote(opts.PEMPath))
	fmt.Fprintf(w, "       %s   %s\n", opts.EnvPath, overwriteNote(opts.EnvPath))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  4. Open the App install page in your browser so you can attach it to repos")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Nothing is sent to GitHub before step 2. Nothing is written to disk before step 3.")
}

// webHost returns the host of githubURL, with its port: an address to open.
func webHost(githubURL string) string {
	u, err := url.Parse(githubURL)
	if err != nil {
		return githubURL
	}
	return u.Host
}

// overwriteNote returns a parenthetical warning if path already exists.
func overwriteNote(path string) string {
	if _, err := os.Stat(path); err == nil {
		return "(will overwrite existing file)"
	}
	return "(new file)"
}

// confirm prompts the user for y/N and returns true only on an explicit
// "y" or "yes" (case-insensitive). Anything else — including just hitting
// enter — is treated as a no.
func confirm(in io.Reader, w io.Writer) (bool, error) {
	fmt.Fprint(w, "\nProceed? [y/N] ")
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("reading confirmation: %w", err)
		}
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}

// bootstrapMux serves /start (renders the self-submitting manifest form,
// posted to the App creation page of githubURL) and /callback (captures
// ?code= and validates state). Both routes are single-use.
func bootstrapMux(
	manifest, state, org, githubURL string, codeCh chan<- string, errCh chan<- error,
) http.Handler {
	mux := http.NewServeMux()

	action := githubURL + "/settings/apps/new?state=" + state
	if org != "" {
		action = fmt.Sprintf("%s/organizations/%s/settings/apps/new?state=%s", githubURL, org, state)
	}

	startPage := template.Must(template.New("start").Parse(`<!doctype html>
<html><body>
<p>Redirecting to GitHub to create the App. Click the button if it does not redirect automatically.</p>
<form id="f" action="{{.Action}}" method="post">
  <input type="hidden" name="manifest" value='{{.Manifest}}'>
  <button type="submit">Continue to GitHub</button>
</form>
<script>document.getElementById('f').submit();</script>
</body></html>`))

	mux.HandleFunc("/start", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = startPage.Execute(w, map[string]string{
			"Action":   action,
			"Manifest": manifest,
		})
	})

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		gotState := r.URL.Query().Get("state")
		if gotState != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- errors.New("state mismatch on callback")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			errCh <- errors.New("callback missing code")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body>
<p>GitHub App created. You can close this tab and return to the terminal.</p>
</body></html>`)
		codeCh <- code
	})
	return mux
}

// resolveWebhookURL returns the App's full webhook URL. With flag empty it
// asks ngrok for the tunnel URL and returns ngrok's error unchanged. Otherwise
// flag must be an absolute https URL without credentials, query or fragment;
// webhookPath is appended unless the path already ends with it.
func resolveWebhookURL(flag string, ngrok func() (string, error)) (string, error) {
	if flag == "" {
		base, err := ngrok()
		if err != nil {
			return "", err
		}
		return base + webhookPath, nil
	}
	invalid := fmt.Errorf(
		"--webhook-url must be an absolute https URL without credentials, query or fragment, got %q",
		flag,
	)
	u, err := url.Parse(flag)
	if err != nil || strings.ContainsAny(flag, "?#") || u.Scheme != "https" || u.User != nil ||
		u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	if !strings.HasSuffix(p, webhookPath) {
		p += webhookPath
	}
	return u.Scheme + "://" + u.Host + p, nil
}

// readNgrokTunnel queries the local ngrok inspector for the first https
// public_url. Fails fast with an actionable message if ngrok isn't running.
func readNgrokTunnel() (string, error) {
	unreachable := func(err error) error {
		return fmt.Errorf("ngrok inspector not reachable at %s — is `ngrok http 8080` running? (%w)", ngrokAPI, err)
	}
	client := &http.Client{Timeout: ngrokTimeout}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ngrokAPI, nil)
	if err != nil {
		return "", unreachable(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", unreachable(err)
	}
	defer resp.Body.Close()
	var body struct {
		Tunnels []struct {
			PublicURL string `json:"public_url"`
			Proto     string `json:"proto"`
		} `json:"tunnels"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr != nil {
		return "", fmt.Errorf("decoding ngrok tunnels: %w", decodeErr)
	}
	for _, t := range body.Tunnels {
		if t.Proto == "https" && strings.HasPrefix(t.PublicURL, "https://") {
			return t.PublicURL, nil
		}
	}
	return "", errors.New("no https tunnel found in ngrok — start one with `ngrok http 8080`")
}

// randomHex returns n random bytes hex-encoded. crypto/rand.Read never
// returns an error.
func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// DefaultAppName returns "badil-dev-" plus 6 random lowercase hex chars.
// GitHub App names are global, so the suffix avoids collisions without
// publishing anything about the developer's machine.
func DefaultAppName() string {
	buf := make([]byte, 3) //nolint:mnd // 3 bytes = 6 hex chars
	_, _ = rand.Read(buf)  // crypto/rand.Read never returns an error
	return "badil-dev-" + hex.EncodeToString(buf)
}

type envValues struct {
	AppID         int64
	PEMPath       string
	WebhookSecret string
	GitOpsRepoURL string
	DeployToken   string
}

func renderEnv(v envValues) string {
	gitops := v.GitOpsRepoURL
	if gitops == "" {
		gitops = " # set this — the clone URL of your gitops repository, e.g. https://github.com/<owner>/<repo>.git"
	}
	return fmt.Sprintf(`export GITHUB_APP_ID=%d
export GITHUB_PRIVATE_KEY_PATH=%s
export GITHUB_WEBHOOK_SECRET=%s
export GITHUB_INSTALLATION_ID= # set after installing the App
export GITOPS_REPO_URL=%s
export BADIL_DEPLOY_TOKEN=%s
export LISTEN_ADDR=:8080
`, v.AppID, v.PEMPath, v.WebhookSecret, gitops, v.DeployToken)
}

// openBrowser launches the platform-default browser for url. Failure is
// non-fatal; the URL is printed so the user can open it by hand.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	// The browser outlives the command: no context may kill it.
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(context.Background(), "open", url)
	case "windows":
		cmd = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.CommandContext(context.Background(), "xdg-open", url)
	}
	return cmd.Start()
}
