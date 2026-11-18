# HTTP API reference

`badil serve` exposes these endpoints. The variables they depend on are in [Server environment variables](CONFIGURATION.md#server-environment-variables).

| Endpoint | Listener | Enabled by | Purpose |
|---|---|---|---|
| [`GET /healthz`](#get-healthz) | `LISTEN_ADDR` | always | health check |
| [`GET /metrics`](#get-metrics) | `METRICS_ADDR` | always, unless `METRICS_ADDR` is `""` | Prometheus metrics |
| [`POST /v1/deploy`](#post-v1deploy) | `LISTEN_ADDR` | `BADIL_DEPLOY_TOKEN` | manual deploy |
| [`POST /v1/sync`](#post-v1sync) | `LISTEN_ADDR` | `BADIL_DEPLOY_TOKEN` | sync previews with `badil.yaml` |
| [`POST /webhooks/github`](#post-webhooksgithub) | `LISTEN_ADDR` | always | GitHub App deliveries |
| [`POST /webhooks/argocd`](#post-webhooksargocd) | `LISTEN_ADDR` | `ARGOCD_WEBHOOK_TOKEN` | Argo CD spin-up events |

Each endpoint below follows the same pattern: availability, auth, request, response and errors.

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
- **Errors:** `409 {"error": "preview limit (<max>) reached"}` when `preview.maxPreviews` is reached and `previews/<slug>/` does not exist. Other read or list errors are answered like the write's.

`service` must be a service of `badil.yaml`. `commit_sha` accepts any value: send the full SHA, or a `{sha7}`/`{sha8}` image tag will not match the one CI pushed (see [`imageTag`](CONFIGURATION.md#imagetag)).

### Flow

A deploy runs these steps:

1. Validate `service` against the `services` of `badil.yaml`.
2. Slugify `head_ref`.
3. Refuse a new preview past the preview cap with 409 (see [Preview cap check](#preview-cap-check)).
4. Read or bootstrap `previews/<slug>/values.yaml`.
5. Write it, with SHA optimistic locking + 409 retry.
6. Fan out the PR comment, best-effort: failures are returned as `warnings`.

It always writes, even when only `updated-at` changes, so a repeated deploy of the same commit refreshes the TTL clock.

A manual deploy runs outside the reconcile queue and skips the one-PR-per-service check (see [One PR per service](LIFECYCLE.md#one-pr-per-service)). The cleanup sweep may later unpin it (see [Manual deploy pins](LIFECYCLE.md#manual-deploy-pins)).

### Preview cap check

After the catalog check, the deploy reads `previews/<slug>/values.yaml`. When it does not exist, it lists the previews.

At the cap, it answers `409 {"error": "preview limit (<max>) reached"}`, writes nothing and logs `deploy failed` with `op` `preview limit`. Other read or list errors are answered like the write's, with `op` `read preview` or `list previews`.

The cap is soft: see [Preview cap](LIFECYCLE.md#preview-cap).

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

It ignores and keeps the push SHA. See [Config sync](LIFECYCLE.md#config-sync).

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
| `pull_request` | action `opened`, `reopened`, `synchronize`, `labeled`, `unlabeled` or `closed` | async reconcile: see [Label flow](LIFECYCLE.md#label-flow) |
| `pull_request` | any other action | log only |
| `push` | to `GITOPS_REPO`'s default branch, not a deletion | async config sync: see [Push](LIFECYCLE.md#push) |
| `push` | any other push | log only |
| any other event | | structured log only |

## `POST /webhooks/argocd`

Receives the spin-up events of Argo CD's notifications controller (see [Spin-up time](OPERATIONS.md#spin-up-time)).

- **Availability:** only when `ARGOCD_WEBHOOK_TOKEN` is set; 404 otherwise. In the chart, it is the optional `argocd-webhook-token` key of `existingSecret`. The public Ingress exposes the route too.
- **Auth:** `Authorization: Bearer $ARGOCD_WEBHOOK_TOKEN`, compared in constant time. A missing or wrong token gets 401.
- **Request:** `{ "app", "service", "preview", "revision", "updatedAt" }`, at most 64 KiB.
- **Response:** 204 for every valid, authorized event, recorded or not.
- **Errors:** invalid JSON or a larger body gets 400.

An event is recorded when `service` is set and `updatedAt` is RFC 3339 and 0 to 24h old. Any other valid event is ignored. Unpinned services carry no `updated-at`, so their events are ignored.

Events are not checked against the catalog: the token is the gate.

Each valid, authorized event logs `argocd event` with `app`, `service`, `preview`, `revision`, `result` and, when recorded, `spinup_seconds`.
