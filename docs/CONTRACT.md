# Preview contract

badil and the renderer of a preview never talk to each other: badil writes one file per preview into the gitops repo, and a renderer turns it into running workloads. This page is that file's contract, `badil.dev/v1beta1`, and what a renderer must do with it. [`preview-env`](../charts/preview-env), driven by the example [ApplicationSet](../examples/argocd/applicationset-previews.yaml), is the renderer badil ships; any other renderer that follows this page works with badil unchanged.

## Versioning

`v1beta1` is a beta: a later version (`v1beta2`, or `v1`) may change the file incompatibly. Each such change ships with a `preview-env` version that accepts only the new `apiVersion`, and badil's cleanup sweep rewrites every preview file that has a pin to it within one sweep after badil is upgraded (the same sweep deletes files without a pin). A renderer refuses an `apiVersion` it does not support.

## Files

At the root of the gitops repo:

| File | Written by | Read by |
|---|---|---|
| `badil.yaml` | you | badil only: the service catalog with each service's chart settings and image tag format, the dependencies, the namespace metadata and the `preview` block |
| `previews/<slug>/values.yaml` | badil | any renderer: one preview, in the format below |

A renderer needs only `previews/<slug>/values.yaml`: badil writes into it everything `badil.yaml` says about rendering, so it carries each service's repo, revision, image tag and chart settings, each dependency's chart, version and values, the gitops repo URL and the namespace metadata. `badil.yaml` is described in [ARCHITECTURE](ARCHITECTURE.md#gitops-repo-contract).

## Lifecycle

- **Create.** badil creates `previews/<slug>/values.yaml` on the first deploy to a slug, from the label flow or `POST /v1/deploy`.
- **Update.** badil rewrites the file on a deploy, a teardown, or a cleanup sweep sync (the file differs from `badil.yaml`), when that changes its content. A label-flow deploy that would change only `metadata.updated-at` writes nothing, and neither does a teardown that unpins nothing or a sync of a file that already matches, so a renderer must not expect a commit per deploy. When a service is removed from `badil.yaml`, its unpinned entries are dropped by the next write; a pinned entry stays until its PR's teardown, which drops it: a renderer must remove what it deployed for an entry that disappears. A dependency removed from `badil.yaml` is dropped by the next write, the same way. A `badil.yaml` change (a chart setting, an image tag format, a dependency, the namespace metadata) reaches every preview the same way, by its next write.
- **Delete.** badil deletes the file, and with it `previews/<slug>/`, when no service of the preview is pinned any more (teardown or cleanup sweep), when the preview expires (`CLEANUP_TTL`), or when the sweep tears down the last PR that pinned it. See [Teardown flow](ARCHITECTURE.md#http-api) and [Cleanup sweep](ARCHITECTURE.md#cleanup-sweep).

The slug is the slugified head branch, or the name of a `<label>:<name>` label: lowercase `a-z` and `0-9`, every other run of characters replaced by one `-`, no leading or trailing `-`. A slug is a DNS label of at most 53 characters (Helm's release-name limit): a longer one is cut and suffixed with `-` and 6 hex characters of a hash of the full slug.

## v1beta1 file

```yaml
apiVersion: badil.dev/v1beta1
gitopsRepoURL: https://github.com/acme/gitops.git
namespace:
  labels:
    team: platform
services:
  - name: api
    repoURL: https://github.com/acme/platform.git
    branch: main
    commitSha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
    imageTag: sha-a1b2c3d
    chart:
      path: deploy/chart
      imageTagParam: app.image.tag
      hostParam: app.ingress.host
      valueFiles:
        - values-preview.yaml
    host: api-feature-pricing.preview.example.com
    metadata:
      pr-author: octocat
      pr-url: https://github.com/acme/platform/pull/42
      pr-number: "42"
      created-at: "2026-05-26T10:00:00Z"
      updated-at: "2026-05-26T11:30:00Z"
      vcs.ref.name: feature/pricing
      cicd.pipeline.run.url: https://github.com/acme/platform/actions/runs/1001
  - name: worker
    repoURL: https://github.com/acme/platform.git
    branch: develop
    imageTag: sha-${ARGOCD_APP_REVISION_SHORT}
    chart:
      path: k8s/
      imageTagParam: app.image.tag
      hostParam: app.ingress.host
dependencies:
  - name: postgres
    chart:
      repoURL: oci://registry-1.docker.io/bitnamicharts/postgresql
      version: 16.4.2
    values:
      auth:
        password: preview
    valueFiles:
      - deps/postgres.yaml
  - name: mailpit
    chart:
      repoURL: https://jouve.github.io/charts
      name: mailpit
      version: 0.20.0
    hostParam: ingress.hostname
    host: mailpit-feature-pricing.preview.example.com
```

| Field | Type | Present | Meaning |
|---|---|---|---|
| `apiVersion` | string | always | `badil.dev/v1beta1` |
| `gitopsRepoURL` | string | always | the gitops repo, `https://github.com/<GITOPS_REPO>.git`: where `dependencies[].valueFiles` are read |
| `namespace` | object | when `badil.yaml` sets `namespace` labels or annotations | extra metadata of the preview namespace |
| `namespace.labels` | map of strings | | extra labels; never one of `app.kubernetes.io/name`, `app.kubernetes.io/instance`, `app.kubernetes.io/part-of`, `app.kubernetes.io/managed-by` |
| `namespace.annotations` | map of strings | | extra annotations; never `argocd.argoproj.io/sync-wave` |
| `services` | list | always | every service of `badil.yaml` (only the pinned ones and what they need when `badil.yaml` declares `needs`), in catalog order, then the pinned entries of services removed from it, in file order, as of the file's last write. A file written before a catalog change keeps its old entries until the next write (deploy or sweep) |
| `services[].name` | string | always | the service name, the key of `services` in `badil.yaml`; a kept pinned entry of a removed service keeps its last name |
| `services[].repoURL` | string | always | the service's git URL, `services.<name>.repo` of `badil.yaml` as written; a kept pinned entry of a removed service keeps its last `repoURL` |
| `services[].branch` | string | always | the branch the service tracks when not pinned: `services.<name>.branch` of `badil.yaml`, else `main`; a kept pinned entry of a removed service keeps its last `branch` |
| `services[].commitSha` | string | pinned entries only | the commit SHA the service is pinned at: the PR head SHA in the label flow, the `commit_sha` sent to `/v1/deploy` otherwise |
| `services[].imageTag` | string | always | the service's image tag: the `imageTag` format of `badil.yaml` (`services.<name>.imageTag`, else the root `imageTag`, else `{sha}`) expanded. On a pinned entry it holds `commitSha` or its first 7 or 8 characters; on an unpinned one, `${ARGOCD_APP_REVISION}`, `${ARGOCD_APP_REVISION_SHORT}` or `${ARGOCD_APP_REVISION_SHORT_8}`. A kept pinned entry of a removed service keeps its last `imageTag` |
| `services[].chart` | object | always | how the service's chart is rendered: `services.<name>.chart` of `badil.yaml`, defaults applied; a kept pinned entry of a removed service keeps its last `chart` |
| `chart.path` | string | always | chart path in the service repo, default `k8s/` |
| `chart.imageTagParam` | string | always | Helm parameter set to `imageTag`, default `app.image.tag` |
| `chart.hostParam` | string | always | Helm parameter set to `host`, default `app.ingress.host` |
| `chart.valueFiles` | list of strings | when set in `badil.yaml` | extra values files, applied in order |
| `services[].host` | string | exposed services only (`preview.expose` of `badil.yaml`, with `preview.domain` set) | the ingress host `<service>-<slug>.<domain>` |
| `services[].metadata` | object | exactly when `commitSha` is | the PR that pinned the entry, every key below required |
| `metadata.pr-author` | string | | author of the PR |
| `metadata.pr-url` | string | | URL of the PR; teardowns match pins to a PR by it |
| `metadata.pr-number` | string | | number of the PR |
| `metadata.created-at` | string | | RFC 3339 time the entry was pinned, kept across later pins until it is unpinned |
| `metadata.updated-at` | string | | RFC 3339 time of the entry's last deploy; the sweep expires a preview by it |
| `metadata.vcs.ref.name` | string | | head branch of the PR |
| `metadata.cicd.pipeline.run.url` | string | | CI run that called `/v1/deploy`; `""` for label deploys |
| `dependencies` | list | always, may be empty | every dependency of `badil.yaml` (only those the pinned services need when `badil.yaml` declares `needs`), in its order, except one named like a kept pinned entry of a removed service, left out until that entry is dropped |
| `dependencies[].name` | string | always | the dependency name, the key of `dependencies` in `badil.yaml`; never the name of a service entry of the file |
| `dependencies[].chart` | object | always | where the chart comes from |
| `chart.repoURL` | string | always | an `oci://` chart reference, or an `https://` Helm repository |
| `chart.name` | string | exactly when `chart.repoURL` is not `oci://` | the chart name in the Helm repository |
| `chart.version` | string | always | the chart version |
| `dependencies[].values` | object | when set in `badil.yaml` | values of the chart, any object (the only open object of the file) |
| `dependencies[].valueFiles` | list of strings | when set in `badil.yaml` | values files, paths from the root of the gitops repo, applied in order |
| `dependencies[].hostParam` | string | exactly when `host` is | Helm parameter set to `host` |
| `dependencies[].host` | string | exposed dependencies only | the ingress host `<name>-<slug>.<domain>` |

The contract is strict: `apiVersion` must be `badil.dev/v1beta1`, every object but `dependencies[].values` is closed (an unknown field at any level is invalid), every field has the type shown, a null value is invalid, and an entry with `commitSha` but no `metadata`, or `metadata` but no `commitSha`, is invalid, as is a dependency with `chart.name` and an `oci://` `chart.repoURL`, or without `chart.name` and another one, or with only one of `hostParam` and `host`. Every string badil writes reads back as a string under YAML 1.1: values such as `"42"` or `"on"` are quoted.

The JSON Schema of the contract is [`schema/preview.v1beta1.schema.json`](../schema/preview.v1beta1.schema.json) (draft 2020-12), generated from badil's Go types.

## Renderer obligations

A renderer of `badil.dev/v1beta1`:

1. Runs one environment per `previews/<slug>/` directory, named by the slug: the slug is its namespace and its release name.
2. Deploys a pinned entry (with `commitSha`) from `repoURL` at `commitSha`.
3. Deploys an unpinned entry from `repoURL`, tracking `branch`.
4. Renders each service's chart at `chart.path`, with `chart.valueFiles` applied in order, and passes `imageTag` as written, as a string, to the parameter `chart.imageTagParam`. It resolves `${ARGOCD_APP_REVISION}`, `${ARGOCD_APP_REVISION_SHORT}` and `${ARGOCD_APP_REVISION_SHORT_8}` in it to the deployed revision (the full SHA, its first 7 and its first 8 characters), as Argo CD does.
5. Passes `host`, when present, to the parameter `chart.hostParam`.
6. Deploys each dependency, in the preview namespace with its `name` as release name, from `chart.repoURL` at `chart.version`: the OCI chart reference, or the chart `chart.name` of the Helm repository. It passes `values` as the chart's values, applies `valueFiles` in order from `gitopsRepoURL` at its default branch, passes `host`, when present, to the parameter `hostParam`, and sets no image tag.
7. Adds `namespace.labels` and `namespace.annotations`, when present, to the preview namespace.
8. Deletes everything of the preview when its directory disappears, its data included: `preview-env` deletes the preview namespace, which is how per-preview data is dropped.
9. Rejects a file whose `apiVersion` it does not support, instead of rendering it.

## What badil never does

- Touch the cluster: it writes to the gitops repo and to PR comments, and the renderer does every deploy and deletion.
- Build images: each service's CI pushes images tagged as its `imageTag` format renders the commit SHA.
- Read, provision or drop data stores: data in the preview namespace goes with it, data outside it belongs to whatever provisions it. See [Preview data](ARCHITECTURE.md#preview-data).

## Bring your own renderer

- Replace the template of the example ApplicationSet, or the whole ApplicationSet, with your renderer: Kustomize, your own app-of-apps, or another tool that watches `previews/*/values.yaml` in the gitops repo.
- Validate every file with [`schema/preview.v1beta1.schema.json`](../schema/preview.v1beta1.schema.json) before rendering it, so an unsupported `apiVersion` or an invalid file is refused, not half-deployed.
- Test against [`testdata/contract/v1beta1/`](../testdata/contract/v1beta1): every file of `valid/` must render and every file of `invalid/` must be refused. The `preview-env/` goldens show what `preview-env` renders from each valid file alone.

### ApplicationSet without Helm

An ApplicationSet alone can produce one Application per service: a matrix of a git files generator over `previews/*/values.yaml` and a [list generator](https://github.com/argoproj/argo-cd/blob/v3.6.1/docs/operator-manual/applicationset/Generators-List.md#dynamically-generated-elements) fed by `elementsYaml: "{{ .services | toJson }}"`. It loses two things `preview-env` provides:

- **No per-preview AppProject.** An ApplicationSet creates Applications only, so every preview shares one AppProject.
- **Namespaces outlive their previews.** A namespace created by `CreateNamespace=true` is not deleted with its Applications. One Application of the preview must own it, through [`managedNamespaceMetadata`](https://github.com/argoproj/argo-cd/blob/v3.6.1/docs/user-guide/sync-options.md#namespace-metadata) with an `argocd.argoproj.io/tracking-id` annotation; otherwise the namespace, and the data in it, stays after badil deletes the preview, which breaks obligation 8.
