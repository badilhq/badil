# HTTP API reference

`badil serve` exposes these endpoints. The variables they depend on are in [Server environment variables](CONFIGURATION.md#server-environment-variables).

| Endpoint | Listener | Enabled by | Purpose |
|---|---|---|---|
| [`GET /healthz`](#get-healthz) | `LISTEN_ADDR` | always | health check |
| [`GET /metrics`](#get-metrics) | `METRICS_ADDR` | always, unless `METRICS_ADDR` is `""` | Prometheus metrics |
| [`POST /v1/deploy`](#post-v1deploy) | `LISTEN_ADDR` | `BADIL_DEPLOY_TOKEN` | manual deploy |
| [`POST /v1/sync`](#post-v1sync) | `LISTEN_ADDR` | `BADIL_DEPLOY_TOKEN` | sync previews with `badil.yaml` |
| [`GET /v1/previews`](#get-v1previews) | `LISTEN_ADDR` | `BADIL_READ_TOKEN` or `BADIL_DEPLOY_TOKEN` | list previews and their status |
| [`GET /v1/previews/{slug}`](#get-v1previewsslug) | `LISTEN_ADDR` | `BADIL_READ_TOKEN` or `BADIL_DEPLOY_TOKEN` | one preview and its status |
| [`GET /v1/previews/{slug}/wait`](#get-v1previewsslugwait) | `LISTEN_ADDR` | `BADIL_READ_TOKEN` or `BADIL_DEPLOY_TOKEN` | wait for a preview to be ready |
| [`GET /v1/wait`](#get-v1wait) | `LISTEN_ADDR` | `BADIL_READ_TOKEN` or `BADIL_DEPLOY_TOKEN` | wait for a PR's preview to be ready |
| [`POST /webhooks/github`](#post-webhooksgithub) | `LISTEN_ADDR` | always | GitHub App deliveries |
| [`POST /webhooks/status`](#post-webhooksstatus) | `LISTEN_ADDR` | `BADIL_STATUS_TOKEN` | renderer status callback |

Each endpoint below follows the same pattern: availability, auth, request, response and errors.

With neither `BADIL_DEPLOY_TOKEN` nor `BADIL_READ_TOKEN` set, every `/v1` route answers 404. `POST /v1/deploy` and `POST /v1/sync` accept only the deploy token; the `GET /v1` routes accept either token.

## `GET /healthz`

- **Availability:** always, on `LISTEN_ADDR`.
- **Auth:** none.
- **Request:** no body.
- **Response:** `{"status":"ok"}`.

## `GET /metrics`

- **Availability:** on `METRICS_ADDR` only (default `:9090`), not on `LISTEN_ADDR`. Setting `METRICS_ADDR` to `""` disables it.
- **Auth:** none. The listener is separate from `LISTEN_ADDR`, so the Ingress never exposes it.
- **Request:** no body.
- **Response:** Prometheus metrics, listed in [Metrics](OPERATIONS.md#metrics).

## `POST /v1/deploy`

The manual trigger: it pins one service of a preview at a commit.

- **Availability:** only when `BADIL_DEPLOY_TOKEN` is set; 404 otherwise.
- **Auth:** `Authorization: Bearer $BADIL_DEPLOY_TOKEN`.
- **Request:** a JSON object with `service`, `head_ref`, `commit_sha`, `pr_author`, `pr_url`, `pr_number` and `workflow_run_url`.
- **Response:** `{ "preview_slug", "gitops_commit_url", "warnings": [...] }`. `warnings` holds the PR comment fan-out failures.
- **Errors:** `409 {"error": "preview limit (<max>) reached"}` when `preview.maxPreviews` is reached and `previews/<slug>/` does not exist. A write the gitops remote refused for badil's credentials answers 502.

`service` must be a service of `badil.yaml`. `commit_sha` accepts any value: send the full SHA, or a `{sha7}`/`{sha8}` image tag will not match the one CI pushed (see [`imageTag`](CONFIGURATION.md#imagetag)).

### Flow

A deploy runs these steps:

1. Validate `service` against the `services` of `badil.yaml`.
2. Slugify `head_ref`.
3. Refuse a new preview past the preview cap with 409 (see [Preview cap check](#preview-cap-check)).
4. Read or bootstrap `previews/<slug>/values.yaml`.
5. Write it in one commit, `chore(preview): deploy <service>@<sha7> to <slug>`, pushed again on a moved remote head (the push retry). Steps 3 and 4 are decided again on every push attempt.
6. Fan out the PR comment, best-effort: failures are returned as `warnings`.

It always writes, even when only `updated-at` changes, so a repeated deploy of the same commit refreshes the TTL clock. `gitops_commit_url` is the URL of the pushed commit, `https://github.com/<GITOPS_REPO>/commit/<sha>`. The one exception is two identical calls within the same second: the second leaves the file as it is, so it commits nothing, and its `gitops_commit_url` and comment link to the gitops repo's current head.

A manual deploy runs outside the reconcile queue and skips the one-PR-per-service check (see [One PR per service](EVENTS.md#one-pr-per-service)). The cleanup sweep may later unpin it (see [Manual deploy pins](CLEANUP.md#manual-deploy-pins)).

### Preview cap check

The deploy reads `previews/<slug>/values.yaml` on the head its commit is made on. When it does not exist, it lists the previews there.

At the cap, it answers `409 {"error": "preview limit (<max>) reached"}`, writes nothing and logs `deploy failed` with `op` `preview limit`. Other read or list errors are answered like the write's, with `op` `read preview` or `list previews`. A failed write is logged with `op` `write preview`.

The check is decided again on every push attempt, so a concurrent deploy that creates a preview is counted: see [Preview cap](EVENTS.md#preview-cap).

## `POST /v1/sync`

Syncs every preview, or one, with `badil.yaml` now, and answers when done.

- **Availability:** only when `BADIL_DEPLOY_TOKEN` is set; 404 otherwise.
- **Auth:** `Authorization: Bearer $BADIL_DEPLOY_TOKEN`.
- **Request:** an empty body or `{}` syncs every preview. `{ "slug": "<slug>" }` syncs that preview. An unknown field gets 400.
- **Response:** `{ "synced": [<slugs rewritten>], "unchanged": <count>, "failed": [<slugs>] }`.
- **Errors:**
  - `400 {"error": "invalid slug"}`: the slug is empty, or not its own slugified form (e.g. `../x`).
  - `404 {"error": "preview <slug> not found"}`: no `previews/<slug>/values.yaml`.
  - `503 {"error": "badil.yaml unavailable: …"}`: `badil.yaml` can't be read or parsed.

It ignores and keeps the push SHA. See [Config sync](CLEANUP.md#config-sync).

## `GET /v1/previews`

Lists the previews, read from badil's clone of the gitops repo and its status memory. It makes no GitHub or Kubernetes call.

- **Availability:** when `BADIL_READ_TOKEN` or `BADIL_DEPLOY_TOKEN` is set; 404 otherwise.
- **Auth:** `Authorization: Bearer <token>`, either token, compared in constant time. A missing or wrong token gets 401.
- **Request:** no body.
- **Response:** `{"previews": [<preview>, ...]}`, sorted by slug: one [preview](#preview-json) per `previews/<slug>/values.yaml`.
- **Errors:** 503 `{"error": "<message>"}` when the clone can't be read (a failed `ls-remote` or fetch, or a read error of `badil.yaml` or a preview file), logged as `read failed` with `route` and `err`.

### Preview JSON

```json
{
  "slug": "feature-pricing",
  "createdAt": "2026-11-19T20:00:00Z",
  "updatedAt": "2026-11-19T21:00:00Z",
  "expiresAt": "2026-11-26T21:00:00Z",
  "services": [
    {
      "name": "backend-1", "kind": "service", "branch": "main",
      "pinned": {"sha": "abc1234...", "pr": {"repo": "acme/backend-1", "number": 42, "url": "https://github.com/acme/backend-1/pull/42"}, "author": "octocat"},
      "url": "https://backend-1-feature-pricing.example.com/docs",
      "status": "ready", "revision": "abc1234...", "statusAt": "2026-11-19T21:03:00Z"
    },
    {"name": "postgres", "kind": "dependency", "status": "unknown"}
  ]
}
```

| Field | Value |
|---|---|
| `slug` | the preview's slug |
| `createdAt` | the oldest `metadata.created-at` of its pinned entries; omitted without a pinned entry |
| `updatedAt` | the newest `metadata.updated-at` of its pinned entries; omitted without a pinned entry |
| `expiresAt` | `updatedAt` plus `CLEANUP_TTL`, only when `badil serve` runs the in-server sweep with a TTL (`CLEANUP_INTERVAL` and `CLEANUP_TTL` set) |
| `services` | one item per service entry, then per dependency entry, in file order |
| `services[].name`, `kind` | the entry's name, and `service` or `dependency` |
| `services[].pinned` | a pinned entry only: its `commitSha`, the PR of its `metadata.pr-url` (`repo` as `owner/repo`, `number`, `url`) and `metadata.pr-author` |
| `services[].branch` | services only: the branch it tracks |
| `services[].url` | an entry with a `host` only: the same link as the PR comment |
| `services[].status` | the latest [status](#post-webhooksstatus) recorded, or `unknown` |
| `services[].revision`, `statusAt` | the revision and time of that status, when known |

## `GET /v1/previews/{slug}`

Returns one preview, as `GET /v1/previews` does.

- **Availability and auth:** as [`GET /v1/previews`](#get-v1previews).
- **Response:** the [preview](#preview-json).
- **Errors:** 404 `{"error": "preview not found"}` when `previews/<slug>/values.yaml` does not exist; 503 as `GET /v1/previews`.

## `GET /v1/wait`

Waits for a PR's preview to be ready, by long poll: badil holds the request for at most 25s and answers its state. A client asks again until the state is final. [`badil wait`](#badil-wait) does that loop.

- **Availability and auth:** as [`GET /v1/previews`](#get-v1previews).
- **Request:** query `repo=<owner>/<repo>`, `pr=<number>` and, optionally, `sha=<sha>`, full or a prefix. Without `sha`, the PR's current pin defines the SHA.
- **Response:** 200 with the [wait response](#wait-response).
- **Errors:**
  - 400 `{"error": "<message>"}` when `repo` or `pr` is missing, `pr` is not a positive integer, or `repo` is not `owner/repo`;
  - 503 as `GET /v1/previews`, at the first evaluation or any later one.

### Wait states

| State | Final | Meaning |
|---|---|---|
| `pending` | no | no preview pins the PR (at `sha`) yet |
| `progressing` | no | the preview is pinned, and an entry of its wait set is not ready yet |
| `ready` | yes | every entry of the wait set is ready |
| `failed` | yes | an entry is degraded or failed to sync at its pin, or the last reconcile failed |
| `refused` | yes | badil won't deploy the PR, with the reason |
| `skipped` | yes | the PR needs no preview, with the reason |

The state of a PR is decided in this order:

1. `refused`, reason `repo not in catalog`, when `badil.yaml` has no service of `repo`, whatever the PR's labels.
2. The PR's latest reconcile outcome, when its head SHA matches `sha` (or for any SHA without `sha`):
   - `refused` with the skip reason `service held by another pr`, `preview limit reached`, `conflicting preview labels`, `fork pr` or `repo not in catalog`;
   - `refused`, reason `torn down`, after a teardown;
   - `skipped`, with the skip reason `no service changed` or `not labeled`;
   - `failed`, reason `reconcile failed`, after a failed reconcile: nothing retries it before the next event or [catch-up](EVENTS.md#catch-up) run, so re-run the CI job or push to reconcile again.

   Any other outcome decides nothing. An event for a label that is not a preview label (skip reason `other label`) does not replace the latest outcome.
3. `pending` when no preview has an entry whose `metadata.pr-url` names the PR (owner and repo in any letter case) and, with `sha`, whose `commitSha` matches it.
4. The state of the wait set in that preview: `failed`, reason `<service> is <status> at <revision>`, when an entry is failed; `ready` when every entry is ready; `progressing` otherwise.

badil keeps the latest reconcile outcome of each PR in memory only. After a restart, [catch-up](EVENTS.md#catch-up) reconciles each open labelled PR not pinned at head, and records `not labeled` and `fork pr` for the other open PRs of the catalog's repos; until then, a refused PR reads `pending`. The reconciles of `badil cleanup` run in another process and are not recorded: their effect reaches the waits through badil's next fetch of the gitops repo.

**SHAs.** Two SHAs, `sha` against a `commitSha` or a reconcile's head SHA, match when both are set and, in lowercase, the longer starts with the shorter: `POST /v1/deploy` accepts a short `commit_sha`, while Argo CD reports full SHAs. A status's revision is at a pin when it starts with the entry's `commitSha`.

**Wait set.** The PR's pinned entries in the preview, plus every service and dependency in the transitive [`needs`](CONFIGURATION.md#servicesnameneeds-and-dependenciesnameneeds) of those that are catalog services. Without `needs`, it is the PR's pinned entries. A pinned entry is `ready` when its status is `ready` at its pin, and failed when it is `degraded` or `sync-failed` at its pin; another entry is `ready` when its latest status is `ready`, and failed when it is `degraded` or `sync-failed`. Anything else, `unknown` included, is not ready.

**Long poll.** A final state answers at once. Otherwise the request waits until a status event, a commit badil pushes, a fetch that moves the gitops repo's head (a push by `badil cleanup` or by hand) or a recorded reconcile outcome changes the state, then answers; after 25s it answers the current state. When badil shuts down, every waiting request answers its current state at once.

### Wait response

```json
{
  "state": "progressing",
  "slug": "feature-pricing",
  "sha": "abc1234...",
  "services": [{"name": "backend-1", "kind": "service", "status": "progressing", "revision": "abc1234...", "ready": false, "url": "https://backend-1-feature-pricing.example.com/docs"}],
  "urls": {"backend-1": "https://backend-1-feature-pricing.example.com/docs"}
}
```

`reason` is set on `failed`, `refused` and `skipped`; `slug` once a preview is found; `sha` when known (the `sha` parameter, else the pin's or the reconcile's). `services` is the wait set, each with its latest status (`unknown` when none), revision, whether it is ready and its URL; `urls` maps the names of those with a URL to it.

## `GET /v1/previews/{slug}/wait`

Waits for a preview to be ready, by long poll, as [`GET /v1/wait`](#get-v1wait).

- **Availability and auth:** as [`GET /v1/previews`](#get-v1previews).
- **Response:** 200 with the [wait response](#wait-response): `pending` when the preview has no pinned entry yet, otherwise the state of its wait set, which holds every pinned entry and the transitive `needs` of the pinned catalog services.
- **Errors:** 404 `{"error": "preview not found"}` when the preview does not exist, also when a later evaluation finds it deleted; 503 as `GET /v1/previews`.

### `badil wait`

```bash
badil wait --server https://badil.example.com --token "$BADIL_READ_TOKEN" --repo acme/front --pr 42 --sha "$SHA"
badil wait --slug feature-pricing   # with BADIL_SERVER and BADIL_TOKEN set
```

It asks the wait route again until a final state or `--timeout` (a Go duration, default `15m`), and prints one line per state change, then the final state with its reason and the URLs; with `--json`, only the final response body. Connection errors and 5xx answers are retried every 2s until the timeout. It exits:

| Code | When |
|---|---|
| 0 | `ready` or `skipped` |
| 1 | `failed` or `refused` |
| 2 | the timeout passed in `pending` or `progressing` |
| 3 | not found (404), unauthorized (401 or 403), an invalid query (400), bad flags, or the timeout passed while the last request failed |

`badil list` prints the previews of `GET /v1/previews` as a table (`SLUG`, `SERVICES`, `PINNED`, `READY` as ready pinned entries over pinned entries, `UPDATED`), or with `--json` the response body. Both take `--server` (env `BADIL_SERVER`) and `--token` (env `BADIL_TOKEN`).

## `POST /webhooks/github`

Receives the GitHub App's deliveries.

- **Availability:** always.
- **Auth:** `X-Hub-Signature-256` is verified; 401 on mismatch. A delivery whose installation is not `GITHUB_INSTALLATION_ID` gets 403; `ping` is exempt.
- **Request:** a GitHub webhook delivery.
- **Response:** 200.
- **Errors:** parse failures and handler panics are logged and answered 200.

Each event is handled as follows:

| Event | Condition | Action |
|---|---|---|
| `pull_request` | action `opened`, `reopened`, `synchronize`, `labeled`, `unlabeled` or `closed` | async reconcile: see [Label flow](EVENTS.md#label-flow) |
| `pull_request` | any other action | log only |
| `push` | to `GITOPS_REPO`'s default branch, not a deletion | async config sync: see [Push](CLEANUP.md#push) |
| `push` | any other push | log only |
| any other event | | structured log only |

## `POST /webhooks/status`

Receives the renderer's status callback: the status of one service or dependency of a preview. Its body is part of the renderer contract ([Status callback](CONTRACT.md#status-callback)); Argo CD notifications is the reference sender ([`examples/argocd/notifications-values.yaml`](../examples/argocd/notifications-values.yaml)).

- **Availability:** only when `BADIL_STATUS_TOKEN` is set; 404 otherwise. In the chart, it is the optional `status-token` key of `existingSecret`. The public Ingress exposes the route too.
- **Auth:** `Authorization: Bearer $BADIL_STATUS_TOKEN`, compared in constant time. A missing or wrong token gets 401.
- **Request:** `{ "apiVersion": "badil.dev/v1beta1", "preview", "service", "revision", "status", "at" }`, at most 64 KiB. Unknown fields are ignored.
- **Response:** 204 for every valid, authorized event, recorded or ignored.
- **Errors:**
  - invalid JSON, a larger body, or a body failing the contract's validation gets 400;
  - 503, with nothing recorded, when badil cannot read the preview from the gitops repo (for example a failed `ls-remote` or fetch). The renderer retries or sends the status again on its next change.

An event is recorded when `previews/<preview>/values.yaml` exists, its services or dependencies include `service`, and `at` is not older than the status stored for that service. Otherwise it is ignored, with reason `unknown_preview`, `unknown_service` or `stale`. badil keeps the latest status per preview and service in memory and in a ConfigMap of its namespace (see [Preview status](OPERATIONS.md#preview-status)).

A recorded `ready` also records the service's spin-up time (see [Spin-up time](OPERATIONS.md#spin-up-time)).

Each valid, authorized event logs `status event` with `preview`, `service`, `revision`, `status` and `result` (`recorded`, `ignored` or `error`), plus `reason` when ignored, `err` on an error, and `spinup_seconds` when a spin-up was observed.
