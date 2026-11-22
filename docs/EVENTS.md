# Pull request events

This page describes what badil does on a pull request delivery: how the PR's labels pick its preview, which previews it leaves, and the checks that run before a deploy.

Related pages: [Services and dependencies](SERVICES.md) covers what a deploy writes, and [Cleanup and config sync](CLEANUP.md) covers teardown, the cleanup sweep and config sync. Why badil works this way is in [Architecture](ARCHITECTURE.md), the keys it reads are in the [Configuration reference](CONFIGURATION.md), and the endpoints are in the [HTTP API reference](API.md).

## Label flow

CI only builds and pushes images. The preview label (`preview.label`) on a PR decides whether the PR has a preview, and in which one.

badil answers `opened`, `reopened`, `synchronize`, `labeled`, `unlabeled` and `closed` deliveries 200 at once, and reconciles the PR in the background:

1. A `labeled` or `unlabeled` delivery for another label is skipped without calling GitHub. So is an `opened`, `reopened` or `synchronize` delivery whose payload lacks a preview label. The preview labels are the plain label and the named labels with a non-empty slug (see [Target preview](#target-preview)).
2. A repo not in the catalog is skipped with a log. Neither step 1 nor step 2 applies to `closed`.
3. Otherwise badil re-reads the PR from GitHub and acts on its current state, labels and head SHA, never the payload's. See [Current PR state](#current-pr-state).
4. The PR's labels resolve its target preview (see [Target preview](#target-preview)).
5. The PR is torn down from every other preview it may still be pinned in (the [cleanup set](#cleanup-set)), before anything is deployed.
6. An open PR with a target deploys to the target slug (see [Deploy step](#deploy-step)).

   Steps 5 and 6 are one commit, pushed again on a moved remote head (see [Reads and writes through the gitops repo](ARCHITECTURE.md#reads-and-writes-through-the-gitops-repo)). Its subject is `chore(preview): deploy <owner>/<repo>#<n> to <slug>` when a service was deployed, followed by `, unpin from <slug>, …` when the PR left other previews, and `chore(preview): unpin <owner>/<repo>#<n> from <slug>, …` otherwise. Its body has one line per change, by slug: `<slug>: pin <service>@<sha7>`, `<slug>: unpin <service>` or `<slug>: delete`. The PR comments are posted once the commit is pushed, one per PR and preview, and link to it. When the PR's files can't be listed, or the target preview can't be read, the commit holds only step 5 and the reconcile fails.
7. An open PR with conflicting named labels gets no preview. Its comment is replaced last by:

   ```
   ## ⚠️ Preview: conflicting labels

   Keep one `preview:` label. This PR has `preview:a`, `preview:b`.
   ```

### Current PR state

Because badil acts on the PR as GitHub returns it, late or out-of-order deliveries, `closed` included, act on the PR's current state. A `closed` delivery running after the PR was reopened and labeled deploys it.

Fork PRs (head repo different from the base repo) are skipped: CI can't push a fork's image. A closed fork PR is still torn down.

### Deploy step

An open PR with a target deploys, at the PR's head SHA, in catalog order, to the target slug:

- every catalog service of its repo;
- or, when its services declare `paths`, the services it touches, after unpinning there its entries for the ones it no longer touches (see [Path filters](SERVICES.md#path-filters)).

It writes the services as the manual flow does (see [`POST /v1/deploy`](API.md#post-v1deploy)), in the reconcile's commit, then posts the preview's summary once on its open PRs. It deploys nothing when one of these holds:

- another PR holds one of its services there (see [One PR per service](#one-pr-per-service));
- the target does not exist and the preview cap is reached (see [Preview cap](#preview-cap)).

A failed commit fails the reconcile: nothing of it is written.

A service whose write would change nothing but its `updated-at` is not written and posts no comment. Unchanged means the same SHA, host and PR metadata, on the head the commit is made on, decided again on every push attempt. It logs `preview deploy unchanged`, unless step 5 removed the PR from another preview. When every service is unchanged, the reconcile is skipped with reason `already deployed`.

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

With [path filters](SERVICES.md#path-filters), the PR's services are the services it touches.

Any held entry refuses the whole PR, for every target, branch or named, since two PRs can also share a head branch. The first held service is reported in the PR's comment:

```
## ⚠️ Preview: `<slug>`

`<service>` is already pinned in this preview by <holder PR URL>. Once that pull request leaves this preview, push to this pull request or re-add its label. If that pull request is closed, or now carries a different `<label>:` label, it is released within the next cleanup run.
```

The reconcile logs `preview reconcile skipped` with reason `service held by another pr` and a `holder` attribute. A preview file that can't be read logs `preview reconcile failed` and deploys nothing.

The holder PR is never read: a holder left by a missed event is released by the sweeper. A refused PR is not retried when the holder leaves, except by [catch-up](#catch-up): `badil serve` reconciles it again once per head SHA per process, and `badil cleanup` starts each run with an empty seen map, so it reconciles refused PRs again on every run. A freed holder is therefore picked up at the next `badil cleanup` run, or in `badil serve` at a new head SHA or after a restart. Since the cleanup runs first, the refused PR has already left its previous preview, as its labels ask.

The check is decided on the head the reconcile's commit is made on, and again on every push attempt: a push is rejected when any other commit landed since, whoever made it. So it does not rely on reconciles being serialized in one replica. `/v1/deploy` skips the check: it stays last-writer-wins and can overwrite a holder's pin.

## Preview cap

`preview.maxPreviews` caps the number of previews: the directories under `previews/`, listed when a deploy would create a new one. A deploy into an existing preview is never refused, and without the key there is no cap.

In the label flow, the check runs after the holder check, when the target's `values.yaml` does not exist (that same read, not a second one). The reconcile then lists the previews. When there are already `maxPreviews` or more, it writes nothing and replaces the PR's comment with:

```
## ⚠️ Preview: `<slug>`

The preview limit (<max>) is reached. Once a preview is torn down, push to this pull request or re-add its label.
```

It logs `preview reconcile skipped` with reason `preview limit reached` and a `max_previews` attribute.

A refused PR is not retried when a slot frees, except by [catch-up](#catch-up): `badil serve` reconciles it again once per head SHA per process, and `badil cleanup` starts each run with an empty seen map, so it reconciles refused PRs again on every run. A freed slot is therefore picked up at the next `badil cleanup` run, or in `badil serve` at a new head SHA or after a restart. Since the cleanup runs first, the refused PR has already left its previous preview.

The cap check of `/v1/deploy` is described in [`POST /v1/deploy`](API.md#post-v1deploy).

The check is decided on the head the commit is made on, and again on every push attempt, so a concurrent deploy that creates a preview is counted.

The check reads the gitops clone, so it costs no extra GitHub call.

## Reconcile queue

Background reconciles run one at a time, in no guaranteed order. A reconcile reads the PR only once it runs.

A reconcile has a 20s budget, counted from when it starts running. It covers the PR and files reads of [path filters](SERVICES.md#path-filters), then its commit, including the wait for an operation already writing (a cleanup sweep, a manual deploy) and every push attempt, then its comments. The commit holds, in this order, its cleanup teardowns, the holder check, the preview cap check, the unpin of untouched services and its services. A reconcile that deploys fails when its commit is not pushed in time. One that only tears down records each of its teardowns as an error and keeps the `torn_down` outcome.

Shutdown waits up to 25s for queued and running jobs. Jobs still queued then are dropped: the cleanup sweeper catches missed teardowns of closed PRs, and [catch-up](#catch-up) the missed deploys of open ones.

## Catch-up

A deploy is not lost to a delivery that arrives while badil is down or restarting, or to a reconcile dropped at shutdown. Catch-up compares the open labelled pull requests of every catalog repo with the preview files, and reconciles each PR that is not pinned at its head SHA in its target preview. Deliveries stay the fast path; catch-up only repairs what they missed.

### When it runs

- When `badil serve` starts, in the background once the gitops clone is ready, without delaying readiness.
- After each in-server sweep, whether it succeeded or failed. It does not start once shutdown has begun.
- After the sweep of each `badil cleanup` run, whether it succeeded or failed.

With `CLEANUP_INTERVAL` unset and no CronJob, it runs only at start. Each run, listing and queueing, is bounded by 10 minutes, apart from the sweep's own bound. Once that bound expires, or shutdown begins, the run lists no more repos and queues no more reconciles; it still logs its `catch-up` line.

### Which PRs

The repos are the distinct `<GITHUB_URL>/<owner>/<repo>` of the services in `badil.yaml` (`https://github.com/<owner>/<repo>` by default), the URLs that match a delivery's repo (URLs on another host and other URL forms never do). Each repo's open PRs are listed with `GET /repos/{owner}/{repo}/pulls?state=open&per_page=100`, every page: about one REST call per repo and page per run. A repo whose listing fails is logged (`catch-up list failed`, with `repo` and `err`) and skipped; the other repos go on.

Each listed PR is decided in this order:

1. A PR without a preview label (the plain label, or a named label with a non-empty slug) is not reconciled.
2. A fork PR is not reconciled.
3. The PR's labels resolve its target preview (see [Target preview](#target-preview)). A PR whose named labels conflict has no target, so it can't be pinned, and goes on to step 5.
4. A PR is pinned at head when its target's `values.yaml` has an entry whose `metadata.pr-url` names the PR (its owner, repo and number, owner and repo in any letter case) and whose `commitSha` is its head SHA. It is skipped. A target that does not exist pins nothing; one that can't be read is logged (`catch-up preview read failed`) and the PR goes on to step 5.
5. A PR is skipped when it is in the seen map at its head SHA, or when a catch-up reconcile of it is still queued or running.
6. Any other PR is reconciled.

The seen map holds, per PR, the head SHA its last catch-up reconcile was started at, once that reconcile completed with an outcome other than `failed`. A reconcile that fails, or never completes (dropped at shutdown, or panicked), leaves the PR out of it, so the next run reconciles it again at the same head SHA. A PR that never gets a pin (no service changed, held by another PR, preview cap reached, conflicting labels) is therefore reconciled once per head SHA. `badil serve` keeps the seen map for the life of the process; each `badil cleanup` run starts with an empty one.

### How

Each PR gets the reconcile a `synchronize` delivery carrying its listed labels gets, so its outcome, comments and metrics are those of a delivery, and it reads the PR again. In `badil serve` it goes through the [reconcile queue](#reconcile-queue), with the same 20s budget, and never runs alongside another reconcile. `badil cleanup` reconciles the PRs one after another, each with a 20s budget. Its exit code stays the sweep's: catch-up's listing failures and failed reconciles are only logged and counted.

Each run logs `catch-up` with `repos`, `open_labelled` (PRs with a preview label, forks included), `reconciled`, `skipped_pinned`, `skipped_seen` and `list_errors`. A `badil.yaml` that can't be read logs `catch-up failed` and lists nothing. The reconciles a run starts are counted in `badil_catchup_reconciles_total` (see [Metrics](OPERATIONS.md#metric-list)).
