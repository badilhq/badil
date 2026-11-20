# Services and dependencies

This page describes which services and dependencies a preview runs and how badil writes them into the preview file: path filters, `needs`, service modes, dependencies and preview file writes.

Related pages: [Pull request events](EVENTS.md) covers how a PR reaches a deploy, and [Cleanup and config sync](CLEANUP.md) covers teardown, the cleanup sweep and config sync.

## Path filters

`paths` lets a monorepo PR pin only the services it changes. Without it, a labelled PR pins every catalog service of its repo. The rules below use this example:

```yaml
services:
  api:    {repo: https://github.com/acme/platform.git, paths: ["api/**", "libs/common/**", "go.mod"]}
  web:    {repo: https://github.com/acme/platform.git, paths: ["web/**"]}
  worker: {repo: https://github.com/acme/platform.git, paths: ["worker/**", "libs/common/**"]}
```

The key's syntax and errors are in [`paths`](CONFIGURATION.md#servicesnamepaths).

### Touched services

Path filters apply on every reconcile that deploys: an open PR with a target, whatever the action, `unlabeled` included. When a service of the PR's repo declares `paths`, badil lists the files the PR changes against its base (`GET /repos/{owner}/{repo}/pulls/{number}/files`, every page of 100).

A service without `paths` is always touched. A service with `paths` is touched when a changed file matches one of its patterns. With the example:

- `web/src/app.tsx` alone touches `web`;
- adding `libs/common/auth.go` also touches `api` and `worker`;
- `docs/README.md` alone touches no service.

Only the touched services are deployed, in catalog order, at the PR's head SHA. Only they are checked for a holder (see [One PR per service](EVENTS.md#one-pr-per-service)).

### Renamed files

A renamed file touches a service when its new path or its previous path matches: moving `api/x.go` to `tools/x.go` touches `api`.

### More than 3,000 files

GitHub lists at most 3,000 files of a PR. When the PR's `changed_files` exceeds 3,000, every service of its repo is touched and no file is listed.

### Failed listing

A listing that fails, on any page, logs `preview reconcile failed` and deploys nothing, whatever the action, `closed` included. It never falls back to every service.

### Later pushes

Before deploying, badil unpins the PR's entries in the target preview for services of its repo that the PR no longer touches. It does so in the reconcile's commit, projected on `badil.yaml` like a teardown that keeps the preview. The deploy's summary refreshes the comments of the PRs still pinned there.

Pins the PR made through `/v1/deploy` for services of other repos stay.

This write never deletes the preview, even when it unpins the PR's last pin: the deploy that follows pins at least one touched service. It counts in `badil_teardowns_total` with trigger `webhook`. When it fails, the reconcile logs `preview reconcile failed` and deploys nothing.

After it, the deploy always writes and comments, so the PR's comment shows the current pins. A PR refused by a holder or the preview cap keeps its pins until an event where it deploys.

An unpinned service's GitHub deployment goes `inactive` (see [Preview deployments on GitHub](OPERATIONS.md#preview-deployments-on-github)).

### No touched service

A PR that touches no service is torn down from its target preview: all its pins there, deleting the preview when no pin is left. There is no teardown comment. Its comment is replaced by the following, even when there was no preview:

```
## ⏭️ Preview: `<slug>`

This PR changes no service of the preview (`paths` in badil.yaml), so nothing is deployed.
```

The reconcile logs `preview reconcile skipped` with reason `no service changed`. Neither the holder nor the preview cap check runs.

A failed teardown is logged and counted as for any teardown, and the comment and outcome stay the same.

### What badil does not infer

badil does not infer `paths`. A shared file missing from a service's `paths` (a library, a lockfile) does not pin it. The PR comment shows which services are pinned.

### Sweep and manual deploys

Only PR events apply path filters. The cleanup sweep does not re-check them, and `/v1/deploy` names its service.

## Needs

`needs` trims a preview to what its PRs use. A service or dependency of `badil.yaml` may list the services and dependencies it calls. The rules below use this example:

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

The key's syntax and errors are in [`needs`](CONFIGURATION.md#servicesnameneeds-and-dependenciesnameneeds).

### Activation

When no entry of `badil.yaml` declares `needs`, every preview runs every service and dependency.

As soon as one entry declares it, every preview runs only its included set:

- its pinned entries: catalog services, and kept pinned entries of services removed from `badil.yaml`;
- every service and dependency in the transitive `needs` of its pinned catalog services.

