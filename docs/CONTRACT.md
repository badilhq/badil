# Preview contract

badil and the renderer of a preview never talk to each other: badil writes one file per preview into the gitops repo, and a renderer turns it into running workloads. This page is that file's contract, `badil.dev/v1`, and what a renderer must do with it. [`preview-env`](../charts/preview-env), driven by the example [ApplicationSet](../examples/argocd/applicationset-previews.yaml), is the renderer badil ships; any other renderer that follows this page works with badil unchanged.

## Files

At the root of the gitops repo:

| File | Written by | Read by |
|---|---|---|
| `badil.yaml` | you | badil only: the service catalog and the `preview` block |
| `preview-env.yaml` | you | the `preview-env` renderer only: `serviceCharts` and `previewNamespace` |
| `previews/<slug>/values.yaml` | badil | any renderer: one preview, in the format below |

A renderer needs only `previews/<slug>/values.yaml`: it carries each service's repo, revision and image tag. `badil.yaml` and `preview-env.yaml` are described in [ARCHITECTURE](ARCHITECTURE.md#gitops-repo-contract).

## Lifecycle

- **Create.** badil creates `previews/<slug>/values.yaml` on the first deploy to a slug, from the label flow or `POST /v1/deploy`.
- **Update.** badil rewrites the file on a deploy or a teardown that changes its content. A label-flow deploy that would change only `metadata.updated-at` writes nothing, and neither does a teardown that unpins nothing, so a renderer must not expect a commit per deploy. A deploy write drops every entry whose service left the catalog, pinned or not: a renderer must remove what it deployed for an entry that disappears.
- **Delete.** badil deletes the file, and with it `previews/<slug>/`, when no service of the preview is pinned any more (teardown or cleanup sweep), when the preview expires (`CLEANUP_TTL`), or when the sweep tears down the last PR that pinned it. See [Teardown flow](ARCHITECTURE.md#http-api) and [Cleanup sweep](ARCHITECTURE.md#cleanup-sweep).

The slug is the slugified head branch, or the name of a `<label>:<name>` label: lowercase `a-z` and `0-9`, every other run of characters replaced by one `-`, no leading or trailing `-`. A slug is a DNS label of at most 53 characters (Helm's release-name limit): a longer one is cut and suffixed with `-` and 6 hex characters of a hash of the full slug.

## v1 file

```yaml
apiVersion: badil.dev/v1
services:
  - name: api
    repoURL: https://github.com/acme/platform.git
    branch: main
    commitSha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
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
```

| Field | Type | Present | Meaning |
|---|---|---|---|
| `apiVersion` | string | always | `badil.dev/v1` |
| `services` | list | always | one entry per catalog service of `badil.yaml`, in catalog order |
| `services[].name` | string | always | the service name, the key of `services` in `badil.yaml` |
| `services[].repoURL` | string | always | the service's git URL, `services.<name>.repo` of `badil.yaml` as written |
| `services[].branch` | string | always | the branch the service tracks when not pinned: `services.<name>.branch` of `badil.yaml`, else `main` |
| `services[].commitSha` | string | pinned entries only | the full commit SHA the service is pinned at; also its image tag |
| `services[].host` | string | exposed services only (`preview.expose` of `badil.yaml`, with `preview.domain` set) | the ingress host `<service>-<slug>.<domain>` |
| `services[].metadata` | object | exactly when `commitSha` is | the PR that pinned the entry, every key below required |
| `metadata.pr-author` | string | | author of the PR |
| `metadata.pr-url` | string | | URL of the PR; teardowns match pins to a PR by it |
| `metadata.pr-number` | string | | number of the PR |
| `metadata.created-at` | string | | RFC 3339 time the entry was pinned, kept across later pins until it is unpinned |
| `metadata.updated-at` | string | | RFC 3339 time of the entry's last deploy; the sweep expires a preview by it |
| `metadata.vcs.ref.name` | string | | head branch of the PR |
| `metadata.cicd.pipeline.run.url` | string | | CI run that called `/v1/deploy`; `""` for label deploys |

The contract is strict: `apiVersion` must be `badil.dev/v1`, every object is closed (an unknown field at any level is invalid), every field has the type shown, a null value is invalid, and an entry with `commitSha` but no `metadata`, or `metadata` but no `commitSha`, is invalid. Every string badil writes reads back as a string under YAML 1.1: values such as `"42"` or `"on"` are quoted.

The JSON Schema of the contract is [`schema/preview.v1.schema.json`](../schema/preview.v1.schema.json) (draft 2020-12), generated from badil's Go types.

## Renderer obligations

A renderer of `badil.dev/v1`:

1. Runs one environment per `previews/<slug>/` directory, named by the slug: the slug is its namespace and its release name.
2. Deploys a pinned entry (with `commitSha`) from `repoURL` at `commitSha`, with the image tag `commitSha`.
3. Deploys an unpinned entry from `repoURL`, tracking `branch`.
4. Passes `host`, when present, to the service as its ingress host.
5. Deletes everything of the preview when its directory disappears, its data included: `preview-env` deletes the preview namespace, which is how per-preview data is dropped.
6. Rejects a file whose `apiVersion` it does not support, instead of rendering it.

## What badil never does

- Touch the cluster: it writes to the gitops repo and to PR comments, and the renderer does every deploy and deletion.
- Build images: each service's CI pushes images tagged with the full commit SHA.
- Read, provision or drop data stores: data in the preview namespace goes with it, data outside it belongs to whatever provisions it. See [Preview data](ARCHITECTURE.md#preview-data).

## Bring your own renderer

- Replace the template of the example ApplicationSet, or the whole ApplicationSet, with your renderer: Kustomize, your own app-of-apps, or another tool that watches `previews/*/values.yaml` in the gitops repo.
- Validate every file with [`schema/preview.v1.schema.json`](../schema/preview.v1.schema.json) before rendering it, so an unsupported `apiVersion` or an invalid file is refused, not half-deployed.
- Test against [`testdata/contract/v1/`](../testdata/contract/v1): every file of `valid/` must render and every file of `invalid/` must be refused. The `preview-env/` goldens show what `preview-env` renders from each valid file with the settings of `testdata/contract/v1/preview-env.yaml`.

### ApplicationSet without Helm

An ApplicationSet alone can produce one Application per service: a matrix of a git files generator over `previews/*/values.yaml` and a [list generator](https://github.com/argoproj/argo-cd/blob/v3.6.1/docs/operator-manual/applicationset/Generators-List.md#dynamically-generated-elements) fed by `elementsYaml: "{{ .services | toJson }}"`. It loses two things `preview-env` provides:

- **No per-preview AppProject.** An ApplicationSet creates Applications only, so every preview shares one AppProject.
- **Namespaces outlive their previews.** A namespace created by `CreateNamespace=true` is not deleted with its Applications. One Application of the preview must own it, through [`managedNamespaceMetadata`](https://github.com/argoproj/argo-cd/blob/v3.6.1/docs/user-guide/sync-options.md#namespace-metadata) with an `argocd.argoproj.io/tracking-id` annotation; otherwise the namespace, and the data in it, stays after badil deletes the preview, which breaks obligation 5.
