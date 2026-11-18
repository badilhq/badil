# Configuration reference

This page lists what badil reads and writes: the gitops repo files, every key of `badil.yaml`, the preview file, what a service repo provides, and the server's environment variables. How these pieces work together is in [Architecture](ARCHITECTURE.md).

## Gitops repo files

The gitops repo holds two kinds of files:

```
badil.yaml                       # read by badil only: services, their chart settings, image tag format and paths, dependencies, needs, namespace metadata, optional preview block
previews/<slug>/values.yaml      # written by badil, read by preview-apps: apiVersion badil.dev/v1beta1, self-contained
```

`badil.yaml` is the only preview config. badil is its only reader, and writes everything a renderer needs into each preview file, chart settings and image tags included. `badil.yaml` lives at the repo root, at a fixed path.

The renderer reads only the preview file (plus the ApplicationSet's `argocdNamespace`), so it is swappable on the preview file alone. [CONTRACT](CONTRACT.md) publishes `previews/<slug>/values.yaml` for any renderer: its schema, lifecycle, conformance fixtures and a renderer's obligations.

## `badil.yaml`

Every key, with its default:

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

### Loading and validation

badil reads `badil.yaml` on every deploy, reconcile, teardown that keeps the preview, and sweep.

badil rejects these:

- an unknown or repeated key at any level;
- a service name that is not a string;
- a `services` entry that is not a mapping.

A malformed file fails `/v1/deploy` and makes every reconcile log `preview reconcile failed`. The error is prefixed `badil.yaml:` and names the offending key. A missing file fails the same way (`gitops: not found`).

An `expose` entry for a name that is neither in `services` nor in `dependencies` is ignored.

### `imageTag`

`imageTag` and `services.<name>.imageTag` are an image tag format. A service's own `imageTag` wins over the root one, which defaults to `{sha}`.

A format is made of these parts:

- an optional prefix;
- one placeholder: `{sha}`, `{sha7}` or `{sha8}`;
- an optional suffix.

It uses only `[a-zA-Z0-9_.-]`, the prefix does not start with `.` or `-`, and it is at most 93 characters. Quote it: an unquoted `{sha}` is a YAML mapping.

badil expands it into each entry's `imageTag`:

| Placeholder | Pinned (`commitSha` set) | Branch-tracking |
|---|---|---|
| `{sha}` | `commitSha` as written | `${ARGOCD_APP_REVISION}` |
| `{sha7}` | first 7 characters of `commitSha` | `${ARGOCD_APP_REVISION_SHORT}` |
| `{sha8}` | first 8 characters of `commitSha` | `${ARGOCD_APP_REVISION_SHORT_8}` |

Argo CD replaces the branch-tracking variables with the revision it syncs.

`/v1/deploy` accepts any `commit_sha`. Send the full SHA, or a `{sha7}`/`{sha8}` tag will not match the one CI pushed.

See [Pinned and branch-tracking services](ARCHITECTURE.md#pinned-and-branch-tracking-services).

### `namespace`

`namespace.labels` and `namespace.annotations` are added to every preview Namespace, after the chart's own. The chart sets the labels `app.kubernetes.io/name`, `app.kubernetes.io/instance`, `app.kubernetes.io/part-of` and `app.kubernetes.io/managed-by`, and the annotation `argocd.argoproj.io/sync-wave`.

Keys and values are strings, and an empty value is allowed. A null, a number or a boolean is rejected, as is a key the chart sets. An empty block writes nothing.

### `services.<name>.repo` and `services.<name>.branch`

Both accept any YAML string: `branch: yes` is the branch `yes`, since `badil.yaml` never reaches Helm. They reject a number, a boolean, a null, a mapping or a sequence.

A PR's repo maps to the services whose `repo` is `https://github.com/<owner>/<repo>`. The match is case-insensitive, with or without a trailing `/` or `.git`. Other URL forms (SSH, GitHub Enterprise) never match, so the label flow never deploys those services.

### `services.<name>.chart`

All `chart` keys are optional. badil writes each into the preview file with its default applied.

- `path`, `imageTagParam` and `hostParam` must be non-empty strings.
- `valueFiles` must be a non-empty list of non-empty strings.
- A null `chart` or a null key is rejected; omit it to use the defaults.

What the chart must provide is in [Service repo contract](#service-repo-contract).

### `services.<name>.needs` and `dependencies.<name>.needs`

Both are non-empty lists of names of services or dependencies of `badil.yaml`. Cycles are allowed.

badil rejects an unknown name, a repeated name, the entry's own name and an empty list. The error names the key's path, for example `badil.yaml: services.front.needs: "cache" is neither a service nor a dependency`.

See [Dependencies and needs](ARCHITECTURE.md#dependencies-and-needs), and the rules in [Needs](LIFECYCLE.md#needs).

### `services.<name>.paths`

`paths` is a non-empty list of glob patterns, relative to the service's repo root. badil matches them against the paths of the files a PR changes, with `/` separators, by [doublestar](https://github.com/bmatcuk/doublestar) v4:

- `*` matches within a path segment;
- `**` matches across segments: `api/**` is every file under `api/`;
- `?`, `[...]` and `{a,b}` work as in a shell.

These errors are possible, `<i>` being counted from 0:

| Value | Error |
|---|---|
| a null, a value that is not a list, an empty list, an empty string or an item that is not a string | `badil.yaml: services.<name>.paths must be a non-empty list of non-empty strings` |
| a malformed pattern | `badil.yaml: services.<name>.paths[<i>]: invalid pattern "<p>"` |
| a pattern that starts with `./` or `/`, or ends with `/` | `badil.yaml: services.<name>.paths[<i>]: pattern "<p>" never matches: paths are relative to the repository root, with no leading "./" or "/" and no trailing "/"` |

The last pattern never matches, since GitHub lists changed files relative to the repository root.

See [Path filters](ARCHITECTURE.md#path-filters), and the rules in [Path filters](LIFECYCLE.md#path-filters).

### `dependencies`

`dependencies.<name>` needs `chart` and `version`, both non-empty strings. Quote a version YAML reads as a number, such as `"1.0"`.

The chart source depends on `chart`:

- A `chart` starting with `oci://` is an OCI chart reference, with a host, and `repo` must be absent.
- Any other `chart` is a chart name. `repo`, an `https://` Helm repository URL with a host, is then required.

The other keys are optional:

- `values` is any mapping.
- `valueFiles` is a non-empty list of non-empty strings.
- `hostParam` is a non-empty string. An exposed dependency without `hostParam` is rejected, as no Helm parameter is known to set its host.

A dependency name must be unique and must not be a service name: both become `<slug>-<name>` Applications.

The per-preview AppProject allows only the preview namespace as destination, and only `Namespace` as a cluster-scoped kind, for dependencies as for services. Argo CD refuses a chart that renders cluster-scoped resources (a ClusterRole, a CRD) or targets another namespace. Pick a chart, or values, that render only namespaced resources in the release namespace. badil does not work around it.

A private chart registry or Helm repository needs an Argo CD repository credential, which you set up: badil passes no credential.

See [Dependencies and needs](ARCHITECTURE.md#dependencies-and-needs), and how they are rendered in [Dependencies](LIFECYCLE.md#dependencies).

### `preview.label`

`preview.label` must be a non-empty string. It is matched case-insensitively, like GitHub label names. `<label>:<name>` labels name the preview (see [Target preview](LIFECYCLE.md#target-preview)).

### `preview.maxPreviews`

`preview.maxPreviews` must be a YAML integer of at least 1. A string (`"3"`), `0`, a negative number or a float is rejected. A null or empty value means unset. It does not need `preview.domain`.

See [Preview cap](LIFECYCLE.md#preview-cap).

### `preview.argocdURL`

`preview.argocdURL` is the base URL of the Argo CD UI. It must be a string holding an absolute `http` or `https` URL with a host, and these limits apply:

- a path such as `/argocd` is allowed;
- no credentials (`user:password@`);
- no query or fragment;
- only URL characters as written: no space, parenthesis or other character that needs percent-encoding (write `%20`, not a space).

A trailing `/` is removed. Any other value is rejected, with `preview.argocdURL` in the error. A null means unset. It does not need `preview.domain`.

When set, the preview's PR comment ends, before the `---` line, with this link:

``🔎 [All apps in Argo CD](<argocdURL>/applications?labels=app.kubernetes.io%2Finstance%3D<slug>)``

It opens Argo CD's application list filtered on the label `app.kubernetes.io/instance=<slug>`, with its live sync and health status. Every service and dependency Application of the preview carries that label. Only that comment carries the link, not the teardown, expiry or refusal comments.

Setting or changing it writes no preview file. A comment gets the link at its next refresh: a deploy, a teardown that keeps the preview, or a sync that refreshes comments.

badil does not check that the URL is reachable, and Argo CD asks for a login as usual.

## Per-preview URLs

`preview.domain`, `preview.tls` and `preview.expose` give each exposed service of a preview the host `<service>-<slug>.<domain>`. One wildcard DNS record (and, for TLS, one wildcard certificate) for `*.<domain>` covers every preview.

badil uses the host in two places:

- It writes the host into each exposed entry's `host` in `values.yaml`. `preview-apps` passes it to the service's Application as the Helm parameter named by the entry's `chart.hostParam` (`app.ingress.host` by default).
- It appends ` · [<host><path>](<scheme>://<host><path>)` to the service's line in the PR comment.

`tls` only picks the link scheme. Ingress TLS, the ingress class and annotations stay in the service chart. Services not in `expose` (non-HTTP services, for example) get no host and no link.

### Length caps

Two values are cut when too long, and a hash of the full value (6 hex characters) is appended:

- a slug longer than 53 characters: Helm's release-name limit, which Argo CD hits through the ApplicationSet's `releaseName`;
- a `<service>-<slug>` host label longer than 63 characters: the DNS label limit.

For example, the branch `feature/very-long-branch-name-that-keeps-going-and-going-past-the-helm-limit` gets the slug `feature-very-long-branch-name-that-keeps-going-5bec34`. The service `front-office-web` in it gets the host `front-office-web-feature-very-long-branch-name-that-keep-395ff3.<domain>`.

### Upgrade order

Upgrade badil (0.10.0 or later) first, then `preview-env` (0.3.0 or later). `preview-env` 0.3.0 reads only `host`, so an entry without it gets no ingress host. Preview files written by an older badil carry no `host` until their next write (deploy or sweep).

## Preview limits

badil and the `preview-apps` chart create no quota or policy objects. Per-preview limits belong to the cluster's policy layer. It selects preview namespaces by the label `app.kubernetes.io/part-of: preview-environments`, or by labels set in `namespace` of `badil.yaml`.

[`examples/kyverno/preview-guardrails.yaml`](../examples/kyverno/preview-guardrails.yaml) is one Kyverno `GeneratingPolicy` (Kyverno v1.19.0 or later).

It creates a ResourceQuota, a LimitRange and an ingress NetworkPolicy in every preview namespace, and keeps them in sync with the policy. Kyverno creates them, not Argo CD, so the `previews` AppProject is unchanged. Teardown deletes them with the namespace.

A quota on compute resources (`requests.cpu`, `requests.memory`, `limits.memory`) refuses pods that do not set them, and `charts/app` ships `resources: {}`. Ship a LimitRange with default requests and limits next to such a quota, as the example does.

## Preview file

badil writes `previews/<slug>/values.yaml`. [CONTRACT](CONTRACT.md#v1beta1-file) defines every field and the strict rules of v1beta1. When badil writes it is in [Preview file writes](LIFECYCLE.md#preview-file-writes).

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

The file is self-contained. A renderer needs only it to know each service's repo, revision, image tag and chart settings, each dependency's chart, version and values, and the namespace metadata.

### `preview-apps` values

The ApplicationSet passes the preview file as the only value file of `preview-apps`, and its `argocdNamespace` as a parameter. `preview-apps` creates its AppProject and Applications in the namespace set by `argocdNamespace` (default `argocd`).

`preview-apps` ships `values.schema.json`, generated from badil's Go types (`make schema`). Helm validates the values (the preview file and `argocdNamespace`) against it on every render, and Argo CD shows a sync error. A violation fails the render with the field's JSON pointer, for example `at '': missing property 'apiVersion'`. CI fails when the schema is out of date.

The root is closed. A key other than `apiVersion`, `gitopsRepoURL`, `namespace`, `services`, `dependencies` and `argocdNamespace` fails the render, for example a leftover `serviceRepos` or `preview`, with `at '': additional properties '<key>' not allowed`. A missing `gitopsRepoURL` or `dependencies` fails it too.

## Service repo contract

The `preview-apps` chart ([`application.yaml`](../charts/preview-apps/templates/application.yaml)) renders one Argo CD Application per entry of the preview file, from the entry's `repoURL` and `branch`. Each service repo provides the following, with its `badil.yaml` override:

| Item | Default | Override in `badil.yaml` |
|---|---|---|
| A Helm chart | at `k8s/` | `services.<name>.chart.path` |
| A branch, used as `targetRevision` by branch-tracking services | `main` | `services.<name>.branch` |
| A Helm parameter for the image tag | `app.image.tag`: the service chart declares the [`app`](../charts/app) chart as a dependency aliased `app` | `services.<name>.chart.imageTagParam` |
| Images tagged as the service's `imageTag` format renders the git commit SHA | the full SHA | `services.<name>.imageTag` |
| For services in `preview.expose`: ingress enabled, and a Helm parameter setting the ingress host | `app.ingress.host` | `services.<name>.chart.hostParam` |
| Extra values files, applied in order | none | `services.<name>.chart.valueFiles` |

Pinned services use the PR head SHA (label flow) or the `commit_sha` sent to `/v1/deploy` as image tag source. Branch-tracking services use the revision Argo CD syncs.

An exposed service without ingress, or without the host parameter, gets a dead PR-comment link. A service whose image is not built per commit (for example a database) may ignore the image tag parameter.

Argo CD joins a relative values file path to the chart path, and a path starting with `/` to the repo root. A missing file fails the sync.

`hostParam` is passed as a Helm `--set`, key verbatim. A list-indexed name such as `ingress.hosts[0].host` replaces the chart's default list. The full list (with its `paths`) and `ingress.enabled: true` must then come from a file in `valueFiles`, and the parameter only sets that element's field.

The ApplicationSet example sources `preview-apps` from an OCI registry (`oci://` with `path: "."`), which requires Argo CD 3.1 or later.

## Server environment variables

badil reads these environment variables:

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_APP_ID` | yes | | GitHub App ID |
| `GITHUB_PRIVATE_KEY_PATH` or `GITHUB_PRIVATE_KEY` | yes, one of them | | GitHub App private key: PEM file path or inline PEM |
| `GITHUB_WEBHOOK_SECRET` | yes | | GitHub App webhook secret |
| `GITHUB_INSTALLATION_ID` | yes | | the only installation badil accepts webhooks from |
| `GITOPS_REPO` | yes | | the gitops repo, `owner/repo` |
| `BADIL_DEPLOY_TOKEN` | no | unset | enables `/v1/deploy` and `/v1/sync` |
| `LISTEN_ADDR` | no | `:8080` | API and webhook listener |
| `METRICS_ADDR` | no | `:9090` when unset | `/metrics` listener; set to `""` to disable `/metrics` |
| `ARGOCD_WEBHOOK_TOKEN` | no | unset | enables `/webhooks/argocd` |
| `CLEANUP_INTERVAL` | no | unset: no in-server sweep | `serve` only: how often the in-server sweep runs, a Go duration of at least 1m (e.g. `1h`) |
| `CLEANUP_TTL` | no | unset: no expiry | `cleanup`, and `serve` when `CLEANUP_INTERVAL` is set: sweep expiry, a Go duration (e.g. `168h`) |

### GitHub App

The GitHub App needs these permissions: Contents R/W, Pull requests W, Metadata R.

It subscribes to the events `pull_request` and `push`. Installation events are delivered automatically.
