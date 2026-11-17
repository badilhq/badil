# Architecture

Self-hosted, single-tenant. Everything runs in the user's cluster; badil talks to GitHub through the user's own GitHub App and never touches the cluster directly: ArgoCD does the deploys.

```
 service repo CI ── builds + pushes the image (no deploy call)

 manual trigger ──► POST /v1/deploy ──────────────────────────────────────────────────────────┐
 GitHub ── pull_request (PR label or push) ──► POST /webhooks/github ── async reconcile ──► badil serve ──► gitops repo
   ▲                                                                                          │ (GitHub App)   previews/<slug>/values.yaml
   └──────────────── PR comment (fan-out) ◄───────────────────────────────────────────────────┘                  ▼
                                     ArgoCD ApplicationSet (git dir generator: previews/*)
                                       └─ preview-apps chart → AppProject + Namespace + one Application per service and per dependency
                                            ├─ service chart (repoURL, chart path and image tag of the entry) at commitSha or the entry's branch
                                            └─ dependency chart (OCI registry or Helm repository) at its version

 label removed or pull_request closed ──► badil serve (async) ── unpins the PR's services, or deletes previews/<slug>/values.yaml ──► ArgoCD prunes
 badil cleanup (CronJob) ── deletes stale previews/<slug>/, unpins closed PRs, and open PRs whose label moved, conflicts or is gone ──► ArgoCD prunes
 GitHub ── push to the gitops default branch changing badil.yaml ──► POST /webhooks/github ── async sync of every preview ──► gitops repo
 POST /v1/sync ── syncs every preview, or one, with badil.yaml ──► gitops repo
```

## Components

