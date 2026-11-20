# Cleanup and config sync

This page describes how badil removes a PR from a preview and keeps previews in line with `badil.yaml`: teardown, the cleanup sweep and config sync.

Related pages: [Pull request events](EVENTS.md) covers what a PR delivery triggers, and [Services and dependencies](SERVICES.md) covers what each write puts in the preview file.

## Teardown

A reconcile tears the PR down from each slug of its [cleanup set](EVENTS.md#cleanup-set). This happens when the PR is closed (merged or not), lost its label (for example after `unlabeled`), has conflicting labels, or moved to another preview.

### Unpin, update or delete

For each slug (the slugified head branch or named label), badil reads `previews/<slug>/values.yaml`. It unpins every service whose `pr-url` points to the PR: the entry loses its `commitSha` and `metadata` and goes back to tracking its branch.

If other pins remain, the file is updated and the remaining PRs' comments are refreshed. The update is projected on `badil.yaml` in the same commit: an unpinned entry gets its branch-tracking `imageTag`, and one of a service no longer in `badil.yaml` is dropped. When `badil.yaml` cannot be read or parsed, nothing is written, and the teardown is retried by the PR's next event or the next sweep.

Otherwise the file is deleted, without reading `badil.yaml`.

Both are part of the reconcile's one commit (see [Label flow](EVENTS.md#label-flow)). When its push is rejected because the remote head moved, update vs delete is decided again on the new head.

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

The file is deleted. No comment.

#### 2. Expired

This rule applies only when `CLEANUP_TTL` is set. It matches when the newest `updated-at` of the pinned services is older than `CLEANUP_TTL`.

The file is deleted, and its open PRs get a comment saying the preview expired. A pinned service without a parseable `updated-at` never counts, so such a preview never expires.

#### 3. Closed or moved PRs

Every distinct PR pinning a service is looked up on GitHub. A PR whose state can't be read (404, 403, any error) keeps its pins and is logged as a warning.

A closed (or merged) PR goes through the same teardown as a `pull_request` `closed` delivery: unpin, update or delete, comments.

An open PR's labels are resolved to its target preview (see [Target preview](EVENTS.md#target-preview)). It is unpinned from this preview, in this order:

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

Every preview that rules 1 and 2 did not match is re-read after rule 3, whose teardowns may have changed or deleted it. It is rewritten as its projection on the catalog (see [Preview file writes](SERVICES.md#preview-file-writes)) when it differs, in the sweep's commit. A file that already matches is not written.

The whole sweep is decided again on every push attempt. A preview that a concurrent deploy pinned again no longer matches rule 1 or 2 on the retried tree, so it is synced instead of deleted.

A rewrite is logged (`preview synced`) and counted in `badil_previews_synced_total`. When it changed the list of services, the list of dependencies or a host, the open PRs' comments are refreshed.

### Catalog changes

A `badil.yaml` change therefore reaches every preview within one sweep interval, without a push: a service added, removed or renamed, a `repo`, `branch`, `preview.domain` or `expose` change. With the App subscribed to `push`, it reaches them at once (see [Config sync](#config-sync)).

A mistaken edit propagates the same way, except that pinned entries of a removed service stay until their PR leaves. A git revert of `badil.yaml` undoes it at the next sweep.

After a catalog edit, one sweep makes one commit for every preview whose file differs. It updates the comment of each open PR of the previews whose services, dependencies or hosts changed. All of this stays within the GitHub App's rate limit, and `badil_previews_synced_total` shows the volume.

A failed sweep commit is retried by the next sweep. A failed comment refresh is logged (`preview sync comment failed`) and not retried. That comment stays stale until the PR's next deploy or teardown, as for every comment fan-out.

### Failures and races

A sweep is one commit, `chore(preview): sweep: delete <d>, unpin <u>, sync <s>`, with the count of previews deleted, unpinned and kept, and synced, and one body line per change. The rules and step 4 are decided on the head the commit is made on. When the push is rejected because the remote head moved, they are decided again on the new head, so a deploy landing during the sweep cancels a delete.

The PR states rule 3 needs are read once, before the commit, one read per PR. A PR that a retried attempt finds pinned without its state read keeps its pins until the next sweep. The comments are posted once the commit is pushed.

A preview file that can't be read is logged and the sweep goes on. The command then exits 1 and the Job fails without retrying (`backoffLimit: 0`). A failing closed-PR teardown is only logged. A failed commit fails the whole sweep. The next scheduled run retries all of them.

Comment fan-outs (deploy, teardown, expiry) skip closed PRs, so a closed PR keeps its teardown comment.

## Config sync

Two triggers run the sweep's step 4 outside the sweep, without its rules 1–3: the rewrite, the `preview synced` log, `badil_previews_synced_total` and the comment refresh.

### Push

A verified `push` delivery is acted on only when all of these hold:

- `repository.full_name` is `GITOPS_REPO` (case-insensitive);
- `ref` is `refs/heads/<repository.default_branch>`;
- `deleted` is false.

The `webhook push` log of any other push carries its `reason`. Every push is answered 200.

badil makes the next read of its gitops clone check the remote, so it reads the pushed commit. In the background, it reads `badil.yaml`'s blob SHA on the default branch. When it equals the SHA of the last push sync (kept in memory, empty at startup), it stops (`config sync skipped`, reason `badil.yaml unchanged`). badil's own deploy, teardown and sync commits are pushes too, and stop there.

Otherwise every preview is synced with that content, in one commit, and its SHA is kept. The SHA is kept also when a preview failed (the sweep retries it) or the content can't be parsed (the same content would fail again). A `badil.yaml` that can't be read, or a commit that can't be pushed, keep no SHA, so the next push retries.

### `POST /v1/sync`

[`POST /v1/sync`](API.md#post-v1sync) syncs every preview, or the one its body names, and answers when done. It ignores and keeps the push SHA.

### Concurrency

One config sync runs at a time in the server. Pushes and `/v1/sync` calls each wait their turn, in no guaranteed order. A push whose `badil.yaml` SHA a run before it synced stops at the SHA check, and each `/v1/sync` call gets its own answer.

A run is one commit: `chore(preview): sync <slug> with badil.yaml` for one preview, `chore(preview): sync <n> previews with badil.yaml` for several. It is capped at 10 minutes (the sweep's bound), counted from when it starts running. Its commit is serialized with the other operations of the process, and a push rejected because the remote head moved is decided again on the new head, so concurrent writes, from the server or the cleanup CronJob, are safe.

Shutdown waits for a push sync like for reconciles (up to 25s).

Each run logs `config sync finished` (with `synced`, `unchanged` and `failed` counts) or `config sync failed`. A failed preview logs `config sync preview failed`.

A large `badil.yaml` change makes the same burst of comments as a sweep, in one commit.