A kept pinned entry of a removed service has no `needs`. Cycles are followed once.

### Projection

Every write projects the file on `badil.yaml` (deploy, teardown that keeps the preview, sweep sync), so the set is computed on each. The file then holds, in order:

1. the included services, in catalog order;
2. the kept pinned entries of removed services;
3. the included dependencies, in `badil.yaml` order.

Only included parts get hosts. A part left out is absent from the preview file: `preview-apps` renders no Application for it and Argo CD prunes it.

A shared preview grows when a PR joins and shrinks when one leaves. Closing the `payments` PR of a preview also pinned by `front` drops `payments` and `kafka` in the teardown commit.

### User responsibility

badil does not infer `needs`. A missing entry gives a preview without a part the code calls.

### PR comment line

After the service and dependency lists, separated by a blank line, a line names the catalog services then the dependencies left out, in `badil.yaml` order. It appears only when there is at least one. With only `front` pinned in the example:

``Not in this preview: `payments`, `search`, `kafka`, `elasticsearch` ``

It comes before the pinned-image note.

### Staleness

The line is derived from `badil.yaml` minus the included set. It is refreshed only by a deploy, or by a write that changes the ordered service or dependency names or a host.

A `badil.yaml` change that alters only the left-out set (a new service or dependency that no pinned part needs) leaves the preview file unchanged. The sweep writes nothing, and existing comments keep the old line until the PR's next deploy.

## Service modes

