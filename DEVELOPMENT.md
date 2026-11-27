# Development

Local setup for badil: ngrok tunnel (or a public URL), GitHub App, env, smoke test.

## Prerequisites

- Go 1.27+
- `ngrok` (unless badil has a public URL)
- `gh` CLI, authenticated (`gh auth status`)
- GitHub account with admin access to the gitops repo

## Quickstart

```bash
# Terminal 1 — start the webhook tunnel
ngrok http 8080
# with a reserved ngrok domain: ngrok http 8080 --url https://<domain> (the URL survives restarts)

# Terminal 2 — from the repository root
# Create the App by hand and write .env: see "Create the GitHub App" below
# Create a token with Contents write on the gitops repo and set it in .env (GITOPS_REPO_TOKEN=...)

make dev-serve             # starts the server
make dev-cleanup           # runs one cleanup sweep (CLEANUP_TTL=168h make dev-cleanup to also expire idle previews)
```

The sections below detail each step.

## Smoke the deploy endpoint

badil reads the PR from GitHub, so the deploy needs a real PR on the service's repository, `$ORG/backend-1` (its `repo` in `badil.yaml`). With the server running, paste this verbatim — `$ORG` resolves to your GitHub login via `gh`. It opens a PR from `feature/smoke-test`, then deploys it:

```bash
source .env
ORG=$(gh api /user --jq .login)

SMOKE=$(mktemp -d)
gh repo clone "$ORG/backend-1" "$SMOKE"
git -C "$SMOKE" switch -c feature/smoke-test
git -C "$SMOKE" commit --allow-empty -m "smoke test"
git -C "$SMOKE" push -u origin feature/smoke-test
gh pr create --repo "$ORG/backend-1" --head feature/smoke-test --title "smoke test" --body ""
PR=$(gh pr view feature/smoke-test --repo "$ORG/backend-1" --json number --jq .number)

curl -s -X POST http://localhost:8080/v1/deploy \
  -H "Authorization: Bearer $BADIL_DEPLOY_TOKEN" \
  -H "Content-Type: application/json" \
  -d @- <<EOF | jq
{
  "service": "backend-1",
  "pr_number": "$PR",
  "commit_sha": "0000000000000000000000000000000000000001",
  "workflow_run_url": "https://github.com/$ORG/backend-1/actions/runs/1"
}
EOF
```

A successful response means the gitops commit landed. The slug is the PR's head branch, slugified, and the preview summary is commented on the PR:

```json
{
  "preview_slug": "feature-smoke-test",
  "gitops_commit_url": "https://github.com/.../commit/..."
}
```

Open the gitops repo on GitHub to confirm the commit landed on `main`, with `pr-author: $ORG` and the PR's `pr-url` in `previews/feature-smoke-test/values.yaml`. Close the PR when done.

## Verify webhook plumbing

