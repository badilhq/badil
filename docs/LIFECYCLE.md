# Preview lifecycle reference

This page describes what badil does on each event: a pull request delivery, a teardown, a cleanup sweep and a config sync. Why it works this way is in [Architecture](ARCHITECTURE.md). The keys it reads are in the [Configuration reference](CONFIGURATION.md), and the endpoints in the [HTTP API reference](API.md).

## Label flow

CI only builds and pushes images. The preview label (`preview.label`) on a PR decides whether the PR has a preview, and in which one.

badil answers `opened`, `reopened`, `synchronize`, `labeled`, `unlabeled` and `closed` deliveries 200 at once, and reconciles the PR in the background:

1. A `labeled` or `unlabeled` delivery for another label is skipped without calling GitHub. So is an `opened`, `reopened` or `synchronize` delivery whose payload lacks a preview label. The preview labels are the plain label and the named labels with a non-empty slug (see [Target preview](#target-preview)).
2. A repo not in the catalog is skipped with a log. Neither step 1 nor step 2 applies to `closed`.
3. Otherwise badil re-reads the PR from GitHub and acts on its current state, labels and head SHA, never the payload's. See [Current PR state](#current-pr-state).
4. The PR's labels resolve its target preview (see [Target preview](#target-preview)).
5. The PR is torn down from every other preview it may still be pinned in (the [cleanup set](#cleanup-set)), before anything is deployed.
6. An open PR with a target deploys to the target slug (see [Deploy step](#deploy-step)).
7. An open PR with conflicting named labels gets no preview. Its comment is replaced last by:

   ```
   ## ⚠️ Preview: conflicting labels

   Keep one `preview:` label. This PR has `preview:a`, `preview:b`.
   ```

### Current PR state

Because badil acts on the PR as GitHub returns it, late or out-of-order deliveries, `closed` included, converge on the current truth. A `closed` delivery running after the PR was reopened and labeled deploys it.

Fork PRs (head repo different from the base repo) are skipped: CI can't push a fork's image. A closed fork PR is still torn down.

### Deploy step

An open PR with a target deploys, at the PR's head SHA, in catalog order, to the target slug:

- every catalog service of its repo;
- or, when its services declare `paths`, the services it touches, after unpinning there its entries for the ones it no longer touches (see [Path filters](#path-filters)).

It goes through the manual flow's write and comment fan-out (see [`POST /v1/deploy`](API.md#post-v1deploy)). It deploys nothing when one of these holds:

- another PR holds one of its services there (see [One PR per service](#one-pr-per-service));
- the target does not exist and the preview cap is reached (see [Preview cap](#preview-cap)).

A failed service is logged and the next one still deploys.

A service whose write would change nothing but its `updated-at` writes no commit and posts no comment. Unchanged means the same SHA, host and PR metadata, on the first read and after a 409 re-fetch. It logs `preview deploy unchanged`, unless step 5 removed the PR from another preview. When every service is unchanged, the reconcile is skipped with reason `already deployed`.

A PR comment that failed to post, or that a failed teardown left stale, is therefore not repaired by a later label event at the same head SHA. The next push to the PR or a manual `/v1/deploy` repairs it.

The deploy fan-out is the last write to the PR's comment, so a PR that moved shows its new preview.

## Target preview

A label is **named** when it starts with `<label>:`, case-insensitively, for example `preview:pricing` or `Preview:Pricing`. Its name is the rest, trimmed of spaces, and its slug is the slugified name. A named label whose slug is empty (`preview:`, `preview:--`) is ignored and logged (`preview label ignored`).

The **plain** label is `<label>` itself. `preview-x` and `previewx:y` are neither.

The target follows from the labels:

- Exactly one distinct named slug: the target is that slug. A named label wins over the plain label. Two named labels with the same slug count as one.
- Two or more distinct named slugs: a conflict, with no target.
- No valid named label, and the plain label: the target is the slugified head branch.
- Otherwise: no target.

Several PRs, from different repos and branches, labeled `preview:pricing` share the preview `pricing`. Each pins its own services, and the rest run at their branch (default `main`).

A named preview and a branch preview with the same slug are the same preview: `preview:main` joins the preview of a branch called `main`.

badil never creates labels. Labels are per repo, so `preview:<name>` must be created in each repo.

## Cleanup set

The cleanup set of a reconcile holds these slugs:

- the slugified head branch;
- every named slug;
- on `unlabeled`, the removed label's slug (the head branch's for the plain label).

It is deduplicated, without empty slugs, and without the target when the PR is deployed there. Each slug is torn down with one reason, the first that applies:

1. `PR closed`, when the PR is closed;
2. ``label `<removed label>` removed``, on `unlabeled`;
3. ``conflicting `<label>:` labels``, on a conflict;
4. ``moved to preview `<target>` ``, when the PR is deployed elsewhere;
5. ``label `<label>` removed`` otherwise.

A slug where the PR has no pin costs one read and is otherwise a no-op, with no comment.

A renamed or deleted label triggers nothing until the PR's next event or the next sweep, which unpins without redeploying. In that missed-event case the PR's comment can stay stale until its next event.

## One PR per service

A preview holds one PR per service. Before deploying, a reconcile reads the target's `values.yaml`; a missing file means no holder.

An entry for one of the PR's services is held when all of these hold:

- it is pinned (`commitSha` set);
- it has metadata;
- its `pr-url` names another PR.

With [path filters](#path-filters), the PR's services are the services it touches.

Any held entry refuses the whole PR, for every target, branch or named, since two PRs can also share a head branch. The first held service is reported in the PR's comment:

```
## ⚠️ Preview: `<slug>`

`<service>` is already pinned in this preview by <holder PR URL>. Once that pull request leaves this preview, push to this pull request or re-add its label. If that pull request is closed, or now carries a different `<label>:` label, it is released within the next cleanup run.
```

The reconcile logs `preview reconcile skipped` with reason `service held by another pr` and a `holder` attribute. A read error other than 404 logs `preview reconcile failed` and deploys nothing.

The holder PR is never read: a holder left by a missed event is released by the sweeper. A refused PR is not retried when the holder leaves. Since the cleanup runs first, it has already left its previous preview, as its labels ask.

The check assumes `replicaCount: 1` (`charts/badil/values.yaml`). Reconciles are serialized within the replica, so no other reconcile writes between the read and the write. `/v1/deploy` runs outside that queue and skips the check: it stays last-writer-wins and can overwrite a holder's pin.

## Preview cap

`preview.maxPreviews` caps the number of previews: the directories under `previews/`, listed when a deploy would create a new one. A deploy into an existing preview is never refused, and without the key there is no cap.

In the label flow, the check runs after the holder check, when the target's `values.yaml` does not exist (that same read, not a second one). The reconcile then lists the previews. When there are already `maxPreviews` or more, it writes nothing and replaces the PR's comment with:

```
## ⚠️ Preview: `<slug>`

The preview limit (<max>) is reached. Once a preview is torn down, push to this pull request or re-add its label.
```

It logs `preview reconcile skipped` with reason `preview limit reached` and a `max_previews` attribute. A list error logs `preview reconcile failed` and deploys nothing.

A refused PR is not retried when a slot frees. Since the cleanup runs first, it has already left its previous preview.

The cap check of `/v1/deploy` is described in [`POST /v1/deploy`](API.md#post-v1deploy).

The cap is soft. The check and the write are not atomic, and `/v1/deploy` runs outside the reconcile queue, so a concurrent manual deploy and reconcile can both pass the check and each exceed the cap by one.

The check costs one extra GitHub call, only when the cap is set and the target does not exist.

## Reconcile queue

Background reconciles run one at a time, in no guaranteed order. A reconcile reads the PR only once it runs.

A reconcile has a 20s budget, counted from when it starts running. Its steps share it, in this order:

1. its cleanup teardowns;
2. the files listing of [path filters](#path-filters);
3. the holder check;
4. the preview cap check;
5. the unpin of untouched services;
6. its services.

A service not reached in time is logged as failed.

Shutdown waits up to 25s for queued and running jobs. Jobs still queued then are dropped, and the cleanup sweeper catches missed teardowns of closed PRs.

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

Only the touched services are deployed, in catalog order, at the PR's head SHA. Only they are checked for a holder (see [One PR per service](#one-pr-per-service)).

### Renamed files

A renamed file touches a service when its new path or its previous path matches: moving `api/x.go` to `tools/x.go` touches `api`.

### More than 3,000 files

GitHub lists at most 3,000 files of a PR. When the PR's `changed_files` exceeds 3,000, every service of its repo is touched and no file is listed.

### Failed listing

A listing that fails, on any page, logs `preview reconcile failed` and deploys nothing, whatever the action, `closed` included. It never falls back to every service.

### Later pushes

Before deploying, badil unpins the PR's entries in the target preview for services of its repo that the PR no longer touches. It does so in one commit, `chore(preview): remove <names> from <slug>`, projected on `badil.yaml` like a teardown that keeps the preview. The comments of the PRs still pinned there are refreshed.

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

The image tag parameter is passed with `forceString: true`, so a digits-only short SHA stays a string ([CONTRACT](CONTRACT.md#renderer-obligations), obligation 4).

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

`on-preview-healthy` of `notifications-values.yaml` still fires for them. badil ignores the event, as for a branch-tracking service: it has no `updated-at`.

### Name collision

A dependency name is never a service name of `badil.yaml`, which rejects it. A service removed from `badil.yaml` can still have a kept pinned entry with the name of a new dependency (see [Services added to or removed from the catalog](#services-added-to-or-removed-from-the-catalog)).

The pinned entry then wins until its PR leaves the preview. badil leaves the dependency out of that preview's file and logs the warning `dependency left out: a pinned service has its name` with `slug` and `name`. The first write after the entry is unpinned and dropped adds the dependency.

## Preview file writes

badil writes `previews/<slug>/values.yaml` on every deploy, teardown and sweep. Its fields are in [CONTRACT](CONTRACT.md#v1beta1-file).

Each deploy write, and the cleanup sweep's sync ([step 4](#4-sync-with-badilyaml)), project the file on the catalog as it is then. The file holds, in order:

1. `apiVersion`, `gitopsRepoURL` and `namespace`;
2. every service of `services`, in catalog order, with its `repoURL`, `branch`, `chart`, `imageTag` and `host` (a missing one added as a branch-tracking entry);
3. the pinned entries of services no longer in `services` that have a `repoURL`, unchanged except `host`, recomputed from `preview.expose`;
4. every dependency of `badil.yaml`, in its order, with its `host`.

When an entry of `badil.yaml` declares `needs`, only the pinned entries and what they need are kept (see [Needs](#needs)). For removed services, see [Services added to or removed from the catalog](#services-added-to-or-removed-from-the-catalog).

A `badil.yaml` change is written on the next deploy, even for an unchanged SHA. It is also written in every preview by the push that changed it, `POST /v1/sync` or the next sweep (see [Config sync](#config-sync)), one commit per preview. The PR comment is refreshed only when the services, the dependencies or hosts change.

Teardowns unpin, then project the file on `badil.yaml`, in the same commit, so an unpinned entry gets its branch-tracking `imageTag` at once. When `badil.yaml` cannot be read or parsed, such a teardown writes nothing and fails. The file keeps the PR's pin, and the next event of that PR or the next sweep retries it. A teardown that deletes the preview does not read `badil.yaml`.

## Teardown

A reconcile tears the PR down from each slug of its [cleanup set](#cleanup-set). This happens when the PR is closed (merged or not), lost its label (for example after `unlabeled`), has conflicting labels, or moved to another preview.

### Unpin, update or delete

For each slug (the slugified head branch or named label), badil reads `previews/<slug>/values.yaml`. It unpins every service whose `pr-url` points to the PR: the entry loses its `commitSha` and `metadata` and goes back to tracking its branch.

If other pins remain, the file is updated and the remaining PRs' comments are refreshed. The update is projected on `badil.yaml` in the same commit: an unpinned entry gets its branch-tracking `imageTag`, and one of a service no longer in `badil.yaml` is dropped. When `badil.yaml` cannot be read or parsed, nothing is written, and the teardown is retried by the PR's next event or the next sweep.

Otherwise the file is deleted, without reading `badil.yaml`.

Both writes use SHA optimistic locking + 409 retry, deciding update vs delete again on every retry.

### Comment

The PR's comment is edited (or created) to say what was removed and why: `PR closed`, ``label `<label>` removed``, ``label `<label>:<name>` removed``, ``conflicting `<label>:` labels`` or ``moved to preview `<slug>` ``.

### Fallback on closed PRs

A `closed` delivery whose reconcile can't read the catalog or the PR falls back to tearing down, as closed, the payload's head branch and the payload's named labels. This is logged as a warning, so a close still tears down when GitHub reads fail.

A fallback teardown that would keep the preview also needs `badil.yaml`. When that read fails too, it writes nothing, and the PR's next event or the next sweep tears it down.

### Deleting a preview

Deleting the file drops `previews/<slug>/` from the git directory generator, and the ApplicationSet deletes the preview's Application and its resources.

This requires the ApplicationSet controller policy `sync` (the default) or `create-delete`. With `create-only` or `create-update`, a deleted preview keeps running.

### What the sweeper catches

The cleanup sweeper catches these cases:

- closes missed while badil was down;
- pins written by a deploy after the PR closed;
- a labeled PR whose branch was renamed after the deploy: it is unpinned from its old slug, without a comment. Push to redeploy it at the new slug.

A labeled PR with no push for longer than `CLEANUP_TTL` is expired by the sweeper, and is not recreated while the label stays. Push, or remove and re-add the label, to recreate it.

A missed `unlabeled` of a named label, or a label that moved, is unpinned by the sweeper. A missed `unlabeled` of the plain label at the branch slug still waits for close or TTL.

## Cleanup sweep

### Schedule

The sweep runs in one of two places. Use one or the other: both run the same idempotent sweep, so running both only wastes work.

- **In the server**, every `CLEANUP_INTERVAL` when it is set, starting one interval after startup. Runs never overlap, and shutdown cancels a run in progress. Only the in-server sweep exports the sweep metrics.
- **As a CronJob**: otherwise `badil cleanup` runs it once per CronJob schedule (hourly by default).

The chart runs the sweep in the server (`cleanup.interval`, `1h` by default) unless `cleanup.enabled=true` switches it to the CronJob.

### Rules

The sweep lists `previews/*` and applies to each preview the first matching rule of 1 to 3. It then syncs every preview it keeps (step 4).

#### 1. No pinned service

The file is deleted (`chore(preview): delete <slug> preview`). No comment.

#### 2. Expired

This rule applies only when `CLEANUP_TTL` is set. It matches when the newest `updated-at` of the pinned services is older than `CLEANUP_TTL`.

The file is deleted (`chore(preview): expire <slug> preview`), and its open PRs get a comment saying the preview expired. A pinned service without a parseable `updated-at` never counts, so such a preview never expires.

#### 3. Closed or moved PRs

Every distinct PR pinning a service is looked up on GitHub. A PR whose state can't be read (404, 403, any error) keeps its pins and is logged as a warning.

A closed (or merged) PR goes through the same teardown as a `pull_request` `closed` delivery: unpin, update or delete, comments.

An open PR's labels are resolved to its target preview (see [Target preview](#target-preview)). It is unpinned from this preview, in this order:

- when its labels conflict (``conflicting `<label>:` labels``);
- when they target another preview (``moved to preview `<target>` ``).

Both leave its comment untouched, since it shows where the PR lives now. When the PR has no target and this preview is not its branch slug, it goes through the teardown, and its comment says ``label `<label>` removed``. Any other open PR is left alone, including an unlabeled PR pinned at its branch slug.

This rule catches missed `unlabeled` deliveries, conflicts, and renamed or deleted labels.

The catalog is read once per sweep, for the label and step 4. When it can't be read, `cleanup catalog unavailable` is logged as a warning. It is not counted as a failure, only closed PRs are torn down, and step 4 is skipped.

#### Manual deploy pins

Rule 3 changes manual deploys. A `/v1/deploy` pin survives the sweep only in two places:

- at the PR's target preview: its single `<label>:<name>` slug, or its branch slug when it has only the plain label;
- at its branch slug, when the PR has no target.

Anywhere else it is unpinned (``moved to preview `<target>` `` or ``label `<label>` removed``). A PR carrying `<label>:a` loses a pin at its branch slug. A PR with conflicting named labels is unpinned from every preview (``conflicting `<label>:` labels``).

A webhook reconcile of the PR also unpins it from its branch slug and its named-label slugs other than its target (`cleanupSlugs`). Such pins used to be left alone.

#### 4. Sync with `badil.yaml`

Every preview that rules 1 and 2 did not match is re-read after rule 3, whose teardowns may have changed or deleted it. It is rewritten as its projection on the catalog (see [Preview file writes](#preview-file-writes)) when it differs, in one commit `chore(preview): sync <slug> with badil.yaml`, with SHA optimistic locking + 409 retry. A file that already matches is not written.

A preview whose rule 1–2 delete is cancelled by the re-check skips this step: the deploy that cancelled it already wrote the projection.

A rewrite is logged (`preview synced`) and counted in `badil_previews_synced_total`. When it changed the list of services, the list of dependencies or a host, the open PRs' comments are refreshed.

### Catalog changes

A `badil.yaml` change therefore reaches every preview within one sweep interval, without a push: a service added, removed or renamed, a `repo`, `branch`, `preview.domain` or `expose` change. With the App subscribed to `push`, it reaches them at once (see [Config sync](#config-sync)).

A mistaken edit propagates the same way, except that pinned entries of a removed service stay until their PR leaves. A git revert of `badil.yaml` undoes it at the next sweep.

After a catalog edit, one sweep makes one commit per preview whose file differs. It updates the comment of each open PR of the previews whose services, dependencies or hosts changed. All of this stays within the GitHub App's rate limit, and `badil_previews_synced_total` shows the volume.

A failed sync write counts as a failed preview and is retried by the next sweep. A failed comment refresh is logged (`preview sync comment failed`) and not retried. That comment stays stale until the PR's next deploy or teardown, as for every comment fan-out.

### Failures and races

Rules 1 and 2 re-check their condition on the freshly read file, and again after every 409, so a deploy landing during the sweep cancels the delete.

A failing preview read, rule 1–2 delete or step 4 sync is logged and the sweep goes on. The command then exits 1 and the Job fails without retrying (`backoffLimit: 0`). A failing closed-PR teardown is only logged. The next scheduled run retries both.

Comment fan-outs (deploy, teardown, expiry) skip closed PRs, so a closed PR keeps its teardown comment.

### Known limit

The GitHub contents API lists at most 1,000 entries per directory, so the sweep sees at most 1,000 previews.

## Config sync

Two triggers run the sweep's step 4 outside the sweep, without its rules 1–3: the rewrite, the `preview synced` log, `badil_previews_synced_total` and the comment refresh.

### Push

A verified `push` delivery is acted on only when all of these hold:

- `repository.full_name` is `GITOPS_REPO` (case-insensitive);
- `ref` is `refs/heads/<repository.default_branch>`;
- `deleted` is false.

The `webhook push` log of any other push carries its `reason`. Every push is answered 200.

In the background, badil reads `badil.yaml`'s blob SHA on the default branch. When it equals the SHA of the last push sync (kept in memory, empty at startup), it stops (`config sync skipped`, reason `badil.yaml unchanged`). badil's own deploy, teardown and sync commits are pushes too, and stop there.

Otherwise every preview is synced with that content, and its SHA is kept. The SHA is kept also when a preview failed (the sweep retries it) or the content can't be parsed (the same content would fail again). A `badil.yaml` that can't be read, or previews that can't be listed, keep no SHA, so the next push retries.

### `POST /v1/sync`

[`POST /v1/sync`](API.md#post-v1sync) syncs every preview, or the one its body names, and answers when done. It ignores and keeps the push SHA.

### Concurrency

One config sync runs at a time in the server. Pushes and `/v1/sync` calls each wait their turn, in no guaranteed order. A push whose `badil.yaml` SHA a run before it synced stops at the SHA check, and each `/v1/sync` call gets its own answer.

A run is capped at 10 minutes (the sweep's bound), counted from when it starts running. It does not wait for reconciles nor block them, and the sweep's own step 4 runs beside it. SHA optimistic locking keeps concurrent writes safe.

Shutdown waits for a push sync like for reconciles (up to 25s).

Each run logs `config sync finished` (with `synced`, `unchanged` and `failed` counts) or `config sync failed`. A failed preview logs `config sync preview failed`.

A large `badil.yaml` change makes the same burst of commits and comments as a sweep.
