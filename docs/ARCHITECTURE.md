# Architecture

Self-hosted, single-tenant. Everything runs in the user's cluster; badil talks to GitHub through the user's own GitHub App and never touches the cluster directly: ArgoCD does the deploys.

```mermaid
flowchart LR
  pr["GitHub<br/>pull request<br/>labelled preview"]
  subgraph you["Your side"]
    badil["badil"] -->|write| repo[("gitops repo")] -->|watch| argo["Argo CD"] -->|deploy| ns["Preview<br/>namespace"]
  end
  pr -->|webhook| badil
  badil -.->|PR comment| pr
  argo -.->|deployment status| pr

  classDef github stroke:#8c959f,stroke-width:2px
  classDef badil stroke:#1a7f37,stroke-width:2px
  classDef repo stroke:#8250df,stroke-width:2px
  classDef argo stroke:#EF7B4D,stroke-width:2px
  classDef k8s stroke:#326CE5,stroke-width:2px
  class pr github
  class badil badil
  class repo repo
  class argo argo
  class ns k8s
```

- Each service repo's CI builds and pushes its images, and makes no deploy call.
- The deployment status is optional: Argo CD's notifications controller posts it as GitHub Deployments, through its own GitHub App (see [Preview deployments on GitHub](#preview-deployments-on-github)).
- Closing or unlabelling a PR, and the [cleanup sweep](#cleanup-sweep), unpin services or delete preview files; a push changing `badil.yaml`, or `POST /v1/sync`, resyncs every preview (see [Config sync](#config-sync)); Argo CD prunes what was removed.
- `POST /v1/deploy` and `POST /v1/sync` exist only when `BADIL_DEPLOY_TOKEN` is set.

## Components

badil is a few binaries and charts. Each service repo adds its own chart.

| Component | Where | Role |
|---|---|---|
| `badil serve` | this repo | chi HTTP server: GitHub and Argo CD webhooks, and the optional deploy and sync endpoints. See the [HTTP API reference](API.md). |
| `badil cleanup` | this repo | One-shot [cleanup sweep](#cleanup-sweep), run by a CronJob (hourly). |
| `badil bootstrap-app` / `verify-app` | this repo | Create a GitHub App via the manifest flow / check credentials. |
| `preview-apps` chart | this repo, [`charts/preview-apps`](../charts/preview-apps) | The [renderer](CONTRACT.md#preview-contract) badil ships: an AppProject, a Namespace, and one Argo CD Application per service and per dependency. Application annotations `preview.badil.dev/*` carry the PR metadata. |
| ApplicationSet | this repo, [`examples/argocd`](../examples/argocd) | Git directory generator on `previews/*`. |
| Service chart | each service repo | Built on the `app` chart, [`charts/app`](../charts/app). See the [Service repo contract](CONFIGURATION.md#service-repo-contract). |
| `badil` chart | this repo, [`charts/badil`](../charts/badil) | Installs `badil serve` (Deployment, Service, optional Ingress) and the cleanup CronJob (off by default). |

## How a preview flows

The steps from label to teardown:

1. A service repo's CI builds and pushes the image of each commit.
2. A user adds the preview label to a PR. GitHub sends the `pull_request` webhook.
3. badil reads the PR and its labels, and picks the target preview: the PR's branch, or a `preview:<name>` label.
4. badil writes `previews/<slug>/values.yaml` to the gitops repo. The PR's services are pinned at its head SHA, and every other service tracks its branch.
5. badil comments on the PR, with a link per exposed service.
6. The ApplicationSet sees the new directory. Argo CD renders `preview-apps` and deploys the preview namespace, one Application per service and one per dependency.
7. Each push to the PR pins the new head SHA, and Argo CD syncs it.
8. Closing the PR or removing its label unpins its services. When no pin is left, badil deletes the file, and Argo CD prunes the preview.

The [cleanup sweep](#cleanup-sweep) catches what events missed. Every step is detailed in the [Preview lifecycle reference](LIFECYCLE.md).

## The gitops repo is the interface

badil never talks to the cluster or to Argo CD. It reads one file, `badil.yaml`, and writes one file per preview. Argo CD does every deploy and deletion.

`badil.yaml` is the only preview config, and badil is its only reader. badil writes everything a renderer needs into each preview file. The renderer reads only that file, so it can be swapped: [CONTRACT](CONTRACT.md) publishes the file for any renderer.

Keys: [Configuration reference](CONFIGURATION.md).

## Pinned and branch-tracking services

A preview runs the full stack, but only the PR's services run its code. Each service runs in one of two modes:

- **Pinned:** the service runs at a commit SHA, the PR head. It changes only when badil writes a new SHA.
- **Branch-tracking:** the service follows its branch (default `main`). Argo CD resolves the image tag to the commit it syncs.

So each service's CI must push an image tagged with the commit SHA, as its `imageTag` format renders it.

How each mode is rendered: [Service modes](LIFECYCLE.md#service-modes). Image tag formats: [`imageTag`](CONFIGURATION.md#imagetag).

### Images that do not exist yet

badil does not check the registry. A service can briefly sit in `ImagePullBackOff` because its image tag does not exist yet:

- a branch-tracking service, when Argo CD syncs a new commit on its branch before that commit's CI has pushed the image;
- a pinned service, because badil pins the PR head SHA as soon as the label or a push arrives. The pod starts when the image is in the registry, and the PR comment says so.

This is accepted because it resolves without intervention. The kubelet keeps retrying the pull, with a back-off capped at 300 seconds. The Applications sync with `selfHeal: true`, and Argo CD polls Git every 3 minutes by default.

These alternatives were considered and not chosen:

- **badil re-pins branch-tracking services** to the branch's new SHA once its image exists. It removes the race, but every build of that branch has to rewrite every active preview, with concurrent writes to the same files.
- **Argo CD webhook from CI** after the image push, instead of polling. It needs CI-to-cluster network access and an exposed Argo CD API, and does not help while Argo CD also polls.
- **Argo CD Image Updater.** Another controller with registry credentials and write-back config, built for tracking the latest tag rather than previews.
- **Init container that waits for the image.** It adds registry access and a wait to every service chart, and still delays startup.
- **badil waits for CI checks or for the image before pinning.** It needs an extra GitHub App permission (Checks) or registry credentials. It delays the other services of the preview, which today start while CI builds, and gains no time (measured: CI 3m34s, pinned service Healthy at 216 s).

## Shared previews

A preview is named by a slug: the PR's branch, or the name of a `preview:<name>` label. PRs from several repos with the same branch or the same named label share one preview. Each pins its own services.

A preview holds one PR per service, so two PRs never fight over a service. `preview.maxPreviews` can cap the number of previews.

Rules: [Target preview](LIFECYCLE.md#target-preview), [One PR per service](LIFECYCLE.md#one-pr-per-service) and [Preview cap](LIFECYCLE.md#preview-cap).

## Dependencies and needs

A service often needs a database, a cache or a mail catcher. `dependencies` are upstream charts that badil adds to every preview, next to the services. They are never pinned, get no image tag, and need no git repo of their own.

By default every preview runs every service and dependency. `needs` trims a preview to what its PRs use: a service or dependency lists what it calls, and a preview runs only its pinned services and what they need. badil does not infer `needs`.

Keys: [`dependencies`](CONFIGURATION.md#dependencies) and [`needs`](CONFIGURATION.md#servicesnameneeds-and-dependenciesnameneeds). Rules: [Dependencies](LIFECYCLE.md#dependencies) and [Needs](LIFECYCLE.md#needs).

## Path filters

In a monorepo, one repo holds several services. Without path filters, a labelled PR pins every service of its repo.

`paths` lists the files of a repo that belong to a service. A PR then pins only the services whose files it changes. badil does not infer `paths`, and the PR comment shows which services are pinned.

Key: [`paths`](CONFIGURATION.md#servicesnamepaths). Rules: [Path filters](LIFECYCLE.md#path-filters).

## Cleanup sweep

The cleanup sweep fixes what missed webhooks leave behind, for example when badil was down or a label was renamed.

It runs in the server every `CLEANUP_INTERVAL`, or as the `badil cleanup` CronJob. For each preview, it deletes the file when no service is pinned, or when no deploy happened within `CLEANUP_TTL`. Otherwise it unpins closed PRs, and open PRs whose label moved, conflicts or is gone. It then rewrites every preview it keeps to match `badil.yaml`.

Rules: [Cleanup sweep](LIFECYCLE.md#cleanup-sweep).

## Config sync

A change to `badil.yaml` should reach running previews without waiting for a push to each PR. Config sync rewrites every preview to match `badil.yaml`, the way the sweep's last step does.

It runs on a push to the gitops repo's default branch, when the App subscribes to `push`, and on `POST /v1/sync`. Without either, the next cleanup sweep applies the change.

Rules: [Config sync](LIFECYCLE.md#config-sync).

## Preview deployments on GitHub

Argo CD, not badil, knows whether a preview service is healthy. So Argo CD's notifications controller, not badil, posts GitHub Deployments for pinned services, through its own GitHub App. The same controller sends badil the spin-up time of each service.

Triggers and limitations: [Preview deployments on GitHub](OPERATIONS.md#preview-deployments-on-github).

## Preview data

badil never reads, writes or drops data stores.

- A preview's identity is its slug: both the Helm release name of `preview-apps` and the preview namespace.
- Each service's release name is the service name, so per-preview data is keyed by namespace.
- Data that lives in the preview namespace is pruned with it on teardown, sweep or expiry.
- Data outside the namespace is owned, and dropped, by whatever provisions it.
- The [`app`](../charts/app) chart only offers `envFrom` and `initContainers` to wire a data provider.
