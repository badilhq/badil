<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.png">
    <img alt="badil" src="docs/assets/logo-light.png" width="232">
  </picture>
</h1>

Per-pull-request preview environments for teams running Kubernetes and ArgoCD. Self-hosted, open source.

Every branch gets its own isolated, full-stack environment: the services changed in the PR run at their PR version, every other service runs at its branch (default `main`). badil writes the desired state to your gitops repo; your ArgoCD deploys it. badil never touches your previews directly: its only cluster access is its own status ConfigMaps (`badil-status-<slug>`), in its own namespace.

## Why

When code is written at machine speed, the bottleneck is validating and integrating it, and a shared staging environment becomes a queue. badil applies the practices from [AI Writes Code in Hours. It Still Takes Weeks to Ship.](https://medium.com/@aminebenseddik/ai-writes-code-in-hours-it-still-takes-weeks-to-ship-7fb07f87cc07):

- One isolated preview per branch, or per `preview:<name>` label, across repos: PRs on the same branch, or with the same `preview:<name>` label, share one preview.
- Changed services run at their PR commit, everything else at their branch (default `main`).
- CI builds once; badil only writes desired state. It never builds images and never reads or writes preview namespaces: it writes only its own status ConfigMaps, in its own namespace.
- Argo CD deploys from git.
- Previews are torn down when their PR closes or the label is removed, and swept so they don't accumulate: the sweep also unpins open PRs whose label moved, conflicts or is gone.

## How it works

1. CI in a service repo builds and pushes the image.
2. Adding the `preview` label to the PR makes badil write `previews/<slug>/values.yaml` (every catalog service, the PR's at its head SHA where it changes them when they declare `paths`; with `needs` in `badil.yaml`, only the preview's pinned services and what they need) and comment on the PR; a push to the labeled PR updates it. With `paths` in `badil.yaml`, a labelled PR pins only the services it changes. The slug is the PR's branch, slugified; a `preview:<name>` label picks the preview name instead.
3. An ArgoCD ApplicationSet picks up `previews/*` and deploys one namespace per preview, one Application per service, and one per dependency: an upstream chart from an OCI registry or a Helm repository (postgres, redis) declared in `badil.yaml`, with no git repo of its own.
4. Removing the label or closing the PR unpins its service, or deletes the file when no PR remains, and ArgoCD prunes the preview.
5. `badil cleanup` (in the server on an interval, or an hourly CronJob) sweeps every preview: it deletes previews with no pinned service, or with no deploy within `CLEANUP_TTL` when set, and tears down the pins of closed PRs that step 4 missed, and of open PRs whose label moved, conflicts or is gone. It then rewrites each preview it keeps to match `badil.yaml`. ArgoCD prunes what it removes.

### Sharing a preview across repos

To test changes from several repos together, either:

- use the same branch name in every repo, or
- label every PR `preview:<name>` (for example `preview:pricing`), whatever its branch. Labels are per repo: create the `preview:<name>` label in each repo first; badil never creates labels.

Each PR pins its own services in the preview `<name>` (slugified), and every other service runs at its branch (default `main`).

- A named label wins over the plain `preview` label. A PR with two `preview:<name>` labels for different names gets no preview, and a comment asks to keep one.
- Moving a PR to another label (plain to named, named to named, named to plain) unpins it from its old preview before deploying it to the new one.
- A named preview and a branch preview with the same slug are the same preview: `preview:main` joins the preview of a branch called `main`.
- A preview holds one PR per service: a second PR from the same repo is refused, with a comment naming the PR that holds the service. Once that PR leaves the preview, push to the refused PR or re-add its label.

Manual trigger:

```
POST /v1/deploy
Authorization: Bearer $BADIL_DEPLOY_TOKEN

{ "service": "backend-1", "head_ref": "feature/pricing", "commit_sha": "<sha>",
  "pr_url": "...", "pr_number": "42", "pr_author": "...", "workflow_run_url": "..." }
```

Sync every preview, or one, with `badil.yaml` now, instead of waiting for the push webhook or the next sweep:

```
POST /v1/sync
Authorization: Bearer $BADIL_DEPLOY_TOKEN

{}  or  { "slug": "feature-pricing" }
→ { "synced": ["feature-pricing"], "unchanged": 3, "failed": [] }
```

A manual deploy always targets the slug of `head_ref`, and skips the one-PR-per-service check. The cleanup sweep keeps its pin only at the PR's target preview (the slug of its single `preview:<name>` label, or its branch slug when it has only the plain `preview` label), or at its branch slug when the PR has no preview label. A PR with conflicting `preview:<name>` labels keeps no pin. A webhook reconcile of the PR also unpins it from its branch slug and its named-label slugs other than its target.

## Install

From an empty cluster to a first preview: [docs/INSTALL.md](docs/INSTALL.md).

The `badil` CLI (`badil list`, `badil wait`) for CI jobs and agents: [Install the badil CLI](docs/INSTALL.md#install-the-badil-cli).

The gitops repo can start from the template [`badilhq/gitops-template`](https://github.com/badilhq/gitops-template).

## Status

Pre-1.0, and the v1 scope is complete. The full loop (label a PR, preview deployed by Argo CD, URLs tested, merge, teardown) runs end to end on a k3d testbed with a four-service demo.

Working: label-triggered previews per branch or per `preview:<name>` label across repos, per-service chart overrides, upstream chart dependencies, teardown when a PR closes or loses its label, a cleanup sweep with optional TTL (in the server or as a CronJob), the manual `/v1/deploy` endpoint, previews synced when `badil.yaml` changes (push webhook, or `/v1/sync`), PR comments with preview links, Prometheus metrics and a Grafana dashboard, preview status and spin-up time from the renderer's status callback, and GitHub deployments through Argo CD notifications.

Until 1.0, env vars, metric names, annotations and chart values can still change between minor releases; the [CHANGELOG](CHANGELOG.md) marks breaking changes.

## Usage

```bash
badil serve           # API + webhook server
badil cleanup         # stale preview cleanup
badil list            # list previews and their status
badil wait            # wait for a PR's or a slug's preview to be ready
badil bootstrap-app   # create a GitHub App (webhook from --webhook-url or an ngrok tunnel; see DEVELOPMENT.md)
badil verify-app      # check GitHub App credentials, list installations
badil version         # print the version (also badil --version)
```

badil reads its whole preview config from `badil.yaml` in the gitops repo (service catalog, chart settings, image tag format, dependencies, namespace metadata, `preview` block) and writes everything `preview-apps` needs into each preview file (see the [Configuration reference](docs/CONFIGURATION.md#badilyaml)).

The server's environment variables are listed in the [Configuration reference](docs/CONFIGURATION.md#server-environment-variables).

Local setup: [DEVELOPMENT.md](DEVELOPMENT.md).

## Charts

| Chart | Purpose |
|---|---|
| [`charts/badil`](charts/badil) | Installs the badil server; the cleanup sweep runs in the server hourly by default, the cleanup CronJob is disabled by default |
| [`charts/preview-apps`](charts/preview-apps) | One preview: AppProject, Namespace, one ArgoCD Application per service and per dependency |
| [`charts/app`](charts/app) | Generic service chart; service charts wrap it as a dependency aliased `app` |

Install: [docs/INSTALL.md](docs/INSTALL.md); ArgoCD wiring: [examples/argocd](examples/argocd/README.md). Charts are published to `oci://ghcr.io/badilhq/charts`; see [CONTRIBUTING.md](CONTRIBUTING.md#releases) for how versions are set.

## Docs

- [Install](docs/INSTALL.md): from an empty cluster to a first preview
- [Architecture](docs/ARCHITECTURE.md): how badil works and why
- [Configuration reference](docs/CONFIGURATION.md): `badil.yaml`, the preview file, the service repo contract and the server's environment variables
- [HTTP API reference](docs/API.md): every endpoint
- [Pull request events](docs/EVENTS.md): what badil does on a pull request delivery
- [Services and dependencies](docs/SERVICES.md): path filters, `needs`, service modes, dependencies and preview file writes
- [Cleanup and config sync](docs/CLEANUP.md): teardown, the cleanup sweep and config sync
- [Operations reference](docs/OPERATIONS.md): metrics, preview status, spin-up time, preview deployments on GitHub and the dashboard
- [Preview contract](docs/CONTRACT.md): the `badil.dev/v1beta1` preview file, its schema and conformance fixtures, and what a renderer must do

## License

[Apache-2.0](LICENSE)
