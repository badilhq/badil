# Architecture

Self-hosted, single-tenant. Everything runs in the user's cluster; badil talks to GitHub through the user's own GitHub App and never touches the cluster directly: ArgoCD does the deploys.

```
 service repo CI ── builds + pushes the image (no deploy call)

 manual trigger ──► POST /v1/deploy ──────────────────────────────────────────────────────────┐
 GitHub ── pull_request (PR label or push) ──► POST /webhooks/github ── async reconcile ──► badil serve ──► gitops repo
   ▲                                                                                          │ (GitHub App)   previews/<slug>/values.yaml
   └──────────────── PR comment (fan-out) ◄───────────────────────────────────────────────────┘                  ▼
                                     ArgoCD ApplicationSet (git dir generator: previews/*)
                                       └─ preview-env chart → AppProject + Namespace + one Application per service
                                            └─ service chart (repoURL of the entry, path per serviceCharts of preview-env.yaml, default k8s/) at commitSha or the entry's branch

 label removed or pull_request closed ──► badil serve (async) ── unpins the PR's services, or deletes previews/<slug>/values.yaml ──► ArgoCD prunes
 badil cleanup (CronJob) ── deletes stale previews/<slug>/, unpins closed PRs, and open PRs whose label moved, conflicts or is gone ──► ArgoCD prunes
```

## Components