A preview runs each service in one of two modes, set by `commitSha` in `previews/<slug>/values.yaml`. The concept is in [Pinned and branch-tracking services](ARCHITECTURE.md#pinned-and-branch-tracking-services).

| Mode | `commitSha` | Application revision | Image tag parameter |
|---|---|---|---|
| Pinned | set | `targetRevision: <sha>` | the entry's `imageTag`, the format expanded from `<sha>` (the full SHA by default) |
| Branch-tracking | absent | `targetRevision: <branch>`, the entry's `branch` | the entry's `imageTag`, which holds `${ARGOCD_APP_REVISION}`, `${ARGOCD_APP_REVISION_SHORT}` or `${ARGOCD_APP_REVISION_SHORT_8}` |

The image tag parameter is the entry's `chart.imageTagParam`.

A pinned service changes only when badil writes a new SHA. A branch-tracking service follows new commits on its branch: Argo CD resolves the variable to the commit it syncs.

The image tag parameter is passed with `forceString: true`, so a digits-only short SHA stays a string ([Preview contract](CONTRACT.md#renderer-obligations), obligation 4).

In both modes the Application's `repoURL` is the entry's `repoURL`. The AppProject's `sourceRepos` are the distinct `repoURL`s of the file's entries.

References: [Helm build environment in Argo CD](https://argo-cd.readthedocs.io/en/stable/user-guide/helm/), [Argo CD tracking strategies](https://argo-cd.readthedocs.io/en/latest/user-guide/tracking_strategies/).

### Services added to or removed from the catalog

Every preview lists every service of `badil.yaml`. When `badil.yaml` declares `needs`, it lists only the pinned ones and what they need (see [Needs](#needs)). A service added to `badil.yaml` appears in every preview as a branch-tracking entry at the next write: deploy, sweep, or a teardown that keeps the preview.

A service removed from `badil.yaml` leaves a preview according to its mode:

- **Branch-tracking** entries are dropped by the next write, and Argo CD prunes their Application.
- **Pinned** entries stay, with their last `name`, `repoURL`, `branch`, `commitSha`, `imageTag`, `chart` and `metadata`. They keep running at their SHA until their PR leaves the preview. Its teardown unpins them and drops them in the same commit, or deletes the file when no pin remains.

Keeping pinned entries protects the previews of open PRs from an accidental removal. A kept entry keeps its `host` while its name is still in `preview.expose`.

## Dependencies

`dependencies` of `badil.yaml` are upstream charts deployed next to the services. The key's syntax, the AppProject limit and private registries are in [`dependencies`](CONFIGURATION.md#dependencies).

### Writes

badil writes the dependencies into every preview file, in `badil.yaml` order, on every deploy, teardown that keeps the preview, and sweep sync. With `needs`, only the previews whose pinned services need a dependency include it (see [Needs](#needs)).

A dependency added to or removed from `badil.yaml` reaches every preview that includes it by its next write, and the PR comment is refreshed. With `needs`, a dependency that no pinned part needs leaves the preview file unchanged and refreshes no comment (see [Staleness](#staleness)).

### Rendering

`preview-apps` ([`dependencies.yaml`](../charts/preview-apps/templates/dependencies.yaml)) renders one Application `<slug>-<name>` per dependency, in the preview namespace, from its chart source:

- **OCI chart** (`chart.repoURL` starts with `oci://`): `repoURL` the chart reference, `targetRevision` the version, `path: "."`.
- **Helm repository**: `repoURL` the repository, `chart` the chart name, `targetRevision` the version.

`values` is passed as `helm.valuesObject`. With `valueFiles`, the Application has two sources and no `spec.source`:

- the chart, with `helm.valueFiles` set to `$values/<path>`;
- the gitops repo (`gitopsRepoURL`) at `HEAD` with `ref: values`.

Value precedence in Argo CD, highest first: parameters, `valuesObject`, `valueFiles`, the chart's `values.yaml`.

An exposed dependency gets the host `<name>-<slug>.<domain>`, passed as the Helm parameter `hostParam`, and a link in the PR comment.

Dependency Applications use sync wave `-1`, so they are created before the service Applications (wave `0`). This orders creation only: a service still has to tolerate a dependency that is not ready yet.

Their labels are those of a service Application, with `app.kubernetes.io/name: <name>`. Their annotations are `argocd.argoproj.io/sync-wave: "-1"`, `preview.badil.dev/vcs.ref.revision: ""` and `preview.badil.dev/host` (the host, `""` when not exposed), like a branch-tracking service, and no PR metadata.

The per-preview AppProject lists every dependency `repoURL` in `sourceRepos`, and `gitopsRepoURL` when a dependency has `valueFiles`.

### PR comment line

The PR comment lists each dependency after the services: ``- **<name>** · 📦 `<chart>` `<version>` ``. The chart is `chart.name` or the last path segment of the OCI reference. The link follows when the dependency is exposed.

### Notifications

The triggers of [`github-deployments-values.yaml`](../examples/argocd/github-deployments-values.yaml) skip dependency Applications, whose revision annotation is empty (see [Preview deployments on GitHub](OPERATIONS.md#preview-deployments-on-github)).

The `on-status-*` triggers of `notifications-values.yaml` fire for dependency Applications too, and badil records their status like a service's. No spin-up time is observed for them, since a dependency is never pinned.

### Name collision

A dependency name is never a service name of `badil.yaml`, which rejects it. A service removed from `badil.yaml` can still have a kept pinned entry with the name of a new dependency (see [Services added to or removed from the catalog](#services-added-to-or-removed-from-the-catalog)).

The pinned entry then wins until its PR leaves the preview. badil leaves the dependency out of that preview's file and logs the warning `dependency left out: a pinned service has its name` with `slug` and `name`. The first write after the entry is unpinned and dropped adds the dependency.

## Preview file writes

badil writes `previews/<slug>/values.yaml` on every deploy, teardown and sweep. Its fields are in [Preview contract](CONTRACT.md#v1beta1-file).

Each deploy write, and the cleanup sweep's sync ([step 4](CLEANUP.md#4-sync-with-badilyaml)), project the file on the catalog as it is then. The file holds, in order:

1. `apiVersion`, `gitopsRepoURL` and `namespace`;
2. every service of `services`, in catalog order, with its `repoURL`, `branch`, `chart`, `imageTag` and `host` (a missing one added as a branch-tracking entry);
3. the pinned entries of services no longer in `services` that have a `repoURL`, unchanged except `host`, recomputed from `preview.expose`;
4. every dependency of `badil.yaml`, in its order, with its `host`.

When an entry of `badil.yaml` declares `needs`, only the pinned entries and what they need are kept (see [Needs](#needs)). For removed services, see [Services added to or removed from the catalog](#services-added-to-or-removed-from-the-catalog).

A `badil.yaml` change is written on the next deploy, even for an unchanged SHA. It is also written in every preview by the push that changed it, `POST /v1/sync` or the next sweep (see [Config sync](CLEANUP.md#config-sync)), in one commit covering every preview. The PR comment is refreshed only when the services, the dependencies or hosts change.

Teardowns unpin, then project the file on `badil.yaml`, in the same commit, so an unpinned entry gets its branch-tracking `imageTag` at once. When `badil.yaml` cannot be read or parsed, such a teardown writes nothing and fails. The file keeps the PR's pin, and the next event of that PR or the next sweep retries it. A teardown that deletes the preview does not read `badil.yaml`.