GitHub App settings → **Advanced** → **Recent Deliveries** → pick any → **Redeliver**. The server answers 200 and logs one line per event type (`webhook ping`, `webhook installation`, `webhook installation_repositories`, `webhook pull_request`, `webhook push`); other event types log `unhandled webhook event` (or `webhook parse failed` if go-github doesn't know the type). A 401 means the webhook secret in `.env` doesn't match what GitHub has — fix on either side. A 403 means the delivery's installation is not `GITHUB_INSTALLATION_ID`: the `webhook installation rejected` log shows the received `installation_id`; compare it with the ID the App's install URL ends with (`/settings/installations/<ID>`).

To exercise the handler without GitHub, sign the body yourself. The HMAC covers the exact body bytes, so use `printf '%s'` (no trailing newline), never `echo`:

```bash
source .env
sign() { printf 'sha256=%s' "$(printf '%s' "$1" | openssl dgst -sha256 -hmac "$GITHUB_WEBHOOK_SECRET" | awk '{print $NF}')"; }
hook() { curl -s -o /dev/null -w "HTTP %{http_code}\n" -X POST http://localhost:8080/webhooks/github \
  -H "X-Github-Event: $1" -H "X-Hub-Signature-256: $2" -H "Content-Type: application/json" -d "$3"; }

hook ping 'sha256=deadbeef' '{"zen":"forged"}'                          # 401, handler never runs
B='{not valid json'; hook pull_request "$(sign "$B")" "$B"               # 200 + "webhook parse failed"
B='{"zen":"manual test","hook_id":1}'; hook ping "$(sign "$B")" "$B"     # 200 + "webhook ping"
```

A signed-but-unparseable delivery is authentic and there is nothing to re-queue, so it is logged and answered with 200; a non-2xx would only flag it as failed in Recent Deliveries.

## Run badil against a mock GitHub

`cmd/fakegithub` answers, from memory, the GitHub API calls badil makes (under `/api/v3/`), and sends badil a signed webhook for each call to its control API (under `/_mock/`): open a PR, label or unlabel it, push a new head SHA, close or reopen it, push to a repo. badil then runs without github.com, a GitHub App or a tunnel. A throwaway Gitea hosts the gitops repo. It needs Docker, `curl`, `jq` and `openssl`, and ports 3000, 8080 and 8090 free.

Terminal 1: start Gitea, create the user `dev`, its token and the `gitops` repo, push a `badil.yaml` with one service `demo`, and write the settings the other terminals share:

```bash
docker run -d --name badil-gitea -p 3000:3000 -e GITEA__security__INSTALL_LOCK=true docker.gitea.com/gitea:28.4.0
until curl -sf http://localhost:3000/api/healthz >/dev/null; do sleep 1; done

docker exec -u git badil-gitea gitea admin user create \
  --username dev --password dev-password --email dev@example.com --admin --must-change-password=false
GITEA_TOKEN=$(docker exec -u git badil-gitea gitea admin user generate-access-token \
  --username dev --token-name badil --scopes all --raw)

curl -sf -X POST http://localhost:3000/api/v1/user/repos \
  -H "Authorization: token $GITEA_TOKEN" -H 'Content-Type: application/json' -d '{"name":"gitops"}' >/dev/null

seed=$(mktemp -d)
cat > "$seed/badil.yaml" <<'EOF'
services:
  demo:
    repo: http://localhost:3000/dev/demo.git
EOF
git -C "$seed" init -q -b main
git -C "$seed" add badil.yaml
git -C "$seed" -c user.name=dev -c user.email=dev@example.com commit -qm "seed badil.yaml"
git -C "$seed" push -q "http://dev:$GITEA_TOKEN@localhost:3000/dev/gitops.git" main

cat > /tmp/badil-mock.env <<EOF
export FAKEGITHUB_WEBHOOK_SECRET=$(openssl rand -hex 32)
export GITEA_TOKEN=$GITEA_TOKEN
EOF
```

Still in terminal 1, start the mock. Its PR and repo URLs are on Gitea (`--web-url`), and its webhooks go to badil:

```bash
source /tmp/badil-mock.env
go run ./cmd/fakegithub --web-url http://localhost:3000 --webhook-url http://localhost:8080/webhooks/github
```

Terminal 2: start badil on the mock's API, with `GITHUB_URL` equal to the mock's `--web-url`, `GITHUB_INSTALLATION_ID` equal to its `--installation-id` (default `1`) and the same webhook secret. The mock does not verify the App JWT, so any App ID and a throwaway key do:

```bash
source /tmp/badil-mock.env
GITHUB_APP_ID=1 \
GITHUB_PRIVATE_KEY="$(openssl genrsa 2048 2>/dev/null)" \
GITHUB_WEBHOOK_SECRET=$FAKEGITHUB_WEBHOOK_SECRET \
GITHUB_INSTALLATION_ID=1 \
GITHUB_URL=http://localhost:3000 \
GITHUB_API_URL=http://localhost:8090/api/v3/ \
GITOPS_REPO_URL=http://localhost:3000/dev/gitops.git \
GITOPS_REPO_TOKEN=$GITEA_TOKEN \
go run ./cmd/badil serve
```

Terminal 3: open PR 1 on `dev/demo`, add the label `preview`, then wait for badil's comment. Each control call answers with the PR and `webhook`, badil's answer to the delivery (`"status": 200`, or `"error"` when badil is unreachable):

```bash
curl -s -X POST http://localhost:8090/_mock/repos/dev/demo/pulls \
  -d '{"head_ref":"feature/demo","head_sha":"0000000000000000000000000000000000000001"}' | jq .webhook
curl -s -X POST http://localhost:8090/_mock/repos/dev/demo/pulls/1/labels -d '{"name":"preview"}' | jq .webhook

for i in $(seq 60); do
  comments=$(curl -s http://localhost:8090/_mock/repos/dev/demo/pulls/1/comments)
  [ "$(jq length <<<"$comments")" -gt 0 ] && break
  sleep 1
done
jq -r '.[].body' <<<"$comments"
```

The other control calls:

| Call | Body | Webhook |
|---|---|---|
| `POST /_mock/repos/{o}/{r}/pulls` | `{"head_ref", "head_sha", "number"?, "base"?, "author"?, "labels"?, "fork"?, "files"?: [{"filename", "status"?, "previous_filename"?}]}`; each of `labels` records a `labeled` issue event by `author` | `pull_request` `opened` |
| `POST /_mock/repos/{o}/{r}/pulls/{n}/labels` | `{"name", "sender"?}`; records a `labeled` issue event by `sender`, default the PR's author, unless the PR already has the label | `labeled` |
| `DELETE /_mock/repos/{o}/{r}/pulls/{n}/labels/{name}` | | `unlabeled` |
| `POST /_mock/repos/{o}/{r}/pulls/{n}/push` | `{"head_sha", "files"?}` | `synchronize` |
| `POST /_mock/repos/{o}/{r}/pulls/{n}/close` | `{"merged"?}` | `closed` |
| `POST /_mock/repos/{o}/{r}/pulls/{n}/reopen` | | `reopened` |
| `POST /_mock/repos/{o}/{r}/push` | `{"after", "ref"?, "deleted"?}` | `push` |
| `PUT /_mock/repos/{o}/{r}/collaborators/{login}/permission` | `{"permission"}`: `admin`, `maintain`, `write`, `triage`, `read` or `none`. It sets the role the permission read answers for `login`, `write` by default; only `write`, `maintain` and `admin` have `push`, which badil's [labeller check](docs/EVENTS.md#labeller-check) requires | none |
| `GET /_mock/repos/{o}/{r}/pulls/{n}/comments` | | none: the comments badil posted |
| `GET /_mock/stats` | | none: the API calls per route |
| `POST /_mock/reset` | | none: clears the repos, PRs, comments, `labeled` events, permissions and counters |

To stop, press Ctrl-C in terminals 1 and 2, then remove Gitea and the settings:

```bash
docker rm -f badil-gitea
rm /tmp/badil-mock.env
```

## Appendix A — common failures

| Symptom | Cause | Fix |
|---|---|---|
| Deliveries return 404 on `POST /` (Recent Deliveries, or `http request … path=/ status=404` in the logs) | The App's webhook URL lacks `/webhooks/github` | In the App settings, set **Webhook URL** to `<base>/webhooks/github` |
| `badil serve` exits with `github app check: GET /app: … 401 A JSON web token could not be decoded` | `GITHUB_APP_ID` and `dev.private-key.pem` don't match | Regenerate the private key from the App settings page and replace `dev.private-key.pem` |
| `badil serve` exits with `github app check: installation <ID> token: … response status "404 Not Found" …` | The App is not installed, or `GITHUB_INSTALLATION_ID` is wrong | Install the App and take the ID from the install URL (`/settings/installations/<ID>`) |
| `badil serve` fails with `GITHUB_APP_ID is required` | `.env` not sourced | `source .env` first, or use `make dev-serve` which sources for you |
| `badil serve` fails with `GITOPS_REPO_TOKEN is required` | `GITOPS_REPO_TOKEN` is not set in `.env` | Set it to a token with Contents write on the gitops repo |

## Appendix B — create the GitHub App

Create the dev App by hand:

1. https://github.com/settings/apps/new — fill in:
   - **GitHub App name**: `badil-dev-<your-handle>` (globally unique)
   - **Homepage URL**: `http://localhost:8080`
   - **Webhook URL**: `<your-public-base-url>/webhooks/github`
   - **Webhook secret**: `openssl rand -hex 32` — save the value
   - **Repository permissions**: Contents R, Pull requests W, Metadata R
   - **Subscribe to events**: `pull_request` and `push` (installation / installation_repositories fire automatically — do not subscribe to them explicitly)
   - **Where can this be installed?**: Only on this account
2. Click **Create GitHub App**. Note the **App ID** at the top of the page.
3. Scroll to **Private keys** → **Generate a private key**. Move the downloaded `.pem` to `dev.private-key.pem` at the repository root.
4. Install the App (left sidebar → **Install App**) on the gitops repo and service repos. After install, the browser URL ends with `/settings/installations/<ID>`; that number is `GITHUB_INSTALLATION_ID`.
5. Write `.env` manually:

   ```bash
   cat > .env <<EOF
   export GITHUB_APP_ID=<from step 2>
   export GITHUB_PRIVATE_KEY_PATH=./dev.private-key.pem
   export GITHUB_WEBHOOK_SECRET=<from step 1>
   export GITHUB_INSTALLATION_ID=<from the install URL>
   export GITOPS_REPO_URL=https://github.com/<your-org>/<your-gitops-repo>.git
   export GITOPS_REPO_TOKEN=<a token with Contents write on the gitops repo>
   export BADIL_DEPLOY_TOKEN=$(openssl rand -hex 32)
   export LISTEN_ADDR=:8080
   EOF
   chmod 600 .env
   ```

The App has Contents read only: badil pushes to the gitops repo with `GITOPS_REPO_TOKEN`, a token with Contents write on the gitops repo (a fine-grained personal access token with access to that repo only). Without it, badil exits at start with `GITOPS_REPO_TOKEN is required`. See [Gitops repo credentials](docs/CONFIGURATION.md#gitops-repo-credentials).

On another GitHub host (GitHub Enterprise), add `GITHUB_URL` and `GITHUB_API_URL` to `.env`: `serve` reads them (see [Server environment variables](docs/CONFIGURATION.md#server-environment-variables)).