| Component | Where | Notes |
|---|---|---|
| `badil serve` | this repo | chi HTTP server: `/healthz`, `/v1/deploy`, `/webhooks/github`, `/webhooks/argocd` (optional) |
| `badil cleanup` | this repo | One-shot, CronJob (hourly). Per preview, the first matching rule: no pinned service → delete; `CLEANUP_TTL` set and no deploy within it → delete; otherwise unpin closed PRs, and open PRs whose label moved, conflicts or is gone (see [Cleanup sweep](#cleanup-sweep)) |
| `badil bootstrap-app` / `verify-app` | this repo | Create a GitHub App via the manifest flow / check credentials |
| `preview-env` chart | this repo, [`charts/preview-env`](../charts/preview-env) | Renders AppProject, Namespace, one ArgoCD Application per service; the AppProject and Applications in the namespace set by `argocdNamespace` (default `argocd`); Application annotations `preview.badil.dev/*` carry the PR metadata |
| ApplicationSet | this repo, [`examples/argocd`](../examples/argocd) | Git directory generator on `previews/*` |
| Service chart | each service repo, at `k8s/` unless `serviceCharts` (in `preview-env.yaml`) sets `path` (uses the `app` chart, [`charts/app`](../charts/app)) | Image tag = `commitSha` or `$ARGOCD_APP_REVISION` |
| `badil` chart | this repo, [`charts/badil`](../charts/badil) | Installs `badil serve` (Deployment, Service, optional Ingress) and the cleanup CronJob (off by default) |

## Gitops repo contract

```
badil.yaml                       # read by badil only: services { <service>: { repo, branch } }, optional preview block
preview-env.yaml                 # read by preview-env only (an ApplicationSet value file): optional serviceCharts and previewNamespace
previews/<slug>/values.yaml      # written by badil, read by preview-env: apiVersion badil.dev/v1, self-contained per service
```

Each file has one reader: badil never parses chart settings, and the chart never sees badil's settings. Both live at the repo root, at fixed paths.

```yaml
# badil.yaml
services:                       # required, non-empty: the service catalog, in catalog order
  backend-1:
    repo: https://github.com/acme/backend-1.git   # required, non-empty string
    branch: master              # optional, default "main": the branch badil writes into each preview entry, and the PR-comment badge
  front:
    repo: https://github.com/acme/front.git
preview:                        # optional: preview label, preview cap and per-preview URLs (URLs off without domain)
  label: preview                # optional, default "preview": the PR label that creates the preview
  maxPreviews: 10               # optional, default no cap: deploys that would create a preview past it are refused
  domain: 127.0.0.1.nip.io      # lowercase bare domain: no scheme, no path, no leading or trailing dot
  tls: false                    # optional, default true: https or http in the PR-comment links only
  expose:                       # services that get a host and a link
    front: {}                   # entry value: empty, {} or {path: ...}
    backend-1:
      path: /docs               # optional, default "/": PR-comment link only
```

badil reads `badil.yaml` on every deploy and reconcile. An unknown or repeated key at any level is rejected, as is a `services` entry that is not a mapping. A malformed file fails `/v1/deploy` and makes every reconcile log `preview reconcile failed`, with an error prefixed `badil.yaml:` naming the offending key; a missing file fails the same way (`gitops: not found`). An `expose` entry for a service missing from `services` is ignored.

- `services.<name>.repo` and `services.<name>.branch` accept any YAML string (`branch: yes` is the branch `yes`: `badil.yaml` never reaches Helm) and reject a number, a boolean, a null, a mapping or a sequence. A PR's repo maps to the services whose `repo` is `https://github.com/<owner>/<repo>`, compared case-insensitively, with or without a trailing `/` or `.git`. Other URL forms (SSH, GitHub Enterprise) never match, so the label flow never deploys those services.
- `preview.label` must be a non-empty string; it is matched case-insensitively, like GitHub label names. `<label>:<name>` labels name the preview (see the label flow below).
- `preview.maxPreviews` must be a YAML integer of at least 1; a string (`"3"`), `0`, a negative number or a float is rejected, and a null or empty value means unset. It does not need `preview.domain`. See Preview cap in the [HTTP API](#http-api) section.

```yaml
# preview-env.yaml
serviceCharts:                  # optional: per-service chart settings, every key optional
  backend-1:
    path: deploy/helm           # default "k8s/": chart path in the service repo
    imageTagParam: image.tag    # default "app.image.tag": Helm parameter set to the image tag
    hostParam: ingress.hostname # default "app.ingress.host": Helm parameter set to the preview host
    valueFiles:                 # default none: extra values files, applied in order
      - values-preview.yaml
previewNamespace:               # optional: extra metadata of every preview Namespace
  labels:                       # string values; a label the chart sets is refused
    pod-security.kubernetes.io/enforce: baseline
  annotations: {}               # string values; argocd.argoproj.io/sync-wave is refused
```

The ApplicationSet passes `preview-env.yaml`, then the preview file, as value files of `preview-env`, and its `argocdNamespace` as a parameter. The file is required by the example ApplicationSet (Argo CD fails the render on a missing value file); with no setting it holds `{}`. The chart's schema validates the merged values on every render (Argo CD shows a sync error); its root is closed, so any other root key (a leftover `serviceRepos` or `preview`) fails the render with `at '': additional properties '<key>' not allowed`.

- `previewNamespace.labels` and `previewNamespace.annotations` are added to every preview Namespace, after the chart's own (`app.kubernetes.io/name`, `app.kubernetes.io/instance`, `app.kubernetes.io/part-of`, `app.kubernetes.io/managed-by`, and the annotation `argocd.argoproj.io/sync-wave`). The chart's schema rejects the block when it, `labels` or `annotations` is not a map (null included), a value is not a string (quote YAML 1.1 booleans such as `yes`, `no`, `on`, `off`), a key is one the chart sets, or the block has another key.
- `serviceCharts` entries reject unknown fields, `branch` included (a service's branch is `services.<name>.branch` in `badil.yaml`). A null entry or a null field is rejected; omit the entry or the field to use the defaults. `path`, `imageTagParam` and `hostParam` must be non-empty strings, and `valueFiles` a list of non-empty strings; unquoted YAML 1.1 booleans (`yes`, `no`, `on`, `off`, `y`, `n`) must be quoted. An entry for a service not in the preview is ignored.

```yaml
# previews/feature-pricing/values.yaml
apiVersion: badil.dev/v1        # required, always badil.dev/v1
services:                       # required: one entry per catalog service, in catalog order
  - name: backend-1             # required
    repoURL: https://github.com/acme/backend-1.git   # required: services.<service>.repo of badil.yaml, as written
    branch: master              # required: services.<service>.branch of badil.yaml, else main
    commitSha: "<sha>"          # optional: pinned at this commit (targetRevision and image tag)
    host: backend-1-feature-pricing.127.0.0.1.nip.io   # optional: ingress host of an exposed service
    metadata:                   # optional, present exactly when commitSha is: all seven keys required
      pr-author: octocat
      pr-url: https://github.com/acme/backend-1/pull/42
      pr-number: "42"
      created-at: "2026-05-26T10:00:00Z"
      updated-at: "2026-05-26T10:00:00Z"
      vcs.ref.name: feature/pricing
      cicd.pipeline.run.url: ""   # "" for label deploys
  - name: front                 # no commitSha: tracks its branch
    repoURL: https://github.com/acme/front.git
    branch: main
```

The file is self-contained: a renderer needs only it to know each service's repo, revision and image tag. Contract v1:

- **Strict.** Every object is closed: an unknown field at any level of an entry or of `metadata` is rejected. `apiVersion` must be `badil.dev/v1`. Every field has the type shown; a null value is rejected, as is a pinned entry without `metadata` or `metadata` without `commitSha`.
- **Written on every deploy.** Each deploy write sets `apiVersion`, and every entry's `repoURL` and `branch` from the catalog as it is then: a branch change in `badil.yaml` is written on the next deploy, even for an unchanged SHA. An entry whose service is no longer in `services` is dropped, pinned or not; Argo CD prunes its Application. Teardowns (unpin, sweep) only clear `commitSha` and `metadata` and keep the rest as written.
- **Validated by the chart.** `preview-env` ships `values.schema.json`, generated from badil's Go types (`make schema`), and Helm validates the merged values (`preview-env.yaml`, the preview file and `argocdNamespace`) against it on every render; a violation fails the render with the field's JSON pointer, for example `at '': missing property 'apiVersion'`. CI fails when the schema is out of date. The root of the merged values is closed: a key other than `apiVersion`, `services`, `serviceCharts`, `previewNamespace` and `argocdNamespace` fails the render.

Upgrade (badil and `preview-env` 0.9.0), from a gitops repo with `services.yaml`; a new badil fails every deploy and reconcile without `badil.yaml`, and `preview-env` 0.9.0 rejects `serviceRepos`, `preview` and `serviceCharts.<service>.branch` in its values:

1. Commit `badil.yaml` and `preview-env.yaml` at the root of the gitops repo, and keep `services.yaml`, which the running badil and the deployed ApplicationSet still read: each `serviceRepos.<service>` URL becomes `services.<service>.repo`, each `serviceCharts.<service>.branch` becomes `services.<service>.branch`, and the `preview` block moves as is to `badil.yaml`; `serviceCharts` (without `branch`) and `previewNamespace` move to `preview-env.yaml` (`{}` when neither is set).
2. Upgrade badil.
3. Rewrite every live preview (push to its PR, or remove and re-add the label; the cleanup sweep deletes previews no PR pins any more): a preview file written by an older badil has no `apiVersion`, so `preview-env` 0.9.0 refuses to render it, and shows `missing property 'apiVersion'` in Argo CD until it is rewritten.
4. Bump the chart: set the ApplicationSet's `targetRevision` to 0.9.0 and its first value file to `$values/preview-env.yaml`, as in [`examples/argocd/applicationset-previews.yaml`](../examples/argocd/applicationset-previews.yaml), and re-apply it (`kubectl apply -f applicationset-previews.yaml`): Argo CD does not manage the ApplicationSet.
5. Delete `services.yaml` from the gitops repo: nothing reads it any more.

## Per-preview URLs

Each exposed service of a preview gets the host `<service>-<slug>.<domain>` (the `<service>-<slug>` label capped at 63 characters, see below). One wildcard DNS record (and, for TLS, one wildcard certificate) for `*.<domain>` covers every preview.

- badil writes the host into each exposed entry's `host` in `values.yaml`, and `preview-env` passes it to the service's Application as the Helm parameter `app.ingress.host` (or the service's `hostParam`).
- badil appends ` · [<host><path>](<scheme>://<host><path>)` to the service's line in the PR comment.

`tls` only picks the link scheme; ingress TLS, the ingress class and annotations stay in the service chart. Services not in `expose` (non-HTTP services, for example) get no host and no link.

Length caps: a slug longer than 53 characters (Helm's release-name limit, which Argo CD hits through the ApplicationSet's `releaseName`) and a `<service>-<slug>` host label longer than 63 characters (the DNS label limit) are cut, and a hash of the full value (6 hex characters) is appended. For example, the branch `feature/very-long-branch-name-that-keeps-going-and-going-past-the-helm-limit` gets the slug `feature-very-long-branch-name-that-keeps-going-5bec34`, and the service `front-office-web` in it gets the host `front-office-web-feature-very-long-branch-name-that-keep-395ff3.<domain>`.

Upgrade order: upgrade badil (0.10.0 or later) first, then `preview-env` (0.3.0 or later). `preview-env` 0.3.0 reads only `host`, so an entry without it gets no ingress host. Preview files written by an older badil carry no `host` until their next deploy write.

## Preview limits

badil and the `preview-env` chart create no quota or policy objects: per-preview limits belong to the cluster's policy layer, which selects preview namespaces by the label `app.kubernetes.io/part-of: preview-environments` or by labels set in `previewNamespace`. [`examples/kyverno/preview-guardrails.yaml`](../examples/kyverno/preview-guardrails.yaml) is one Kyverno `GeneratingPolicy` (Kyverno v1.19.0 or later) that creates a ResourceQuota, a LimitRange and an ingress NetworkPolicy in every preview namespace, and keeps them in sync with the policy. Kyverno creates them, not Argo CD, so the `previews` AppProject is unchanged; teardown deletes them with the namespace.

A quota on compute resources (`requests.cpu`, `requests.memory`, `limits.memory`) refuses pods that do not set them, and `charts/app` ships `resources: {}`: ship a LimitRange with default requests and limits next to such a quota, as the example does.

## Pinned and branch-tracking services

A preview runs each service in one of two modes, set by `commitSha` in `previews/<slug>/values.yaml`:

- **Pinned** (`commitSha` set): the Application uses `targetRevision: <sha>` and sets the image tag parameter (default `app.image.tag`) to `<sha>`. It changes only when badil writes a new SHA.
- **Branch-tracking** (no `commitSha`): the Application uses `targetRevision: <branch>`, the entry's `branch`, and sets the image tag parameter (default `app.image.tag`) to `$ARGOCD_APP_REVISION`, which Argo CD resolves to the commit it syncs. It follows new commits on that branch.

In both modes the Application's `repoURL` is the entry's `repoURL`, and the AppProject's `sourceRepos` are the distinct `repoURL`s of the file's entries.

A service can briefly sit in `ImagePullBackOff` because its image tag does not exist yet. A branch-tracking service hits it when Argo CD syncs a new commit on that branch before that commit's CI has pushed the image. A pinned service hits it because badil pins the PR head SHA as soon as the label or a push arrives, before CI has pushed that commit's image; the pod starts when the image is in the registry, and the PR comment says so. This is accepted because it resolves without intervention:

- The kubelet keeps retrying the pull, with a back-off capped at 300 seconds, and the pod starts once the image is in the registry.
- The Applications sync with `selfHeal: true`, and Argo CD polls Git every 3 minutes by default.
- Branch-tracking services are affected after a new commit on their branch; pinned services after the label or a push on their PR, until CI has pushed that commit's image.

Alternatives considered, and why they were not chosen:

- **badil re-pins branch-tracking services** to the branch's new SHA in every active preview once its image exists: removes the race, but every build of that branch has to rewrite every preview, with concurrent writes to the same files.
- **Argo CD webhook from CI** after the image push, instead of polling: needs CI-to-cluster network access and an exposed Argo CD API, and does not help while Argo CD also polls.
- **Argo CD Image Updater**: another controller with registry credentials and write-back config, built for tracking the latest tag rather than previews.
- **Init container that waits for the image**: adds registry access and a wait to every service chart, and still delays startup.
- **badil waits for CI checks or for the image in the registry before pinning**: needs an extra GitHub App permission (Checks) or registry credentials, delays the other services of the preview, which today start while CI builds, and gains no time (measured: CI 3m34s, pinned service Healthy at 216 s).

References: [Helm build environment in Argo CD](https://argo-cd.readthedocs.io/en/stable/user-guide/helm/), [Argo CD tracking strategies](https://argo-cd.readthedocs.io/en/latest/user-guide/tracking_strategies/).

## Service repo contract

The `preview-env` chart ([`application.yaml`](../charts/preview-env/templates/application.yaml)) renders one Argo CD Application per entry of the preview file, from the entry's `repoURL` and `branch`, and expects each service repo to provide:

- A Helm chart at `k8s/` by default, or at the service's `serviceCharts` `path`.
- A `main` branch by default, or the service's `branch` in `badil.yaml`: branch-tracking services use it as `targetRevision`.
- A Helm parameter `app.image.tag` by default (the service chart declares the [`app`](../charts/app) chart as a dependency aliased `app`), or the service's `serviceCharts` `imageTagParam`.
- Images tagged with the full git commit SHA: pinned services use the PR head SHA (label flow) or the `commit_sha` sent to `/v1/deploy`, branch-tracking services use `$ARGOCD_APP_REVISION`.
- For services in `preview.expose`: ingress enabled and a Helm parameter setting the ingress host, `app.ingress.host` by default or the service's `serviceCharts` `hostParam`. Otherwise the PR-comment link is dead.
- A service whose image is not built per commit (for example a database) may ignore the image tag parameter.
- Extra values files, from the service's `serviceCharts` `valueFiles`, applied in order. Argo CD joins a relative path to the chart path and a path starting with `/` to the repo root; a missing file fails the sync.

`hostParam` is passed as a Helm `--set`, key verbatim. A list-indexed name such as `ingress.hosts[0].host` replaces the chart's default list, so the full list (with its `paths`) and `ingress.enabled: true` must come from a file in `valueFiles`; the parameter then only sets that element's field.

Override them per service in `serviceCharts` of `preview-env.yaml` (the branch in `badil.yaml`); image tags must still be the full commit SHA.

The ApplicationSet example sources `preview-env` from an OCI registry (`oci://` with `path: "."`), which requires Argo CD 3.1 or later.

## Preview data

badil never reads, writes or drops data stores.

- A preview's identity is its slug: both the Helm release name of `preview-env` and the preview namespace.
- Each service's release name is the service name, so per-preview data is keyed by namespace.
- Data that lives in the preview namespace is pruned with it on teardown, sweep or expiry.
- Data outside the namespace is owned, and dropped, by whatever provisions it.
- The [`app`](../charts/app) chart only offers `envFrom` and `initContainers` to wire a data provider.

## HTTP API

```
GET  /healthz

GET  /metrics               # on METRICS_ADDR only (default :9090), not on LISTEN_ADDR

POST /v1/deploy
  Authorization: Bearer $BADIL_DEPLOY_TOKEN
  { "service", "head_ref", "commit_sha", "pr_author", "pr_url", "pr_number", "workflow_run_url" }
  → { "preview_slug", "gitops_commit_url", "warnings": [...] }
  → 409 { "error": "preview limit (<max>) reached" }   # preview.maxPreviews reached and previews/<slug>/ does not exist

POST /webhooks/github
  X-Hub-Signature-256 verified (401 on mismatch)
  installation ≠ GITHUB_INSTALLATION_ID (ping exempt) → 403
  pull_request opened/reopened/synchronize/labeled/unlabeled/closed → async reconcile; other actions → log only
  other events → structured log only
  parse failures / handler panics → logged, 200

POST /webhooks/argocd       # only when ARGOCD_WEBHOOK_TOKEN is set (404 otherwise)
  Authorization: Bearer $ARGOCD_WEBHOOK_TOKEN (401 on mismatch)
  { "app", "service", "preview", "revision", "updatedAt" }   # at most 64 KiB; invalid → 400
  → 204, recorded or ignored (see Spin-up endpoint)
```

Manual deploy flow (`/v1/deploy`): validate `service` against the `services` of `badil.yaml` → slugify `head_ref` → refuse a new preview past the preview cap (below) with 409 → read or bootstrap `previews/<slug>/values.yaml` → write with SHA optimistic locking + 409 retry → PR comment fan-out (best-effort, failures returned as `warnings`). It always writes, even when only `updated-at` changes, so a repeated deploy of the same commit refreshes the TTL clock.

Label flow: CI only builds and pushes images; the `preview` label (`preview.label`) on a PR decides whether it has a preview, and in which one. badil answers `opened`, `reopened`, `synchronize`, `labeled`, `unlabeled` and `closed` deliveries 200 at once and reconciles the PR in the background:

1. A `labeled` or `unlabeled` delivery for another label, or an `opened`, `reopened` or `synchronize` delivery whose payload lacks a preview label, is skipped without calling GitHub. The preview labels are the plain label and the named labels below with a non-empty slug.
2. A repo not in the catalog is skipped with a log. Neither step applies to `closed`.
3. Otherwise badil re-reads the PR from GitHub and acts on its current state, labels and head SHA, never the payload's, so late or out-of-order deliveries, `closed` included, converge on the current truth: a `closed` delivery running after the PR was reopened and labeled deploys it. Fork PRs (head repo different from the base repo) are skipped: CI can't push a fork's image; a closed fork PR is still torn down.
4. The PR's labels resolve its target preview (see Target preview below).
5. The PR is torn down from every other preview it may still be pinned in (the cleanup set below), before anything is deployed.
6. An open PR with a target deploys every catalog service of its repo, in catalog order, at the PR's head SHA, to the target slug, through the manual flow's write and comment fan-out, unless another PR holds one of its services there (see One PR per service below), or the target does not exist and the preview cap is reached (see Preview cap below). A failed service is logged and the next one still deploys. A service whose write would change nothing but its `updated-at` (same SHA, host and PR metadata, on the first read and after a 409 re-fetch) writes no commit and posts no comment, and logs `preview deploy unchanged`, unless step 5 removed the PR from another preview; when every service is unchanged, the reconcile is skipped with reason `already deployed`. A PR comment that failed to post, or that a failed teardown left stale, is therefore not repaired by a later label event at the same head SHA: the next push to the PR or a manual `/v1/deploy` repairs it. The deploy fan-out is the last write to the PR's comment, so a PR that moved shows its new preview.
7. An open PR with conflicting named labels gets no preview. Its comment is replaced last by:

   ```
   ## ⚠️ Preview: conflicting labels

   Keep one `preview:` label. This PR has `preview:a`, `preview:b`.
   ```

Target preview: a label is **named** when it starts with `<label>:` (case-insensitively, for example `preview:pricing` or `Preview:Pricing`). Its name is the rest, trimmed of spaces, and its slug is the slugified name. A named label whose slug is empty (`preview:`, `preview:--`) is ignored and logged (`preview label ignored`). The **plain** label is `<label>` itself. `preview-x` and `previewx:y` are neither.

- Exactly one distinct named slug: the target is that slug. A named label wins over the plain label. Two named labels with the same slug count as one.
- Two or more distinct named slugs: a conflict, with no target.
- No valid named label, and the plain label: the target is the slugified head branch.
- Otherwise: no target.

Several PRs, from different repos and branches, labeled `preview:pricing` share the preview `pricing`: each pins its own services, and the rest run at their branch (default `main`). A named preview and a branch preview with the same slug are the same preview: `preview:main` joins the preview of a branch called `main`. badil never creates labels: labels are per repo, so `preview:<name>` must be created in each repo.

The **cleanup set** of a reconcile is, deduplicated, without empty slugs, and without the target when the PR is deployed there: the slugified head branch, every named slug, and, on `unlabeled`, the removed label's slug (the head branch's for the plain label). Each is torn down with one reason, the first that applies:

1. `PR closed`, when the PR is closed;
2. ``label `<removed label>` removed``, on `unlabeled`;
3. ``conflicting `<label>:` labels``, on a conflict;
4. ``moved to preview `<target>` ``, when the PR is deployed elsewhere;
5. ``label `<label>` removed`` otherwise.

A slug where the PR has no pin costs one read and is otherwise a no-op, with no comment.

A renamed or deleted label triggers nothing until the PR's next event or the next sweep, which unpins without redeploying. In that missed-event case the PR's comment can stay stale until its next event.

One PR per service: a preview holds one PR per service. Before deploying, a reconcile reads the target's `values.yaml` (a missing file means no holder). An entry for one of the PR's services is held when it is pinned (`commitSha` set), has metadata, and its `pr-url` names another PR. Any held entry refuses the whole PR, for every target, branch or named, since two PRs can also share a head branch. The first held service is reported in the PR's comment:

```
## ⚠️ Preview: `<slug>`

`<service>` is already pinned in this preview by <holder PR URL>. Once that pull request leaves this preview, push to this pull request or re-add its label. If that pull request is closed, or now carries a different `<label>:` label, it is released within the next cleanup run.
```

and the reconcile logs `preview reconcile skipped` with reason `service held by another pr` and a `holder` attribute. A read error other than 404 logs `preview reconcile failed` and deploys nothing. The holder PR is never read: a holder left by a missed event is released by the sweeper. A refused PR is not retried when the holder leaves, and since the cleanup runs first, it has already left its previous preview, as its labels ask.

The check assumes `replicaCount: 1` (`charts/badil/values.yaml`): reconciles are serialized within the replica, so no other reconcile writes between the read and the write. `/v1/deploy` runs outside that queue and skips the check: it stays last-writer-wins and can overwrite a holder's pin.

Preview cap: `preview.maxPreviews` caps the number of previews: the directories under `previews/`, listed when a deploy would create a new one. A deploy into an existing preview is never refused, and without the key there is no cap.

- Label flow: after the holder check, when the target's `values.yaml` does not exist (that same read, not a second one), the reconcile lists the previews. When there are already `maxPreviews` or more, it writes nothing, replaces the PR's comment with

  ```
  ## ⚠️ Preview: `<slug>`

  The preview limit (<max>) is reached. Once a preview is torn down, push to this pull request or re-add its label.
  ```

  and logs `preview reconcile skipped` with reason `preview limit reached` and a `max_previews` attribute. A list error logs `preview reconcile failed` and deploys nothing. A refused PR is not retried when a slot frees, and since the cleanup runs first, it has already left its previous preview.
- `/v1/deploy`: after the catalog check, it reads `previews/<slug>/values.yaml`; when it does not exist, it lists the previews, and at the cap answers `409 {"error": "preview limit (<max>) reached"}`, writes nothing and logs `deploy failed` with `op` `preview limit`. Other read or list errors are answered like the write's (`op` `read preview` or `list previews`).

The cap is soft: the check and the write are not atomic, and `/v1/deploy` runs outside the reconcile queue, so a concurrent manual deploy and reconcile can both pass the check and each exceed the cap by one. The check costs one extra GitHub call, only when the cap is set and the target does not exist.

badil does not check the registry: an image not pushed yet leaves the pod in `ImagePullBackOff` until CI pushes it (kubelet back-off capped at 300s).

Background reconciles run one at a time, in no guaranteed order; a reconcile reads the PR only once it runs. A reconcile has a 20s budget, counted from when it starts running and shared by its cleanup teardowns, the holder check, the preview cap check, then its services; a service not reached in time is logged as failed. Shutdown waits up to 25s for queued and running jobs; jobs still queued then are dropped, and the cleanup sweeper catches missed teardowns of closed PRs.

A labeled PR with no push for longer than `CLEANUP_TTL` is expired by the sweeper and not recreated while the label stays: push, or remove and re-add the label, to recreate it. A missed `unlabeled` of a named label, or a label that moved, is unpinned by the sweeper; a missed `unlabeled` of the plain label at the branch slug still waits for close or TTL.

Teardown flow: a reconcile tears the PR down from each slug of its cleanup set: the PR is closed (merged or not), lost its label (for example after `unlabeled`), has conflicting labels, or moved to another preview. A `closed` delivery whose reconcile can't read the catalog or the PR falls back to tearing down, as closed, the payload's head branch and the payload's named labels, logged as a warning, so a close still tears down when GitHub reads fail. For each slug (the slugified head branch or named label), badil reads `previews/<slug>/values.yaml` and unpins every service whose `pr-url` points to the PR: the entry goes back to a bare branch-tracking `{name}`. If other pins remain, the file is updated and the remaining PRs' comments are refreshed; otherwise the file is deleted. Both writes use SHA optimistic locking + 409 retry, deciding update vs delete again on every retry. The PR's comment is edited (or created) to say what was removed and why: `PR closed`, ``label `<label>` removed``, ``label `<label>:<name>` removed``, ``conflicting `<label>:` labels`` or ``moved to preview `<slug>` ``.

Deleting the file drops `previews/<slug>/` from the git directory generator, and the ApplicationSet deletes the preview's Application and its resources. This requires the ApplicationSet controller policy `sync` (the default) or `create-delete`; with `create-only` or `create-update` a deleted preview keeps running. Closes missed while badil was down, and pins written by a deploy after the PR closed, are caught by the cleanup sweeper. A labeled PR whose branch was renamed after the deploy is unpinned from its old slug by the sweeper, without a comment; push to redeploy it at the new slug.

## Cleanup sweep

The sweep runs in the server every `CLEANUP_INTERVAL` when it is set, starting one interval after startup; runs never overlap and shutdown cancels a run in progress. Otherwise `badil cleanup` runs it once per CronJob schedule (hourly by default). Use one or the other: both run the same idempotent sweep, so running both only wastes work. Only the in-server sweep exports the sweep metrics. The sweep lists `previews/*` and applies to each preview the first matching rule:

1. **No pinned service**: the file is deleted (`chore(preview): delete <slug> preview`). No comment.
2. **Expired** (only when `CLEANUP_TTL` is set): the newest `updated-at` of the pinned services is older than `CLEANUP_TTL`. The file is deleted (`chore(preview): expire <slug> preview`) and its open PRs get a comment saying the preview expired. A pinned service without a parseable `updated-at` never counts, so such a preview never expires.
3. **Closed or moved PRs**: every distinct PR pinning a service is looked up on GitHub. A PR whose state can't be read (404, 403, any error) keeps its pins and is logged as a warning.
   - A closed (or merged) PR goes through the same teardown as a `pull_request` `closed` delivery: unpin, update or delete, comments.
   - An open PR's labels are resolved to its target preview (see the label flow). It is unpinned from this preview, in this order, when its labels conflict (``conflicting `<label>:` labels``), or target another preview (``moved to preview `<target>` ``): both without touching its comment, which shows where it lives now. When it has no target and this preview is not its branch slug, it goes through the teardown, and its comment says ``label `<label>` removed``. Any other open PR is left alone, including an unlabeled PR pinned at its branch slug.

   This catches missed `unlabeled` deliveries, conflicts, and renamed or deleted labels. The catalog is read once per sweep for the label; when it can't be read, `cleanup catalog unavailable` is logged as a warning, it is not counted as a failure, and only closed PRs are torn down.

   This changes manual deploys: a `/v1/deploy` pin now survives the sweep only at the PR's target preview (its single `<label>:<name>` slug, or its branch slug when it has only the plain label), or at its branch slug when the PR has no target. Anywhere else it is unpinned (``moved to preview `<target>` `` or ``label `<label>` removed``); a PR carrying `<label>:a` loses a pin at its branch slug. A PR with conflicting named labels is unpinned from every preview (``conflicting `<label>:` labels``). A webhook reconcile of the PR also unpins it from its branch slug and its named-label slugs other than its target (`cleanupSlugs`). Such pins used to be left alone.

Rules 1 and 2 re-check their condition on the freshly read file, and again after every 409, so a deploy landing during the sweep cancels the delete. A failing preview read or rule 1–2 delete is logged and the sweep goes on; the command then exits 1 and the Job fails without retrying (`backoffLimit: 0`). A failing closed-PR teardown is only logged. The next scheduled run retries both. Comment fan-outs (deploy, teardown, expiry) skip closed PRs, so a closed PR keeps its teardown comment.

Known limit: the GitHub contents API lists at most 1,000 entries per directory, so the sweep sees at most 1,000 previews.

badil never touches data stores; see [Preview data](#preview-data).

## Configuration

```
GITHUB_APP_ID
GITHUB_PRIVATE_KEY_PATH | GITHUB_PRIVATE_KEY
GITHUB_WEBHOOK_SECRET
GITHUB_INSTALLATION_ID
GITOPS_REPO                 # owner/repo
BADIL_DEPLOY_TOKEN
LISTEN_ADDR                 # default :8080
METRICS_ADDR                # default :9090 when unset; set to "" to disable /metrics
ARGOCD_WEBHOOK_TOKEN        # optional; enables /webhooks/argocd
CLEANUP_INTERVAL            # serve, optional: Go duration of at least 1m (e.g. 1h); unset disables the in-server sweep
CLEANUP_TTL                 # cleanup, and serve when CLEANUP_INTERVAL is set, optional: Go duration (e.g. 168h); unset disables expiry
```

GitHub App permissions: Contents R/W, Pull requests W, Metadata R. Subscribed event: `pull_request` (installation events are delivered automatically).

## Metrics

`GET /metrics` serves Prometheus metrics on `METRICS_ADDR`, a listener separate from `LISTEN_ADDR`, so the Ingress never exposes it. The chart adds a `metrics` port to the Service and, with `metrics.serviceMonitor.enabled`, a ServiceMonitor.

### Metric list

Every label has a closed set of values. Go runtime (`go_*`) and process (`process_*`) metrics are exported too.

| Metric | Type | Labels | |
|---|---|---|---|
| `badil_webhook_deliveries_total` | counter | `event`, `status` | Webhook deliveries. `event` is `ping`, `installation`, `installation_repositories`, `pull_request` or `other` once the signature is verified, `unverified` before. `status` is the HTTP status: `200`, `401` or `403`. |
| `badil_reconciles_total` | counter | `outcome` | One per pull request reconcile: `failed` (catalog, PR or preview read error, preview list error, or no service write succeeded and at least one failed), else `skipped`, else `deployed` (a service write succeeded), else `torn_down`. |
| `badil_reconcile_skips_total` | counter | `reason` | Skipped reconciles by reason: `other label`, `not labeled`, `repo not in catalog`, `fork pr`, `service held by another pr`, `preview limit reached`, `conflicting preview labels`, `already deployed`. |
| `badil_teardowns_total` | counter | `trigger`, `result` | Teardowns of a PR from a preview. `trigger` is `webhook` or `sweep`; `result` is `deleted`, `unpinned`, `noop` (nothing pinned) or `error`. |
| `badil_gitops_operation_duration_seconds` | histogram | `op`, `result` | Duration of gitops `write` and `teardown` operations, 409 retries included; `result` is `ok` or `error`. |
| `badil_sweep_runs_total` | counter | `result` | In-server sweep runs, `ok` or `error`. A run cancelled by shutdown is not counted. |
| `badil_sweep_last_success_timestamp_seconds` | gauge | | Unix time of the last successful in-server sweep. |
| `badil_sweep_previews_deleted_total` | counter | `rule` | Previews deleted by the sweep: `no_pin` (rule 1) or `ttl` (rule 2). |
| `badil_previews` | gauge | | Previews in the gitops repo: listed at server startup, incremented when a deploy creates a preview, decremented when a teardown or sweep deletes one, and reset by each in-server sweep. |
| `badil_preview_spinup_seconds` | histogram | `catalog_service` | Spin-up time of a pinned service, from badil's gitops write to its Application synced and Healthy (see [Spin-up endpoint](#spin-up-endpoint)). Buckets: 15, 30, 60, 120, 180, 300, 600, 900, 1800 s. `catalog_service` is a catalog service; it is not named `service` because that collides with the `service` target label Prometheus adds when scraping through a ServiceMonitor. |
| `badil_argocd_events_total` | counter | `result` | Events accepted by `/webhooks/argocd`: `recorded` (a spin-up was observed) or `ignored`. 400 and 401 answers are not counted. |

`badil_sweep_last_success_timestamp_seconds` is always exported and reads 0 until the first in-server sweep: with the CronJob alone it stays at 0. `badil_previews` is set at server startup, from a listing taken before the server accepts requests and limited to 10 seconds; when that listing fails, the gauge stays as it is until the next in-server sweep or restart. Deletions made by the `badil cleanup` CronJob show only after the next server restart or in-server sweep.

### Spin-up endpoint

badil never reads the cluster, so Argo CD's notifications controller tells it when a preview service is up: it POSTs to `/webhooks/argocd` when a service's Application is synced and Healthy at a new revision, once per revision. Configuration: [`examples/argocd/notifications-values.yaml`](../examples/argocd/notifications-values.yaml).

- The route exists only when `ARGOCD_WEBHOOK_TOKEN` is set (chart: optional `argocd-webhook-token` key of `existingSecret`). Requests need `Authorization: Bearer $ARGOCD_WEBHOOK_TOKEN`, compared in constant time; a missing or wrong token gets 401. The public Ingress exposes the route too.
- The JSON body is at most 64 KiB; invalid JSON or a larger body gets 400.
- Spin-up is `now − updatedAt`, where `updatedAt` is the Application's `preview.badil.dev/updated-at` annotation: the time badil wrote the service to the gitops repo. It runs from that write to the Application synced and Healthy at that revision, so it includes the ApplicationSet poll and any wait for CI to push the image.
- An event is recorded when `service` is set and `updatedAt` is RFC 3339 and 0 to 24h old; any other valid event is ignored. Unpinned services carry no `updated-at`, so their events are ignored. Events are not checked against the catalog: the token is the gate.
- Late samples: the notifications controller also sends the trigger for already-Healthy pinned Applications the first time the configuration is applied, and re-sends it after a failed delivery (badil restarting, a 5xx). Each is recorded as the time since `updated-at`, up to the 24h bound, so it usually lands in `+Inf`. Read samples above the top bucket (1800 s) as backfill or retries, not real spin-ups.
- Every valid, authorized event gets 204, recorded or not, and logs `argocd event` with `app`, `service`, `preview`, `revision`, `result` and, when recorded, `spinup_seconds`.

### Preview deployments on GitHub

Argo CD, not badil, signals on GitHub that a PR's code is running in its preview, or failed to: health is Argo CD's call. Its notifications controller posts GitHub Deployments through its own GitHub App (Deployments R/W, Metadata R); badil makes no Deployments calls. Configuration: [`examples/argocd/github-deployments-values.yaml`](../examples/argocd/github-deployments-values.yaml), applied after `notifications-values.yaml` (setup: INSTALL [Preview deployments on GitHub](INSTALL.md#preview-deployments-on-github-optional)).

- All four triggers apply only to pinned preview service Applications: those whose `preview.badil.dev/vcs.ref.revision` annotation is not empty.
- `on-preview-deployed` fires when the Application is synced and Healthy, once per `syncResult.revision` (the PR head SHA, since a pinned service's `targetRevision` is its commit SHA). It posts status `success` on the service repo (`spec.source.repoURL`) at that revision, with `environment_url` built from the `preview.badil.dev/host` annotation, which `preview-env` 0.6.0 sets from the service's `host`. The deployment is created with `auto_merge: false` and `required_contexts: []`, so an older ref or a red commit still gets one.
- `on-preview-degraded` fires when the Application's health is `Degraded` while it is `Synced` at `spec.source.targetRevision` (the SHA badil last pinned), once per `targetRevision`. It posts status `failure`. A Deployment turns Degraded only after its `progressDeadlineSeconds`; until then it is Progressing.
- `on-preview-sync-error` fires, once per `targetRevision`, when the sync operation at `targetRevision` ended in `Error` or `Failed` (`operationState.syncResult.revision` is that SHA), or the sync status is `Unknown` for a comparison against that SHA (`sync.comparedTo.source.targetRevision`), as when the chart fails to render. It posts status `error`. Its two tests are one condition, so it posts at most one `error` per SHA.
- The guards on `targetRevision` keep the previous SHA's Degraded health or failed sync, still shown right after badil pins a new SHA, from posting on the new SHA's deployment.
- `failure` and `error` use `spec.source.targetRevision` as the deployment's ref. For a synced pinned service it is the same SHA as the `syncResult.revision` that `success` uses, so all three land on the same deployment as successive statuses: the notifications controller reuses the first deployment it finds for that ref and environment, and creates one only when none exists.
- `on-preview-deleted` fires when the Application has a `deletionTimestamp` and posts `inactive` on the deployment for `app.spec.source.targetRevision` (the SHA badil last pinned).
- The environment is `preview-<slug>-<service>`, one per preview service. A new `success` on it marks the prior `success` deployments `inactive` (GitHub's `auto_inactive`).

Limitations:

1. `inactive` on delete is best effort: the controller only sees the deletion if it processes the update before the Application's finalizer completes. It looks up the deployment by the SHA badil last pinned, so it closes that SHA's `success`, `failure` or `error` deployment; only when that SHA never got a status (still Progressing at teardown) does it create an extra `inactive` deployment for it. An earlier SHA's `success` stays.
2. A PR that leaves a surviving preview keeps its last `success`, because its Application is updated, not deleted.
3. Failed notifications are not retried.
4. Applying the config posts `success` for every already-Healthy pinned Application, `failure` for every pinned Application already Degraded at its SHA, and `error` for every one whose sync already failed or is `Unknown` at its SHA, as `oncePer` has no prior state.
5. Environments accumulate on the service repos and can be deleted by hand.
6. The URL is the host root; the expose path is not applied.
7. One App installation covers all service repos (`service.github` takes one `installationID`).
8. For a pinned service with no host (for example `postgres`, not in `expose`), `environment_url` is sent as `""`. Whether GitHub accepts that is not verified; the notifications controller log shows the result.
9. `oncePer` is per pinned SHA: a service that was Healthy (`success`), then Degraded (`failure`), then Healthy again at the same SHA keeps `failure`, because `on-preview-deployed` already fired for that revision. A push (new SHA) resets it.
10. A rollout that never completes (image never pushed, crash loop) shows as `failure` only after the Deployment's `progressDeadlineSeconds` (600 s by default).
11. A transient `Unknown` sync status (for example a repo-server error) posts `error` once for that SHA; a later Healthy sync at a SHA where `on-preview-deployed` had not fired yet posts `success` on the same deployment.
12. With a short `commit_sha` sent to `/v1/deploy`, `on-preview-degraded` and `on-preview-sync-error` never fire, because their guards compare `app.spec.source.targetRevision` with the resolved full SHA; nothing logs it. `on-preview-deleted` then looks up the deployment by the short SHA and misses the full-SHA `success` deployment: it leaves that deployment open and creates an extra `inactive` deployment for the short ref. The label flow always pins the full SHA.

### Dashboard

[`charts/badil/dashboards/badil.json`](../charts/badil/dashboards/badil.json) is a Grafana dashboard (uid `badil`) over these metrics: live previews, deploys and teardowns per hour, spin-up p50/p90 and p90 by service, zombies swept, reconcile skips by reason, webhook deliveries by status, gitops operation p90 and the last successful sweep. It picks its Prometheus data source through a `datasource` variable. With `dashboard.enabled`, the chart renders it as a ConfigMap labelled `dashboard.labels` (default `grafana_dashboard: "1"`), which the Grafana sidecar of kube-prometheus-stack loads; the JSON also imports on its own. Last successful sweep needs `cleanup.interval`, and the spin-up panels need the Argo CD notifications callback.

## Demo setup

Demo services: [demo-backend-1](https://github.com/badilhq/demo-backend-1), [demo-backend-2](https://github.com/badilhq/demo-backend-2), [demo-front](https://github.com/badilhq/demo-front). Gitops repo: [demo-gitops](https://github.com/badilhq/demo-gitops). Local dev of badil itself: [DEVELOPMENT.md](../DEVELOPMENT.md).