| Component | Where | Notes |
|---|---|---|
| `badil serve` | this repo | chi HTTP server: `/healthz`, `/v1/deploy`, `/v1/sync`, `/webhooks/github`, `/webhooks/argocd` (optional) |
| `badil cleanup` | this repo | One-shot, CronJob (hourly). Per preview, the first matching rule: no pinned service → delete; `CLEANUP_TTL` set and no deploy within it → delete; otherwise unpin closed PRs, and open PRs whose label moved, conflicts or is gone (see [Cleanup sweep](#cleanup-sweep)) |
| `badil bootstrap-app` / `verify-app` | this repo | Create a GitHub App via the manifest flow / check credentials |
| `preview-apps` chart | this repo, [`charts/preview-apps`](../charts/preview-apps) | Renders AppProject, Namespace, one ArgoCD Application per service and per dependency; the AppProject and Applications in the namespace set by `argocdNamespace` (default `argocd`); Application annotations `preview.badil.dev/*` carry the PR metadata |
| ApplicationSet | this repo, [`examples/argocd`](../examples/argocd) | Git directory generator on `previews/*` |
| Service chart | each service repo, at the entry's `chart.path` (`services.<name>.chart.path` of `badil.yaml`, default `k8s/`; uses the `app` chart, [`charts/app`](../charts/app)) | Image tag = the entry's `imageTag`: the commit SHA formatted by `imageTag` of `badil.yaml` |
| `badil` chart | this repo, [`charts/badil`](../charts/badil) | Installs `badil serve` (Deployment, Service, optional Ingress) and the cleanup CronJob (off by default) |

## Gitops repo contract

```
badil.yaml                       # read by badil only: services, their chart settings, image tag format and paths, dependencies, needs, namespace metadata, optional preview block
previews/<slug>/values.yaml      # written by badil, read by preview-apps: apiVersion badil.dev/v1beta1, self-contained
```

`badil.yaml` is the only preview config: badil is its only reader, and writes everything a renderer needs into each preview file, chart settings and image tags included. The renderer reads only the preview file (plus the ApplicationSet's `argocdNamespace`), so it is swappable on the preview file alone. `badil.yaml` lives at the repo root, at a fixed path. [CONTRACT](CONTRACT.md) publishes `previews/<slug>/values.yaml` for any renderer: its schema, lifecycle, conformance fixtures and a renderer's obligations.

```yaml
# badil.yaml
imageTag: "sha-{sha7}"          # optional, default "{sha}": image tag format of every service
namespace:                      # optional: extra metadata of every preview Namespace
  labels:                       # string values; a label the chart sets is refused
    pod-security.kubernetes.io/enforce: baseline
  annotations: {}               # string values; argocd.argoproj.io/sync-wave is refused
services:                       # required, non-empty: the service catalog, in catalog order
  backend-1:
    repo: https://github.com/acme/backend-1.git   # required, non-empty string
    branch: master              # optional, default "main": the branch badil writes into each preview entry, and the PR-comment badge
    imageTag: "{sha8}-amd64"    # optional: overrides the root imageTag for this service
    chart:                      # optional: how the service's chart is rendered, every key optional
      path: deploy/helm         # default "k8s/": chart path in the service repo
      imageTagParam: image.tag  # default "app.image.tag": Helm parameter set to the image tag
      hostParam: ingress.hostname   # default "app.ingress.host": Helm parameter set to the preview host
      valueFiles:               # default none: extra values files, applied in order
        - values-preview.yaml
    needs: [postgres]           # optional: the services and dependencies it calls (see Needs)
    paths: ["api/**", "libs/common/**"]   # optional: globs of the files of its repo that belong to it (see Path filters)
  front:
    repo: https://github.com/acme/front.git
    needs: [backend-1]
dependencies:                   # optional: upstream charts deployed in every preview, in this order
  postgres:
    chart: oci://registry-1.docker.io/bitnamicharts/postgresql   # an oci:// chart reference: repo must be absent
    version: 16.4.2             # required: the chart version
    values: {auth: {password: preview}}   # optional: any mapping, passed as the chart's values
    valueFiles: [deps/postgres.yaml]      # optional: values files, paths from the gitops repo root
  mailpit:
    repo: https://jouve.github.io/charts  # required when chart is not oci://: an https:// Helm repository
    chart: mailpit              # the chart name in repo
    version: 0.20.0
    hostParam: ingress.hostname # required when exposed: Helm parameter set to the preview host
    # needs: [...]              # optional on a dependency too
preview:                        # optional: preview label, preview cap, Argo CD link and per-preview URLs (URLs off without domain)
  label: preview                # optional, default "preview": the PR label that creates the preview
  maxPreviews: 10               # optional, default no cap: deploys that would create a preview past it are refused
  argocdURL: https://argocd.example.com   # optional: the Argo CD UI; the PR comment links the preview's Applications
  domain: 127.0.0.1.nip.io      # lowercase bare domain: no scheme, no path, no leading or trailing dot
  tls: false                    # optional, default true: https or http in the PR-comment links only
  expose:                       # services and dependencies that get a host and a link
    front: {}                   # entry value: empty, {} or {path: ...}
    backend-1:
      path: /docs               # optional, default "/": PR-comment link only
    mailpit: {}
```

badil reads `badil.yaml` on every deploy, reconcile, teardown that keeps the preview, and sweep. An unknown or repeated key at any level is rejected, as is a service name that is not a string or a `services` entry that is not a mapping. A malformed file fails `/v1/deploy` and makes every reconcile log `preview reconcile failed`, with an error prefixed `badil.yaml:` naming the offending key; a missing file fails the same way (`gitops: not found`). An `expose` entry for a name that is neither in `services` nor in `dependencies` is ignored.

- `services.<name>.repo` and `services.<name>.branch` accept any YAML string (`branch: yes` is the branch `yes`: `badil.yaml` never reaches Helm) and reject a number, a boolean, a null, a mapping or a sequence. A PR's repo maps to the services whose `repo` is `https://github.com/<owner>/<repo>`, compared case-insensitively, with or without a trailing `/` or `.git`. Other URL forms (SSH, GitHub Enterprise) never match, so the label flow never deploys those services.
- `imageTag` and `services.<name>.imageTag` are an image tag format: an optional prefix, one placeholder (`{sha}`, `{sha7}` or `{sha8}`) and an optional suffix, made of `[a-zA-Z0-9_.-]` (the prefix not starting with `.` or `-`), at most 93 characters. Quote it: an unquoted `{sha}` is a YAML mapping. A service's own `imageTag` wins over the root one, which defaults to `{sha}`. badil expands it into each entry's `imageTag`:

  | Placeholder | Pinned (`commitSha` set) | Branch-tracking |
  |---|---|---|
  | `{sha}` | `commitSha` as written | `${ARGOCD_APP_REVISION}` |
  | `{sha7}` | first 7 characters of `commitSha` | `${ARGOCD_APP_REVISION_SHORT}` |
  | `{sha8}` | first 8 characters of `commitSha` | `${ARGOCD_APP_REVISION_SHORT_8}` |

  Argo CD replaces the branch-tracking variables with the revision it syncs. `/v1/deploy` accepts any `commit_sha`: send the full SHA, or a `{sha7}`/`{sha8}` tag will not match the one CI pushed.
- `services.<name>.chart` keys are all optional: badil writes each into the preview file with its default applied. `path`, `imageTagParam` and `hostParam` must be non-empty strings, and `valueFiles` a non-empty list of non-empty strings. A null `chart` or a null key is rejected; omit it to use the defaults.
- `dependencies.<name>` needs `chart` and `version`, non-empty strings (quote a version YAML reads as a number, such as `"1.0"`). A `chart` starting with `oci://` is an OCI chart reference, with a host, and `repo` must be absent; any other `chart` is a chart name and `repo`, an `https://` Helm repository URL with a host, is required. `values` is any mapping, `valueFiles` a non-empty list of non-empty strings and `hostParam` a non-empty string; an exposed dependency without `hostParam` is rejected, as no Helm parameter is known to set its host. A dependency name must be unique and must not be a service name: both become `<slug>-<name>` Applications. See [Dependencies](#dependencies).
- `services.<name>.needs` and `dependencies.<name>.needs` are non-empty lists of names of services or dependencies of `badil.yaml`. An unknown name, a repeated name, the entry's own name and an empty list are rejected, with the key's path (for example `badil.yaml: services.front.needs: "cache" is neither a service nor a dependency`). Cycles are allowed. See [Needs](#needs).
- `services.<name>.paths` is a non-empty list of glob patterns, relative to the service's repo root, matched against the paths of the files a PR changes, with `/` separators, by [doublestar](https://github.com/bmatcuk/doublestar) v4: `*` matches within a path segment, `**` across segments (`api/**` is every file under `api/`), and `?`, `[...]` and `{a,b}` work as in a shell. A null, a value that is not a list, an empty list, an empty string or an item that is not a string is rejected with `badil.yaml: services.<name>.paths must be a non-empty list of non-empty strings`, and a malformed pattern with `badil.yaml: services.<name>.paths[<i>]: invalid pattern "<p>"` (`<i>` counted from 0). See [Path filters](#path-filters).
- `namespace.labels` and `namespace.annotations` are added to every preview Namespace, after the chart's own (`app.kubernetes.io/name`, `app.kubernetes.io/instance`, `app.kubernetes.io/part-of`, `app.kubernetes.io/managed-by`, and the annotation `argocd.argoproj.io/sync-wave`). Keys and values are strings (an empty value is allowed); a null, a number or a boolean is rejected, as is a key the chart sets. An empty block writes nothing.
- `preview.label` must be a non-empty string; it is matched case-insensitively, like GitHub label names. `<label>:<name>` labels name the preview (see the label flow below).
- `preview.maxPreviews` must be a YAML integer of at least 1; a string (`"3"`), `0`, a negative number or a float is rejected, and a null or empty value means unset. It does not need `preview.domain`. See Preview cap in the [HTTP API](#http-api) section.
- `preview.argocdURL` is the base URL of the Argo CD UI: a string holding an absolute `http` or `https` URL with a host (a path such as `/argocd` is allowed), no credentials (`user:password@`), no query or fragment, and only URL characters as written: no space, parenthesis or other character that needs percent-encoding (write `%20`, not a space); a trailing `/` is removed. Any other value is rejected, with `preview.argocdURL` in the error; a null means unset. It does not need `preview.domain`. When set, the preview's PR comment ends, before the `---` line, with ``🔎 [All apps in Argo CD](<argocdURL>/applications?labels=app.kubernetes.io%2Finstance%3D<slug>)``: Argo CD's application list filtered on the label `app.kubernetes.io/instance=<slug>`, which every service and dependency Application of the preview carries, with its live sync and health status. Only that comment carries it, not the teardown, expiry or refusal comments. Setting or changing it writes no preview file: a comment gets the link at its next refresh (a deploy, a teardown that keeps the preview, or a sync that refreshes comments). badil does not check that the URL is reachable, and Argo CD asks for a login as usual.

The ApplicationSet passes the preview file as the only value file of `preview-apps`, and its `argocdNamespace` as a parameter. The chart's schema validates the values on every render (Argo CD shows a sync error); its root is closed, so any other root key (a leftover `serviceRepos` or `preview`) fails the render with `at '': additional properties '<key>' not allowed`.

```yaml
# previews/feature-pricing/values.yaml: every field in CONTRACT.md
apiVersion: badil.dev/v1beta1
gitopsRepoURL: https://github.com/acme/gitops.git
services:
  - name: backend-1             # pinned: deployed at commitSha
    repoURL: https://github.com/acme/backend-1.git
    branch: master
    commitSha: "<sha>"
    imageTag: "<sha8>-amd64"
    chart: {path: deploy/helm, imageTagParam: image.tag, hostParam: ingress.hostname}
    host: backend-1-feature-pricing.127.0.0.1.nip.io
    metadata: {pr-url: https://github.com/acme/backend-1/pull/42}   # and the PR's other keys
  - name: front                 # no commitSha: tracks its branch
    repoURL: https://github.com/acme/front.git
    branch: main
    imageTag: sha-${ARGOCD_APP_REVISION_SHORT}
    chart: {path: k8s/, imageTagParam: app.image.tag, hostParam: app.ingress.host}
dependencies:
  - name: postgres
    chart: {repoURL: oci://registry-1.docker.io/bitnamicharts/postgresql, version: 16.4.2}
```

The file is self-contained: a renderer needs only it to know each service's repo, revision, image tag and chart settings, each dependency's chart, version and values, and the namespace metadata. [CONTRACT](CONTRACT.md#v1beta1-file) defines every field and the strict rules of v1beta1. For badil and `preview-apps`:

- **Written on every deploy, teardown and sweep.** Each deploy write, and the cleanup sweep's sync (step 4 of [Cleanup sweep](#cleanup-sweep)), project the file on the catalog as it is then: `apiVersion`, `gitopsRepoURL`, `namespace`, every service of `services` in catalog order with its `repoURL`, `branch`, `chart`, `imageTag` and `host` (a missing one added as a branch-tracking entry), then the pinned entries of services no longer in `services` that have a `repoURL`, unchanged except `host`, recomputed from `preview.expose`, then every dependency of `badil.yaml` in its order, with its `host`. When an entry of `badil.yaml` declares `needs`, only the pinned entries and what they need are kept (see [Needs](#needs)). A `badil.yaml` change is written on the next deploy, even for an unchanged SHA, and in every preview by the push that changed it, `POST /v1/sync` or the next sweep (see [Config sync](#config-sync)), one commit per preview; the PR comment is refreshed only when the services, the dependencies or hosts change. See [Pinned and branch-tracking services](#pinned-and-branch-tracking-services) for removed services. Teardowns unpin, then project the file on `badil.yaml`, in the same commit, so an unpinned entry gets its branch-tracking `imageTag` at once. When `badil.yaml` cannot be read or parsed, such a teardown writes nothing and fails: the file keeps the PR's pin, and the next event of that PR or the next sweep retries it. A teardown that deletes the preview does not read `badil.yaml`.
- **Validated by the chart.** `preview-apps` ships `values.schema.json`, generated from badil's Go types (`make schema`), and Helm validates the values (the preview file and `argocdNamespace`) against it on every render; a violation fails the render with the field's JSON pointer, for example `at '': missing property 'apiVersion'`. CI fails when the schema is out of date. The root is closed: a key other than `apiVersion`, `gitopsRepoURL`, `namespace`, `services`, `dependencies` and `argocdNamespace` fails the render, and a missing `gitopsRepoURL` or `dependencies` fails it too.

## Per-preview URLs

Each exposed service of a preview gets the host `<service>-<slug>.<domain>` (the `<service>-<slug>` label capped at 63 characters, see below). One wildcard DNS record (and, for TLS, one wildcard certificate) for `*.<domain>` covers every preview.

- badil writes the host into each exposed entry's `host` in `values.yaml`, and `preview-apps` passes it to the service's Application as the Helm parameter named by the entry's `chart.hostParam` (`app.ingress.host` by default).
- badil appends ` · [<host><path>](<scheme>://<host><path>)` to the service's line in the PR comment.

`tls` only picks the link scheme; ingress TLS, the ingress class and annotations stay in the service chart. Services not in `expose` (non-HTTP services, for example) get no host and no link.

Length caps: a slug longer than 53 characters (Helm's release-name limit, which Argo CD hits through the ApplicationSet's `releaseName`) and a `<service>-<slug>` host label longer than 63 characters (the DNS label limit) are cut, and a hash of the full value (6 hex characters) is appended. For example, the branch `feature/very-long-branch-name-that-keeps-going-and-going-past-the-helm-limit` gets the slug `feature-very-long-branch-name-that-keeps-going-5bec34`, and the service `front-office-web` in it gets the host `front-office-web-feature-very-long-branch-name-that-keep-395ff3.<domain>`.

Upgrade order: upgrade badil (0.10.0 or later) first, then `preview-env` (0.3.0 or later). `preview-env` 0.3.0 reads only `host`, so an entry without it gets no ingress host. Preview files written by an older badil carry no `host` until their next write (deploy or sweep).

## Preview limits

badil and the `preview-apps` chart create no quota or policy objects: per-preview limits belong to the cluster's policy layer, which selects preview namespaces by the label `app.kubernetes.io/part-of: preview-environments` or by labels set in `namespace` of `badil.yaml`. [`examples/kyverno/preview-guardrails.yaml`](../examples/kyverno/preview-guardrails.yaml) is one Kyverno `GeneratingPolicy` (Kyverno v1.19.0 or later) that creates a ResourceQuota, a LimitRange and an ingress NetworkPolicy in every preview namespace, and keeps them in sync with the policy. Kyverno creates them, not Argo CD, so the `previews` AppProject is unchanged; teardown deletes them with the namespace.

A quota on compute resources (`requests.cpu`, `requests.memory`, `limits.memory`) refuses pods that do not set them, and `charts/app` ships `resources: {}`: ship a LimitRange with default requests and limits next to such a quota, as the example does.

## Pinned and branch-tracking services

A preview runs each service in one of two modes, set by `commitSha` in `previews/<slug>/values.yaml`:

- **Pinned** (`commitSha` set): the Application uses `targetRevision: <sha>` and sets the image tag parameter (the entry's `chart.imageTagParam`) to the entry's `imageTag`, the format expanded from `<sha>` (the full SHA by default). It changes only when badil writes a new SHA.
- **Branch-tracking** (no `commitSha`): the Application uses `targetRevision: <branch>`, the entry's `branch`, and sets the image tag parameter to the entry's `imageTag`, which holds `${ARGOCD_APP_REVISION}`, `${ARGOCD_APP_REVISION_SHORT}` or `${ARGOCD_APP_REVISION_SHORT_8}`: Argo CD resolves it to the commit it syncs. It follows new commits on that branch.

The image tag parameter is passed with `forceString: true`, so a digits-only short SHA stays a string ([CONTRACT](CONTRACT.md#renderer-obligations), obligation 4).

In both modes the Application's `repoURL` is the entry's `repoURL`, and the AppProject's `sourceRepos` are the distinct `repoURL`s of the file's entries.

Every preview lists every service of `badil.yaml` (only the pinned ones and what they need when `badil.yaml` declares `needs`, see [Needs](#needs)): a service added to it appears in every preview as a branch-tracking entry at the next write (deploy, sweep, or a teardown that keeps the preview). A service removed from `badil.yaml` leaves a preview according to its mode:

- **Branch-tracking** entries are dropped by the next write, and Argo CD prunes their Application.
- **Pinned** entries stay, with their last `name`, `repoURL`, `branch`, `commitSha`, `imageTag`, `chart` and `metadata`, and keep running at their SHA until their PR leaves the preview: its teardown unpins them and drops them in the same commit, or deletes the file when no pin remains. This protects the previews of open PRs from an accidental removal. A kept entry keeps its `host` while its name is still in `preview.expose`.

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

## Dependencies

`dependencies` of `badil.yaml` are upstream charts deployed in every preview (only in those whose pinned services need them when `badil.yaml` declares `needs`, see [Needs](#needs)) next to the services: a database, a cache, a mail catcher. They are never pinned, get no image tag, and need no git repo of their own. badil writes them into every preview file, in `badil.yaml` order, on every deploy, teardown that keeps the preview, and sweep sync: a dependency added to or removed from `badil.yaml` reaches every preview that includes it by its next write, and the PR comment is refreshed. With `needs`, a dependency that no pinned part needs leaves the preview file unchanged and refreshes no comment (see Staleness in [Needs](#needs)).

`preview-apps` ([`dependencies.yaml`](../charts/preview-apps/templates/dependencies.yaml)) renders one Application `<slug>-<name>` per dependency, in the preview namespace, from its chart source:

- **OCI chart** (`chart.repoURL` starts with `oci://`): `repoURL` the chart reference, `targetRevision` the version, `path: "."`.
- **Helm repository**: `repoURL` the repository, `chart` the chart name, `targetRevision` the version.
- `values` is passed as `helm.valuesObject`. With `valueFiles`, the Application has two sources, and no `spec.source`: the chart, with `helm.valueFiles` set to `$values/<path>`, and the gitops repo (`gitopsRepoURL`) at `HEAD` with `ref: values`. Value precedence in Argo CD, highest first: parameters, `valuesObject`, `valueFiles`, the chart's `values.yaml`.
- An exposed dependency gets the host `<name>-<slug>.<domain>`, passed as the Helm parameter `hostParam`, and a link in the PR comment.
- Sync wave `-1`: dependency Applications are created before the service Applications (wave `0`). This orders creation only; a service still has to tolerate a dependency that is not ready yet.
- Labels: those of a service Application, with `app.kubernetes.io/name: <name>`. Annotations: `argocd.argoproj.io/sync-wave: "-1"`, `preview.badil.dev/vcs.ref.revision: ""` and `preview.badil.dev/host` (the host, `""` when not exposed), like a branch-tracking service, and no PR metadata.
- The per-preview AppProject lists every dependency `repoURL` in `sourceRepos`, and `gitopsRepoURL` when a dependency has `valueFiles`.

The PR comment lists each dependency after the services: ``- **<name>** · 📦 `<chart>` `<version>` ``, the chart being `chart.name` or the last path segment of the OCI reference, then the link when it is exposed.

Notifications: the triggers of [`github-deployments-values.yaml`](../examples/argocd/github-deployments-values.yaml) skip dependency Applications, whose revision annotation is empty (see [Preview deployments on GitHub](#preview-deployments-on-github)). `on-preview-healthy` of `notifications-values.yaml` still fires for them, and badil ignores the event, as for a branch-tracking service: it has no `updated-at`.

Name collision: a dependency name is never a service name of `badil.yaml`, which rejects it. A service removed from `badil.yaml` can still have a kept pinned entry (see [Pinned and branch-tracking services](#pinned-and-branch-tracking-services)) with the name of a new dependency: the pinned entry wins until its PR leaves the preview. badil leaves the dependency out of that preview's file and logs the warning `dependency left out: a pinned service has its name` with `slug` and `name`; the first write after the entry is unpinned and dropped adds the dependency.

AppProject limit: the per-preview AppProject allows only the preview namespace as destination and only `Namespace` as a cluster-scoped kind, for dependencies as for services. Argo CD refuses a chart that renders cluster-scoped resources (a ClusterRole, a CRD) or targets another namespace: pick a chart, or values, that render only namespaced resources in the release namespace. badil does not work around it.

A private chart registry or Helm repository needs an Argo CD repository credential, which you set up: badil passes no credential.

## Needs

`needs` trims a preview to what its PRs use. A service or dependency of `badil.yaml` may list the services and dependencies it calls:

```yaml
services:
  front:    {repo: …, needs: [backend]}
  backend:  {repo: …, needs: [postgres, redis]}
  payments: {repo: …, needs: [backend, kafka, postgres]}
  search:   {repo: …, needs: [elasticsearch]}
dependencies:
  postgres: {…}
  redis: {…}
  kafka: {…}
  elasticsearch: {…}
```

- **Activation.** When no entry of `badil.yaml` declares `needs`, every preview runs every service and dependency. As soon as one entry declares it, every preview runs only its included set: its pinned entries (catalog services, and kept pinned entries of services removed from `badil.yaml`), plus every service and dependency in the transitive `needs` of its pinned catalog services. A kept pinned entry of a removed service has no `needs`. Cycles are followed once.
- **Projection.** Every write projects the file on `badil.yaml` (deploy, teardown that keeps the preview, sweep sync), so the set is computed on each: included services in catalog order, then kept pinned entries of removed services, then included dependencies in `badil.yaml` order, hosts only for included parts. A part left out is absent from the preview file: `preview-apps` renders no Application for it and Argo CD prunes it. A shared preview grows when a PR joins and shrinks when one leaves: closing the `payments` PR of a preview also pinned by `front` drops `payments` and `kafka` in the teardown commit.
- **User responsibility.** badil does not infer `needs`. A missing entry gives a preview without a part the code calls.
- **PR comment.** After the service and dependency lists, separated by a blank line, a line names the catalog services then the dependencies left out, in `badil.yaml` order, only when there is at least one. With only `front` pinned in the example: ``Not in this preview: `payments`, `search`, `kafka`, `elasticsearch` ``. It comes before the pinned-image note.
- **Staleness.** The line is derived from `badil.yaml` minus the included set, and is refreshed only by a deploy, or by a write that changes the ordered service or dependency names or a host. A `badil.yaml` change that alters only the left-out set (a new service or dependency that no pinned part needs) leaves the preview file unchanged: the sweep writes nothing and existing comments keep the old line until the PR's next deploy.

## Path filters

`paths` lets a monorepo PR pin only the services it changes. Without it, a labelled PR pins every catalog service of its repo. A service of `badil.yaml` may list the files of its repo that belong to it:

```yaml
services:
  api:    {repo: https://github.com/acme/platform.git, paths: ["api/**", "libs/common/**", "go.mod"]}
  web:    {repo: https://github.com/acme/platform.git, paths: ["web/**"]}
  worker: {repo: https://github.com/acme/platform.git, paths: ["worker/**", "libs/common/**"]}
```

- **Touched services.** On every reconcile that deploys (an open PR with a target, whatever the action, `unlabeled` included), when a service of the PR's repo declares `paths`, badil lists the files the PR changes against its base (`GET /repos/{owner}/{repo}/pulls/{number}/files`, every page of 100). A service without `paths` is always touched; a service with `paths` is touched when a changed file matches one of its patterns. With the example, `web/src/app.tsx` alone touches `web`; adding `libs/common/auth.go` also touches `api` and `worker`; `docs/README.md` alone touches no service. Only the touched services are deployed, in catalog order, at the PR's head SHA, and only they are checked for a holder (see One PR per service in [HTTP API](#http-api)).
- **Renames.** A renamed file touches a service when its new path or its previous path matches: moving `api/x.go` to `tools/x.go` touches `api`.
- **More than 3,000 files.** GitHub lists at most 3,000 files of a PR. When the PR's `changed_files` exceeds 3,000, every service of its repo is touched and no file is listed.
- **Failed listing.** A listing that fails, on any page, logs `preview reconcile failed` and deploys nothing, whatever the action, `closed` included; it never falls back to every service.
- **Later pushes.** Before deploying, the PR's entries in the target preview for services of its repo that it no longer touches are unpinned, in one commit `chore(preview): remove <names> from <slug>`, projected on `badil.yaml` like a teardown that keeps the preview, and the comments of the PRs still pinned there are refreshed. Pins the PR made through `/v1/deploy` for services of other repos stay. This write never deletes the preview, even when it unpins the PR's last pin: the deploy that follows pins at least one touched service. It counts in `badil_teardowns_total` with trigger `webhook`; when it fails, the reconcile logs `preview reconcile failed` and deploys nothing. After it, the deploy always writes and comments, so the PR's comment shows the current pins. A PR refused by a holder or the preview cap keeps its pins until an event where it deploys. An unpinned service's GitHub deployment goes `inactive` (see [Preview deployments on GitHub](#preview-deployments-on-github)).
- **No touched service.** A PR that touches no service is torn down from its target preview (all its pins there, deleting the preview when no pin is left), without the teardown comment. Its comment is replaced by

  ```
  ## ⏭️ Preview: `<slug>`

  This PR changes no service of the preview (`paths` in badil.yaml), so nothing is deployed.
  ```

  even when there was no preview, and the reconcile logs `preview reconcile skipped` with reason `no service changed`. Neither the holder nor the preview cap check runs. A failed teardown is logged and counted as for any teardown, and the comment and outcome stay the same.
- **User responsibility.** badil does not infer `paths`: a shared file missing from a service's `paths` (a library, a lockfile) does not pin it. The PR comment shows which services are pinned.
- **Sweep and `/v1/deploy`.** Only PR events apply path filters: the cleanup sweep does not re-check them, and `/v1/deploy` names its service.

## Service repo contract

The `preview-apps` chart ([`application.yaml`](../charts/preview-apps/templates/application.yaml)) renders one Argo CD Application per entry of the preview file, from the entry's `repoURL` and `branch`, and expects each service repo to provide:

- A Helm chart at `k8s/` by default, or at `services.<name>.chart.path` of `badil.yaml`.
- A `main` branch by default, or the service's `branch` in `badil.yaml`: branch-tracking services use it as `targetRevision`.
- A Helm parameter `app.image.tag` by default (the service chart declares the [`app`](../charts/app) chart as a dependency aliased `app`), or `services.<name>.chart.imageTagParam`.
- Images tagged as the service's `imageTag` format renders the git commit SHA (the full SHA by default): pinned services use the PR head SHA (label flow) or the `commit_sha` sent to `/v1/deploy`, branch-tracking services the revision Argo CD syncs.
- For services in `preview.expose`: ingress enabled and a Helm parameter setting the ingress host, `app.ingress.host` by default or `services.<name>.chart.hostParam`. Otherwise the PR-comment link is dead.
- A service whose image is not built per commit (for example a database) may ignore the image tag parameter.
- Extra values files, from `services.<name>.chart.valueFiles`, applied in order. Argo CD joins a relative path to the chart path and a path starting with `/` to the repo root; a missing file fails the sync.

`hostParam` is passed as a Helm `--set`, key verbatim. A list-indexed name such as `ingress.hosts[0].host` replaces the chart's default list, so the full list (with its `paths`) and `ingress.enabled: true` must come from a file in `valueFiles`; the parameter then only sets that element's field.

Override them per service in `services.<name>` of `badil.yaml`: `branch`, `imageTag` and `chart`.

The ApplicationSet example sources `preview-apps` from an OCI registry (`oci://` with `path: "."`), which requires Argo CD 3.1 or later.

## Preview data

badil never reads, writes or drops data stores.

- A preview's identity is its slug: both the Helm release name of `preview-apps` and the preview namespace.
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

POST /v1/sync
  Authorization: Bearer $BADIL_DEPLOY_TOKEN
  empty body or {} → every preview; { "slug": "<slug>" } → that preview   # unknown field → 400
  → { "synced": [<slugs rewritten>], "unchanged": <count>, "failed": [<slugs>] }
  → 400 { "error": "invalid slug" }               # empty, or not its own slugified form (e.g. ../x)
  → 404 { "error": "preview <slug> not found" }   # no previews/<slug>/values.yaml
  → 503 { "error": "badil.yaml unavailable: …" }  # badil.yaml can't be read or parsed

POST /webhooks/github
  X-Hub-Signature-256 verified (401 on mismatch)
  installation ≠ GITHUB_INSTALLATION_ID (ping exempt) → 403
  pull_request opened/reopened/synchronize/labeled/unlabeled/closed → async reconcile; other actions → log only
  push to GITOPS_REPO's default branch, not a deletion → async config sync (see Config sync); other pushes → log only
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
6. An open PR with a target deploys every catalog service of its repo, or, when its services declare `paths`, the services it touches, after unpinning there its entries for the ones it no longer touches (see [Path filters](#path-filters)), in catalog order, at the PR's head SHA, to the target slug, through the manual flow's write and comment fan-out, unless another PR holds one of its services there (see One PR per service below), or the target does not exist and the preview cap is reached (see Preview cap below). A failed service is logged and the next one still deploys. A service whose write would change nothing but its `updated-at` (same SHA, host and PR metadata, on the first read and after a 409 re-fetch) writes no commit and posts no comment, and logs `preview deploy unchanged`, unless step 5 removed the PR from another preview; when every service is unchanged, the reconcile is skipped with reason `already deployed`. A PR comment that failed to post, or that a failed teardown left stale, is therefore not repaired by a later label event at the same head SHA: the next push to the PR or a manual `/v1/deploy` repairs it. The deploy fan-out is the last write to the PR's comment, so a PR that moved shows its new preview.
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

One PR per service: a preview holds one PR per service. Before deploying, a reconcile reads the target's `values.yaml` (a missing file means no holder). An entry for one of the PR's services (the services it touches, with [path filters](#path-filters)) is held when it is pinned (`commitSha` set), has metadata, and its `pr-url` names another PR. Any held entry refuses the whole PR, for every target, branch or named, since two PRs can also share a head branch. The first held service is reported in the PR's comment:

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

Background reconciles run one at a time, in no guaranteed order; a reconcile reads the PR only once it runs. A reconcile has a 20s budget, counted from when it starts running and shared, in order, by its cleanup teardowns, the files listing of [path filters](#path-filters), the holder check, the preview cap check, the unpin of untouched services, then its services; a service not reached in time is logged as failed. Shutdown waits up to 25s for queued and running jobs; jobs still queued then are dropped, and the cleanup sweeper catches missed teardowns of closed PRs.

A labeled PR with no push for longer than `CLEANUP_TTL` is expired by the sweeper and not recreated while the label stays: push, or remove and re-add the label, to recreate it. A missed `unlabeled` of a named label, or a label that moved, is unpinned by the sweeper; a missed `unlabeled` of the plain label at the branch slug still waits for close or TTL.

Teardown flow: a reconcile tears the PR down from each slug of its cleanup set: the PR is closed (merged or not), lost its label (for example after `unlabeled`), has conflicting labels, or moved to another preview. A `closed` delivery whose reconcile can't read the catalog or the PR falls back to tearing down, as closed, the payload's head branch and the payload's named labels, logged as a warning, so a close still tears down when GitHub reads fail. A fallback teardown that would keep the preview also needs `badil.yaml`: when that read fails too, it writes nothing, and the PR's next event or the next sweep tears it down. For each slug (the slugified head branch or named label), badil reads `previews/<slug>/values.yaml` and unpins every service whose `pr-url` points to the PR: the entry loses its `commitSha` and `metadata` and goes back to tracking its branch. If other pins remain, the file is updated, projected on `badil.yaml` in the same commit (an unpinned entry gets its branch-tracking `imageTag`, and one of a service no longer in `badil.yaml` is dropped), and the remaining PRs' comments are refreshed; when `badil.yaml` cannot be read or parsed, nothing is written and the teardown is retried by the PR's next event or the next sweep. Otherwise the file is deleted, without reading `badil.yaml`. Both writes use SHA optimistic locking + 409 retry, deciding update vs delete again on every retry. The PR's comment is edited (or created) to say what was removed and why: `PR closed`, ``label `<label>` removed``, ``label `<label>:<name>` removed``, ``conflicting `<label>:` labels`` or ``moved to preview `<slug>` ``.

Deleting the file drops `previews/<slug>/` from the git directory generator, and the ApplicationSet deletes the preview's Application and its resources. This requires the ApplicationSet controller policy `sync` (the default) or `create-delete`; with `create-only` or `create-update` a deleted preview keeps running. Closes missed while badil was down, and pins written by a deploy after the PR closed, are caught by the cleanup sweeper. A labeled PR whose branch was renamed after the deploy is unpinned from its old slug by the sweeper, without a comment; push to redeploy it at the new slug.

## Cleanup sweep

The sweep runs in the server every `CLEANUP_INTERVAL` when it is set, starting one interval after startup; runs never overlap and shutdown cancels a run in progress. Otherwise `badil cleanup` runs it once per CronJob schedule (hourly by default). Use one or the other: both run the same idempotent sweep, so running both only wastes work. The chart runs the sweep in the server (`cleanup.interval`, `1h` by default) unless `cleanup.enabled=true` switches it to the CronJob. Only the in-server sweep exports the sweep metrics. The sweep lists `previews/*`, applies to each preview the first matching rule 1-3, then syncs every preview it keeps (step 4):

1. **No pinned service**: the file is deleted (`chore(preview): delete <slug> preview`). No comment.
2. **Expired** (only when `CLEANUP_TTL` is set): the newest `updated-at` of the pinned services is older than `CLEANUP_TTL`. The file is deleted (`chore(preview): expire <slug> preview`) and its open PRs get a comment saying the preview expired. A pinned service without a parseable `updated-at` never counts, so such a preview never expires.
3. **Closed or moved PRs**: every distinct PR pinning a service is looked up on GitHub. A PR whose state can't be read (404, 403, any error) keeps its pins and is logged as a warning.
   - A closed (or merged) PR goes through the same teardown as a `pull_request` `closed` delivery: unpin, update or delete, comments.
   - An open PR's labels are resolved to its target preview (see the label flow). It is unpinned from this preview, in this order, when its labels conflict (``conflicting `<label>:` labels``), or target another preview (``moved to preview `<target>` ``): both without touching its comment, which shows where it lives now. When it has no target and this preview is not its branch slug, it goes through the teardown, and its comment says ``label `<label>` removed``. Any other open PR is left alone, including an unlabeled PR pinned at its branch slug.

   This catches missed `unlabeled` deliveries, conflicts, and renamed or deleted labels. The catalog is read once per sweep, for the label and step 4; when it can't be read, `cleanup catalog unavailable` is logged as a warning, it is not counted as a failure, only closed PRs are torn down, and step 4 is skipped.

   This changes manual deploys: a `/v1/deploy` pin now survives the sweep only at the PR's target preview (its single `<label>:<name>` slug, or its branch slug when it has only the plain label), or at its branch slug when the PR has no target. Anywhere else it is unpinned (``moved to preview `<target>` `` or ``label `<label>` removed``); a PR carrying `<label>:a` loses a pin at its branch slug. A PR with conflicting named labels is unpinned from every preview (``conflicting `<label>:` labels``). A webhook reconcile of the PR also unpins it from its branch slug and its named-label slugs other than its target (`cleanupSlugs`). Such pins used to be left alone.

4. **Sync with `badil.yaml`**: every preview that rules 1 and 2 did not match is re-read after rule 3 (whose teardowns may have changed or deleted it) and rewritten as its projection on the catalog (see [Gitops repo contract](#gitops-repo-contract)) when it differs, in one commit `chore(preview): sync <slug> with badil.yaml`, with SHA optimistic locking + 409 retry; a file that already matches is not written. A preview whose rule 1-2 delete is cancelled by the re-check skips this step: the deploy that cancelled it already wrote the projection. A rewrite is logged (`preview synced`) and counted in `badil_previews_synced_total`. When it changed the list of services, the list of dependencies or a host, the open PRs' comments are refreshed.

A `badil.yaml` change (a service added, removed or renamed, a `repo`, `branch`, `preview.domain` or `expose` change) therefore reaches every preview within one sweep interval, without a push. With the App subscribed to `push`, it reaches them at once (see [Config sync](#config-sync)); the sweep stays the safety net. A mistaken edit propagates the same way, except that pinned entries of a removed service stay until their PR leaves; a git revert of `badil.yaml` undoes it at the next sweep.

After a catalog edit, one sweep makes one commit per preview whose file differs, and updates the comment of each open PR of the previews whose services, dependencies or hosts changed, all within the GitHub App's rate limit; `badil_previews_synced_total` shows the volume. A failed sync write counts as a failed preview and is retried by the next sweep. A failed comment refresh is logged (`preview sync comment failed`) and not retried, so that comment stays stale until the PR's next deploy or teardown, as for every comment fan-out.

Rules 1 and 2 re-check their condition on the freshly read file, and again after every 409, so a deploy landing during the sweep cancels the delete. A failing preview read, rule 1–2 delete or step 4 sync is logged and the sweep goes on; the command then exits 1 and the Job fails without retrying (`backoffLimit: 0`). A failing closed-PR teardown is only logged. The next scheduled run retries both. Comment fan-outs (deploy, teardown, expiry) skip closed PRs, so a closed PR keeps its teardown comment.

Known limit: the GitHub contents API lists at most 1,000 entries per directory, so the sweep sees at most 1,000 previews.

## Config sync

Two triggers run the sweep's step 4 (rewrite, `preview synced` log, `badil_previews_synced_total`, comment refresh) outside the sweep, without its rules 1-3:

- **Push.** A verified `push` delivery is acted on only when `repository.full_name` is `GITOPS_REPO` (case-insensitive), `ref` is `refs/heads/<repository.default_branch>` and `deleted` is false; the `webhook push` log of any other push carries its `reason`. Every push is answered 200. In the background, badil reads `badil.yaml`'s blob SHA on the default branch; when it equals the SHA of the last push sync (kept in memory, empty at startup), it stops (`config sync skipped`, reason `badil.yaml unchanged`). badil's own deploy, teardown and sync commits are pushes too, and stop there. Otherwise every preview is synced with that content and its SHA kept, also when a preview failed (the sweep retries it) or the content can't be parsed (the same content would fail again). A `badil.yaml` that can't be read, or previews that can't be listed, keep no SHA, so the next push retries.
- **`POST /v1/sync`** (see [HTTP API](#http-api)) syncs every preview, or the one its body names, and answers when done. It ignores and keeps the push SHA.

One config sync runs at a time in the server: pushes and `/v1/sync` calls each wait their turn, in no guaranteed order; a push whose `badil.yaml` SHA a run before it synced stops at the SHA check, and each `/v1/sync` call gets its own answer. A run is capped at 10 minutes (the sweep's bound), counted from when it starts running. It does not wait for reconciles nor block them, and the sweep's own step 4 runs beside it; SHA optimistic locking keeps concurrent writes safe. Shutdown waits for a push sync like for reconciles (up to 25s). Each run logs `config sync finished` (with `synced`, `unchanged` and `failed` counts) or `config sync failed`, and a failed preview logs `config sync preview failed`. A large `badil.yaml` change makes the same burst of commits and comments as a sweep.

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

GitHub App permissions: Contents R/W, Pull requests W, Metadata R. Subscribed events: `pull_request` and `push` (installation events are delivered automatically).

## Metrics

`GET /metrics` serves Prometheus metrics on `METRICS_ADDR`, a listener separate from `LISTEN_ADDR`, so the Ingress never exposes it. The chart adds a `metrics` port to the Service and, with `metrics.serviceMonitor.enabled`, a ServiceMonitor.

### Metric list

Every label has a closed set of values. Go runtime (`go_*`) and process (`process_*`) metrics are exported too.

| Metric | Type | Labels | |
|---|---|---|---|
| `badil_webhook_deliveries_total` | counter | `event`, `status` | Webhook deliveries. `event` is `ping`, `installation`, `installation_repositories`, `pull_request`, `push` or `other` once the signature is verified, `unverified` before. `status` is the HTTP status: `200`, `401` or `403`. |
| `badil_reconciles_total` | counter | `outcome` | One per pull request reconcile: `failed` (catalog, PR or preview read error, preview list error, PR files list error, failed unpin of untouched services, or no service write succeeded and at least one failed), else `skipped`, else `deployed` (a service write succeeded), else `torn_down`. |
| `badil_reconcile_skips_total` | counter | `reason` | Skipped reconciles by reason: `other label`, `not labeled`, `repo not in catalog`, `fork pr`, `service held by another pr`, `preview limit reached`, `conflicting preview labels`, `already deployed`, `no service changed`. |
| `badil_teardowns_total` | counter | `trigger`, `result` | Teardowns of a PR from a preview. `trigger` is `webhook` or `sweep`; `result` is `deleted`, `unpinned`, `noop` (nothing pinned) or `error`. |
| `badil_gitops_operation_duration_seconds` | histogram | `op`, `result` | Duration of gitops `write`, `teardown` and `sync` operations, 409 retries included; `result` is `ok` or `error`. |
| `badil_sweep_runs_total` | counter | `result` | In-server sweep runs, `ok` or `error`. A run cancelled by shutdown is not counted. |
| `badil_sweep_last_success_timestamp_seconds` | gauge | | Unix time of the last successful in-server sweep. |
| `badil_sweep_previews_deleted_total` | counter | `rule` | Previews deleted by the sweep: `no_pin` (rule 1) or `ttl` (rule 2). |
| `badil_previews_synced_total` | counter | | Previews rewritten to match `badil.yaml`, by a sweep (step 4 of the in-server sweep), a push or `/v1/sync` (see [Config sync](#config-sync)). |
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

- There are five triggers. Four apply only to pinned preview service Applications: those whose `preview.badil.dev/vcs.ref.revision` annotation is not empty. Branch-tracking services and dependencies have it empty. `on-preview-unpinned` applies when that annotation is empty and the Application's `status.history` holds a pinned entry.
- `on-preview-degraded` and `on-preview-sync-error` test that annotation first. A dependency with `valueFiles` has `spec.sources` and no `spec.source`, and Argo CD's `expr` fails on `app.spec.source.targetRevision` with `cannot fetch targetRevision from <nil>` instead of evaluating it as false; `and` stops at the empty annotation before reading it. `on-preview-unpinned` reads `status.history` nil-safely: a multi-source dependency's history entry carries `source: {repoURL: ""}` and no `revision`, and the pinned-entry test evaluates it as false.
- `on-preview-deployed` fires when the Application is synced and Healthy and `syncResult.revision` starts with the revision annotation, once per sync at the pinned SHA: its `oncePer` key is `syncResult.revision` (the PR head SHA, since a pinned service's `targetRevision` is its commit SHA) joined with the `id` of the last `status.history` entry. The notifications controller never clears a stored `oncePer` key, so a per-SHA key would leave a service unpinned and then pinned again at the same SHA (label removed and re-added, or a PR closed and reopened, with no new push) at `inactive`; with the per-sync key the re-pin's sync posts `success` again. A pin or re-pin that runs no sync posts nothing (limitation 15). Any further successful sync at that SHA (selfHeal or a manual sync) also adds another `success` status on the same deployment. The prefix guard waits for the sync at the pinned SHA: right after a pin, `operationState` still describes the previous sync, and a service that tracked main would otherwise get `success` on main's commit. A prefix keeps a short SHA sent to `/v1/deploy` working. It posts status `success` on the service repo (`spec.source.repoURL`) at that revision, with `environment_url` built from the `preview.badil.dev/host` annotation, which `preview-apps` sets from the service's `host`. The deployment is created with `auto_merge: false` and `required_contexts: []`, so an older ref or a red commit still gets one.
- `on-preview-degraded` fires when the Application's health is `Degraded` while it is `Synced` at `spec.source.targetRevision` (the SHA badil last pinned), once per `targetRevision`. It posts status `failure`. A Deployment turns Degraded only after its `progressDeadlineSeconds`; until then it is Progressing.
- `on-preview-sync-error` fires, once per `targetRevision`, when the sync operation at `targetRevision` ended in `Error` or `Failed` (`operationState.syncResult.revision` is that SHA), or the sync status is `Unknown` for a comparison against that SHA (`sync.comparedTo.source.targetRevision`), as when the chart fails to render. It posts status `error`. Its two tests are one condition, so it posts at most one `error` per SHA.
- The guards on `targetRevision` keep the previous SHA's Degraded health or failed sync, still shown right after badil pins a new SHA, from posting on the new SHA's deployment.
- `failure` and `error` use `spec.source.targetRevision` as the deployment's ref. For a synced pinned service it is the same SHA as the `syncResult.revision` that `success` uses, so all three land on the same deployment as successive statuses: the notifications controller reuses the first deployment it finds for that ref and environment, and creates one only when none exists.
- `on-preview-deleted` fires when the Application has a `deletionTimestamp` and posts `inactive` on the deployment for `app.spec.source.targetRevision` (the SHA badil last pinned).
- `on-preview-unpinned` fires when badil unpins the service from a preview that survives (a path-filter unpin on a later push, or a PR closed while another PR keeps the preview): the annotation is empty and `status.history` still holds a pinned entry, a single-source entry whose `revision` is a full SHA and whose `source.targetRevision` is a SHA prefix of it. It reads the last pinned SHA from that history (`findLast`) and posts `inactive` on that SHA's deployment, once per pinned sync that badil unpins: its `oncePer` key is that entry's `revision` and `id`, so after a re-pin at the same SHA, the next unpin posts `inactive` again. It fires as soon as the unpin reaches the Application, without waiting for the sync to the branch.
- The environment is `preview-<slug>-<service>`, one per preview service. A new `success` on it marks the prior `success` deployments `inactive` (GitHub's `auto_inactive`).

Limitations:

1. `inactive` on delete is best effort: the controller only sees the deletion if it processes the update before the Application's finalizer completes. It looks up the deployment by the SHA badil last pinned, so it closes that SHA's `success`, `failure` or `error` deployment; only when that SHA never got a status (still Progressing at teardown) does it create an extra `inactive` deployment for it. An earlier SHA's `success` stays.
2. Failed notifications are not retried.
3. Applying the config posts `success` for every already-Healthy pinned Application, `failure` for every pinned Application already Degraded at its SHA, and `error` for every one whose sync already failed or is `Unknown` at its SHA, and `inactive` for every unpinned Application whose sync history still holds a pinned entry, as `oncePer` has no prior state.
4. Environments accumulate on the service repos and can be deleted by hand.
5. The URL is the host root; the expose path is not applied.
6. One App installation covers all service repos (`service.github` takes one `installationID`).
7. For a pinned service with no host (for example `postgres`, not in `expose`), `environment_url` is sent as `""`. Whether GitHub accepts that is not verified; the notifications controller log shows the result.
8. `on-preview-deployed` posts `success` again only on a new sync: a service that was Healthy (`success`), then Degraded (`failure`), then Healthy again at the same SHA without a sync keeps `failure`. A push (new SHA) or a new sync at the same SHA posts `success` again.
9. A rollout that never completes (image never pushed, crash loop) shows as `failure` only after the Deployment's `progressDeadlineSeconds` (600 s by default).
10. A transient `Unknown` sync status (for example a repo-server error) posts `error` once for that SHA; a later Healthy sync at that SHA posts `success` on the same deployment.
11. With a short `commit_sha` sent to `/v1/deploy`, `on-preview-degraded` and `on-preview-sync-error` never fire, because their guards compare `app.spec.source.targetRevision` with the resolved full SHA; nothing logs it. `on-preview-deleted` then looks up the deployment by the short SHA and misses the full-SHA `success` deployment: it leaves that deployment open and creates an extra `inactive` deployment for the short ref. The label flow always pins the full SHA.
12. The unpin status is lost when the pinned entry has left the sync history (Argo CD keeps the last 10 syncs) before the controller sees the unpin. Not expected in practice.
13. When the unpinned SHA's sync succeeded but it never got a status (still Progressing at the unpin), the unpin creates an extra deployment for that SHA holding only `inactive`, as limitation 1 describes for delete.
14. Argo CD records a sync in its history only when it succeeds. If the last pinned SHA's sync failed (its deployment shows `error` or `failure`) and badil then unpins the service, `on-preview-unpinned` posts `inactive` on the previous successfully synced pin's deployment, and the failed SHA's deployment stays open.
15. `on-preview-deployed` needs a sync at the pinned SHA, and Argo CD auto-syncs an Application only when it is OutOfSync. A pin whose rendered manifests equal what is deployed (a chart that ignores the image tag, with its chart path unchanged by the PR) runs no sync: the service runs the pinned SHA with no deployment status. A re-pin at the same SHA that reaches the Application before the sync to the branch after the unpin, or after an unpin that itself ran no sync, runs no sync either: the last history `id` is unchanged, its key is already stored, and the deployment stays `inactive`.

### Dashboard

[`charts/badil/dashboards/badil.json`](../charts/badil/dashboards/badil.json) is a Grafana dashboard (uid `badil`) over these metrics: live previews, deploys and teardowns per hour, spin-up p50/p90 and p90 by service, zombies swept, reconcile skips by reason, webhook deliveries by status, gitops operation p90 and the last successful sweep. It picks its Prometheus data source through a `datasource` variable. With `dashboard.enabled`, the chart renders it as a ConfigMap labelled `dashboard.labels` (default `grafana_dashboard: "1"`), which the Grafana sidecar of kube-prometheus-stack loads; the JSON also imports on its own. Last successful sweep needs `cleanup.interval`, and the spin-up panels need the Argo CD notifications callback.

## Demo setup

Demo services: [demo-backend-1](https://github.com/badilhq/demo-backend-1), [demo-backend-2](https://github.com/badilhq/demo-backend-2), [demo-front](https://github.com/badilhq/demo-front). Gitops repo: [demo-gitops](https://github.com/badilhq/demo-gitops). Local dev of badil itself: [DEVELOPMENT.md](../DEVELOPMENT.md).
