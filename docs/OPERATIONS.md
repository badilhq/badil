# Operations reference

This page lists what badil exposes to run it: its metrics, the preview status and spin-up time the renderer reports, the preview deployments Argo CD posts on GitHub, what the optional namespace guard refuses, and the Grafana dashboard. Their setup is in [Install](INSTALL.md#preview-status-optional).

## Metrics

`GET /metrics` serves Prometheus metrics on `METRICS_ADDR`, a listener separate from `LISTEN_ADDR`, so the Ingress never exposes it (see [`GET /metrics`](API.md#get-metrics)). The chart adds a `metrics` port to the Service and, with `metrics.serviceMonitor.enabled`, a ServiceMonitor.

### Metric list

Every label has a closed set of values. Go runtime (`go_*`) and process (`process_*`) metrics are exported too.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `badil_webhook_deliveries_total` | counter | `event`, `status` | Webhook deliveries. `event` is `ping`, `installation`, `installation_repositories`, `pull_request`, `push` or `other` once the signature is verified, `unverified` before. `status` is the HTTP status: `200`, `401` or `403`. |
| `badil_reconciles_total` | counter | `outcome` | One per pull request reconcile: `failed` (catalog or PR read error, PR files list error, a target preview file that can't be read, a failed namespace check, or a failed commit of a deploy), else `skipped`, else `deployed` (a service was written), else `torn_down`. |
| `badil_reconcile_skips_total` | counter | `reason` | Skipped reconciles by reason: `other label`, `not labeled`, `repo not in catalog`, `fork pr`, `service held by another pr`, `preview limit reached`, `namespace in use`, `labeller without write access`, `conflicting preview labels`, `already deployed`, `no service changed`. |
| `badil_teardowns_total` | counter | `trigger`, `result` | Teardowns of a PR from a preview. `trigger` is `webhook` or `sweep`; `result` is `deleted`, `unpinned`, `noop` (nothing pinned) or `error`. |
| `badil_gitops_operation_duration_seconds` | histogram | `op`, `result` | Duration of the gitops operations, each one commit, push retries included: `deploy` (a reconcile of an open PR with a target, or `/v1/deploy`), `teardown` (any other reconcile), `sweep` and `sync` (a push or `/v1/sync`); and of the git `clone`, `ls_remote` and `fetch` of the gitops clone. `result` is `ok` or `error`. |
| `badil_gitops_push_conflicts_total` | counter | `op` | Pushes of a gitops operation rejected because the remote head moved, each retried on the new head. `op` is the operation: `deploy`, `teardown`, `sweep` or `sync`, as in `badil_gitops_operation_duration_seconds`. |
| `badil_sweep_runs_total` | counter | `result` | In-server sweep runs, `ok` or `error`. A run cancelled by shutdown is not counted. |
| `badil_sweep_last_success_timestamp_seconds` | gauge | | Unix time of the last successful in-server sweep. |
| `badil_sweep_previews_deleted_total` | counter | `rule` | Previews deleted by the sweep: `no_pin` (rule 1) or `ttl` (rule 2). |
| `badil_previews_synced_total` | counter | | Previews rewritten to match `badil.yaml`, by a sweep (step 4 of the in-server sweep), a push or `/v1/sync` (see [Config sync](CLEANUP.md#config-sync)). |
| `badil_previews` | gauge | | Previews in the gitops repo: listed at server startup, incremented when a deploy creates a preview, decremented when a teardown or sweep deletes one, and reset by each in-server sweep. |
| `badil_preview_spinup_seconds` | histogram | `catalog_service` | Spin-up time of a pinned service, from badil's gitops write to the renderer's first `ready` at that revision (see [Spin-up time](#spin-up-time)). Buckets: 15, 30, 60, 120, 180, 300, 600, 900, 1800 s. `catalog_service` is a catalog service; it is not named `service` because that collides with the `service` target label Prometheus adds when scraping through a ServiceMonitor. |
| `badil_catchup_reconciles_total` | counter | | Reconciles started by [catch-up](EVENTS.md#catch-up), at server start and after each in-server sweep. Each is also counted in `badil_reconciles_total` once it runs. |
| `badil_status_events_total` | counter | `status`, `result` | Valid, authorized events of [`/webhooks/status`](API.md#post-webhooksstatus). `status` is the reported status: `ready`, `progressing`, `degraded` or `sync-failed`. `result` is `recorded`, `ignored` (unknown preview, unknown service or stale) or `error` (the preview read failed: answered 503). 400 and 401 answers are not counted. |

### Gauges

`badil_sweep_last_success_timestamp_seconds` is always exported and reads 0 until the first in-server sweep.

`badil_previews` is set at server startup, from a listing taken before the server accepts requests and limited to 10 seconds. When that listing fails, the gauge stays as it is until the next in-server sweep or restart.

Deletions made outside the server, such as a manual commit, show only after the next server restart or in-server sweep.

## Preview status

badil never reads what runs in preview namespaces, so the renderer tells it how each preview service is doing, through the [status callback](CONTRACT.md#status-callback). Argo CD's notifications controller is the reference sender: it POSTs to [`/webhooks/status`](API.md#post-webhooksstatus) when a service's or dependency's Application starts a sync (`progressing`), is synced and Healthy (`ready`), turns Degraded (`degraded`) or fails to sync or render (`sync-failed`) at its current target. Configuration: [`examples/argocd/notifications-values.yaml`](../examples/argocd/notifications-values.yaml).

badil keeps the latest status per preview and service:

- **In memory**, the source of truth for reads. A read never calls the Kubernetes API.
- **In one ConfigMap per preview** in badil's namespace (`POD_NAMESPACE`): `badil-status-<slug>`, labelled `app.kubernetes.io/managed-by: badil` and `badil.dev/preview: <slug>`, one `data` key per service holding `{"revision", "status", "at"}`. Writes are coalesced: each preview's ConfigMap is written at most once every 5 s, with the full status of the preview, and once more on graceful shutdown. A failed write is logged and retried at the next flush.
- **On start**, before it listens, badil lists those ConfigMaps and rebuilds memory from them. A failed listing stops the server.

When a preview is deleted, its status and ConfigMap go: at once when the server's own commit deleted it, and otherwise (a manual deletion) after the next fetch of the gitops repo that moves its head, and at start.

The chart's Role lets badil get, list, create, update and delete ConfigMaps in its namespace, nothing else: the status ConfigMaps and `badil-expired` (see [Expired PRs](#expired-prs)). Its ClusterRole, `<release>-namespaces`, only lets it get namespaces, for the [namespace check](EVENTS.md#namespace-in-use) of a new preview. Outside a cluster, or without `POD_NAMESPACE`, the status is kept in memory only and badil logs `status persistence disabled` at start.

### Expired PRs

When the sweeper deletes a preview for `CLEANUP_TTL`, badil records each of its open PRs at its head SHA, so that [catch-up](EVENTS.md#catch-up) does not recreate the preview (see [Cleanup](CLEANUP.md#what-the-sweeper-catches)). The records live in the ConfigMap `badil-expired` of badil's namespace (`POD_NAMESPACE`), labelled `app.kubernetes.io/managed-by: badil`. Each `data` key is the first 32 hex characters of the SHA-256 of the PR's key (`<owner>/<repo>#<number>`, owner and repo lowercased); its value is `{"key", "pr", "sha", "at"}`: the PR's key, its URL, the head SHA it expired at and when.

- **Writers:** `badil serve`'s sweep, catch-up and deploys write it concurrently. Each write reads the ConfigMap again and changes only its own keys, retrying a conflicting write up to 3 times.
- **Cleared:** a deploy of the PR, by a label event, a push, catch-up or `/v1/deploy`, or a catch-up that lists the PR at another head SHA, removes its record.
- **Pruned:** each catch-up drops the records of PRs whose repo is no longer in `badil.yaml`, or whose repo it listed without them (the PR was closed or merged). Records of a repo whose listing failed are kept. A value that does not decode is dropped.

A failed read or write is logged as a warning and never stops a sweep, a catch-up or a deploy: a failed read lets catch-up reconcile the PR. Outside a cluster, or without `POD_NAMESPACE`, the records are kept in memory only, and badil logs `expiry persistence disabled` with `reason` at start.

## Spin-up time

### What is measured

Spin-up is `at − updated-at`: `at` is the time of the renderer's first `ready` for a pinned service at its pinned revision (a `revision` starting with `commitSha`), and `updated-at` the service's `metadata.updated-at` in the preview file, the time badil wrote the service to the gitops repo. It is recorded once per service and revision in a server process, when it is between 0 and 24h.

It runs from that write to the service synced and Healthy at that revision. It therefore includes the ApplicationSet poll and any wait for CI to push the image. Branch-tracking services and dependencies are never pinned and record no spin-up time.

### Health after the sync

The `on-status-ready` trigger waits for health computed after the sync. Either of these counts:

- a status refresh later than the sync's end: `status.reconciledAt` later than `status.operationState.finishedAt`;
- a health change after the sync's start: `status.health.lastTransitionTime` later than `status.operationState.startedAt`.

Argo CD records the sync's result before it refreshes health. Until then, health still describes the pods from before the sync, and the sample would be cut short. The sample can still come late or early ([limitations 16 and 17](#limitations)).

### Late samples

A `ready` can also come long after the write:

- for already-Healthy pinned Applications, the first time the configuration is applied;
- after a badil restart, the first `ready` at a revision records a sample again;
- a recovery at the same revision sends `ready` again (once per health transition), which records a sample when it is the first `ready` the process sees at that revision.

Each is recorded as the time since `updated-at`, up to the 24h bound, so it usually lands in `+Inf`. Read samples above the top bucket (1800 s) as backfill or retries, not real spin-ups.

## Preview deployments on GitHub

Argo CD's notifications controller posts GitHub Deployments when a PR's code runs in its preview or fails to, based on Argo CD's health status, through its own GitHub App (Deployments R/W, Metadata R). badil makes no Deployments calls.

Configuration: [`examples/argocd/github-deployments-values.yaml`](../examples/argocd/github-deployments-values.yaml), applied after `notifications-values.yaml`. Setup: INSTALL [Preview deployments on GitHub](INSTALL.md#preview-deployments-on-github-optional).

### Triggers

There are five triggers:

| Trigger | Applies to | Status | Once per |
|---|---|---|---|
| `on-preview-deployed` | pinned Applications | `success` | sync at the pinned SHA |
| `on-preview-degraded` | pinned Applications | `failure` | `targetRevision` |
| `on-preview-sync-error` | pinned Applications | `error` | `targetRevision` |
| `on-preview-deleted` | pinned Applications | `inactive` | |
| `on-preview-unpinned` | unpinned Applications with a pinned entry in `status.history` | `inactive` | pinned sync that badil unpins |

Pinned preview service Applications are those whose `preview.badil.dev/vcs.ref.revision` annotation is not empty. Branch-tracking services and dependencies have it empty. `on-preview-unpinned` applies when that annotation is empty and the Application's `status.history` holds a pinned entry.

`on-preview-degraded` and `on-preview-sync-error` test that annotation first. A dependency with `valueFiles` has `spec.sources` and no `spec.source`. Argo CD's `expr` fails on `app.spec.source.targetRevision` with `cannot fetch targetRevision from <nil>` instead of evaluating it as false, and `and` stops at the empty annotation before reading it.

`on-preview-unpinned` reads `status.history` nil-safely. A multi-source dependency's history entry carries `source: {repoURL: ""}` and no `revision`, and the pinned-entry test evaluates it as false.

### `on-preview-deployed`

It fires when the Application is synced and Healthy and `syncResult.revision` starts with the revision annotation. It posts status `success` on the service repo (`spec.source.repoURL`) at that revision.

It fires once per sync at the pinned SHA. Its `oncePer` key is `syncResult.revision` (the PR head SHA, since a pinned service's `targetRevision` is its commit SHA) joined with the `id` of the last `status.history` entry.

The notifications controller never clears a stored `oncePer` key. A per-SHA key would leave a service at `inactive` when it is unpinned and then pinned again at the same SHA (label removed and re-added, or a PR closed and reopened, with no new push). With the per-sync key, the re-pin's sync posts `success` again.

A pin or re-pin that runs no sync posts nothing (limitation 15). Any further successful sync at that SHA (selfHeal or a manual sync) also adds another `success` status on the same deployment.

The prefix guard waits for the sync at the pinned SHA. Right after a pin, `operationState` still describes the previous sync, and a service that tracked main would otherwise get `success` on main's commit. A prefix keeps a short SHA sent to `/v1/deploy` working.

It also waits for health computed after the sync, as [Health after the sync](#health-after-the-sync) describes for the spin-up sample. The health change can come from the new pods turning Healthy. Until the refresh, a service that was Healthy before the sync still shows Healthy while its new pods start (limitations 16 and 17).

The `environment_url` is built from the `preview.badil.dev/host` annotation, which `preview-apps` sets from the service's `host`. The deployment is created with `auto_merge: false` and `required_contexts: []`, so an older ref or a red commit still gets one.

### `on-preview-degraded`

It fires when the Application's health is `Degraded` while it is `Synced` at `spec.source.targetRevision` (the SHA badil last pinned), once per `targetRevision`. It posts status `failure`.

A Deployment turns Degraded only after its `progressDeadlineSeconds`; until then it is Progressing.

### `on-preview-sync-error`

It fires, once per `targetRevision`, in either of these cases:

- the sync operation at `targetRevision` ended in `Error` or `Failed` (`operationState.syncResult.revision` is that SHA);
- the sync status is `Unknown` for a comparison against that SHA (`sync.comparedTo.source.targetRevision`), as when the chart fails to render.

It posts status `error`. Its two tests are one condition, so it posts at most one `error` per SHA.

### Guards and refs

The guards on `targetRevision` keep the previous SHA's Degraded health or failed sync, still shown right after badil pins a new SHA, from posting on the new SHA's deployment.

`failure` and `error` use `spec.source.targetRevision` as the deployment's ref. For a synced pinned service, it is the same SHA as the `syncResult.revision` that `success` uses. All three land on the same deployment as successive statuses: the notifications controller reuses the first deployment it finds for that ref and environment, and creates one only when none exists.

### `on-preview-deleted`

It fires when the Application has a `deletionTimestamp`, and posts `inactive` on the deployment for `app.spec.source.targetRevision` (the SHA badil last pinned).

### `on-preview-unpinned`

It fires when badil unpins the service from a preview that survives: a path-filter unpin on a later push, or a PR closed while another PR keeps the preview. The annotation is then empty, and `status.history` still holds a pinned entry: a single-source entry whose `revision` is a full SHA and whose `source.targetRevision` is a SHA prefix of it.

It reads the last pinned SHA from that history (`findLast`) and posts `inactive` on that SHA's deployment. It fires once per pinned sync that badil unpins: its `oncePer` key is that entry's `revision` and `id`, so after a re-pin at the same SHA, the next unpin posts `inactive` again.

It fires as soon as the unpin reaches the Application, without waiting for the sync to the branch.

### Environments

The environment is `preview-<slug>-<service>`, one per preview service. A new `success` on it marks the prior `success` deployments `inactive` (GitHub's `auto_inactive`).

### Limitations

1. `inactive` on delete is best effort: the controller only sees the deletion if it processes the update before the Application's finalizer completes.

   It looks up the deployment by the SHA badil last pinned, so it closes that SHA's `success`, `failure` or `error` deployment. Only when that SHA never got a status (still Progressing at teardown) does it create an extra `inactive` deployment for it.

   An earlier SHA's `success` stays.
2. Failed notifications are not retried.
3. Applying the config posts `success` for every already-Healthy pinned Application, `failure` for every pinned Application already Degraded at its SHA, and `error` for every one whose sync already failed or is `Unknown` at its SHA, and `inactive` for every unpinned Application whose sync history still holds a pinned entry, as `oncePer` has no prior state.
4. Environments accumulate on the service repos and can be deleted by hand.
5. The URL is the host root; the expose path is not applied.
6. One App installation covers all service repos (`service.github` takes one `installationID`).
7. For a pinned service with no host (for example `postgres`, not in `expose`), `environment_url` is sent as `""`. Whether GitHub accepts that is not verified; the notifications controller log shows the result.
8. `on-preview-deployed` posts `success` again only on a new sync: a service that was Healthy (`success`), then Degraded (`failure`), then Healthy again at the same SHA without a sync keeps `failure`. A push (new SHA) or a new sync at the same SHA posts `success` again.
9. A rollout that never completes (image never pushed, crash loop) shows as `failure` only after the Deployment's `progressDeadlineSeconds` (600 s by default).
10. A transient `Unknown` sync status (for example a repo-server error) posts `error` once for that SHA; a later Healthy sync at that SHA posts `success` on the same deployment.
11. With a short `commit_sha` sent to `/v1/deploy`, `on-preview-degraded` and `on-preview-sync-error` never fire, because their guards compare `app.spec.source.targetRevision` with the resolved full SHA; nothing logs it.

    `on-preview-deleted` then looks up the deployment by the short SHA and misses the full-SHA `success` deployment: it leaves that deployment open and creates an extra `inactive` deployment for the short ref.

    The label flow always pins the full SHA.
12. The unpin status is lost when the pinned entry has left the sync history (Argo CD keeps the last 10 syncs) before the controller sees the unpin.
13. When the unpinned SHA's sync succeeded but it never got a status (still Progressing at the unpin), the unpin creates an extra deployment for that SHA holding only `inactive`, as limitation 1 describes for delete.
14. Argo CD records a sync in its history only when it succeeds. If the last pinned SHA's sync failed (its deployment shows `error` or `failure`) and badil then unpins the service, `on-preview-unpinned` posts `inactive` on the previous successfully synced pin's deployment, and the failed SHA's deployment stays open.
15. `on-preview-deployed` needs a sync at the pinned SHA, and Argo CD auto-syncs an Application only when it is OutOfSync. A pin whose rendered manifests equal what is deployed (a chart that ignores the image tag, with its chart path unchanged by the PR) runs no sync: the service runs the pinned SHA with no deployment status.

    A re-pin at the same SHA runs no sync either in two cases:

    - it reaches the Application before the sync to the branch that follows the unpin;
    - the unpin itself ran no sync.

    The last history `id` is then unchanged, its key is already stored, and the deployment stays `inactive`.
16. `success`, and the `ready` status and spin-up sample of `notifications-values.yaml`, can come late. Argo CD records times to the second, and a time equal to the sync's end or start counts as stale.

    A sync that changes no pod keeps health `Healthy` with no transition after its start, so it waits for a status refresh in a later second than its end; when the refresh that follows the sync lands in the same second, it waits for the next full refresh.

    A full refresh comes every `timeout.reconciliation` plus up to `timeout.reconciliation.jitter`. The wait is at worst about two periods plus jitter: about 5 minutes with the defaults (120 s and 60 s). It has no bound when `timeout.reconciliation` is `0s`.

    A pin rarely leaves pods unchanged, since it changes the image tag.
17. `success`, and the `ready` status and spin-up sample, can come early: any health change after the sync's start counts, even one that does not come from the new pods.

    This happens in two cases:

    - the previous SHA's rollout turns Healthy while the next SHA's sync runs (pushes seconds apart);
    - the chart orders its resources in sync waves. Argo CD applies a wave only once the earlier waves are Healthy, and marks the sync as succeeded as soon as the last wave is applied. An earlier wave turning Healthy (a migration Job, another Deployment) is then a health change after the start, while the last wave's pods still start.

    Either way, `success`, `ready` and the spin-up sample for the new SHA can come before its pods are Healthy, and are not sent again when they turn Healthy.

## Namespace guard

With the [namespace guard example](../examples/namespace-guard/README.md), the renderer cannot adopt or delete a namespace that does not carry the preview label. A preview whose namespace already exists without that label, whoever wrote its preview file, is refused by the cluster. With Argo CD as the renderer:

- the preview's parent Application stays `OutOfSync`, and its operation is `Failed` after 5 retries (about 5 minutes) with `one or more objects failed to apply, reason: error when patching ...: namespaces "<slug>" is forbidden: ValidatingAdmissionPolicy ... denied request: namespace is not a badil preview`;
- its Namespace resource is `SyncFailed`; the per-preview AppProject is created, but no child Application and nothing inside the namespace;
- no GitHub deployment is posted;
- the namespace keeps its uid, labels and annotations.

Closing the PR deletes the preview and its parent Application; the namespace and what it holds stay. A preview whose namespace the application controller creates is not affected.

## Dashboard

[`charts/badil/dashboards/badil.json`](../charts/badil/dashboards/badil.json) is a Grafana dashboard (uid `badil`) over these metrics. Its panels show:

- live previews;
- deploys and teardowns per hour;
- spin-up p50/p90, and p90 by service;
- zombies swept;
- reconcile skips by reason;
- webhook deliveries by status;
- gitops operation p90;
- the last successful sweep.

It picks its Prometheus data source through a `datasource` variable.

With `dashboard.enabled`, the chart renders it as a ConfigMap labelled `dashboard.labels` (default `grafana_dashboard: "1"`), which the Grafana sidecar of kube-prometheus-stack loads. The JSON also imports on its own.

Last successful sweep needs `cleanup.interval`, and the spin-up panels need the [status callback](#preview-status).

## Demo setup

The demo runs on these repos:

- demo services: [demo-backend-1](https://github.com/badilhq/demo-backend-1), [demo-backend-2](https://github.com/badilhq/demo-backend-2), [demo-front](https://github.com/badilhq/demo-front);
- gitops repo: [demo-gitops](https://github.com/badilhq/demo-gitops).

Local dev of badil itself: [DEVELOPMENT.md](../DEVELOPMENT.md).
