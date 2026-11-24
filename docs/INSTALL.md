# Install

From an empty cluster to a first preview. Contract details live in the [Configuration reference](CONFIGURATION.md); this guide links to them.

## 1. Prerequisites

- A Kubernetes cluster with Argo CD 3.1 or later (OCI chart source, Beta since v3.1.0), in any namespace. The `preview-apps` chart creates its AppProject and Applications in the namespace set by its `argocdNamespace` value (default `argocd`). When Argo CD runs elsewhere, set that namespace in the places [Adapt to your org](../examples/argocd/README.md#adapt-to-your-org) lists, and read `-n argocd` in the commands of this guide as your Argo CD namespace.
- An ingress controller with TLS for the badil webhook URL (GitHub calls it over https), plus wildcard DNS when preview URLs are used.
- A GitHub gitops repo, and service repos whose CI pushes images tagged as the `imageTag` format of `badil.yaml` renders the commit SHA (the full SHA by default), all under the one account (user or org) the App is installed on. `/v1/deploy` must send the full SHA: see [`imageTag`](CONFIGURATION.md#imagetag).
- Service images pullable from the preview namespaces: `preview-apps` creates each namespace fresh, with no `imagePullSecrets`. Use public images, node-level registry credentials, or pull secrets set by the service chart.
- `kubectl`, Helm 3.8+ and Go 1.26+ (for the `badil` CLI).

## 2. Gitops repo

Create the gitops repo from [`badilhq/gitops-template`](https://github.com/badilhq/gitops-template) with "Use this template", follow its README, which covers this step and §3, then continue at [§4](#4-public-url-for-badil).

Or set it up by hand:

- Commit at the root a `badil.yaml`, starting from [`examples/argocd/badil.yaml`](../examples/argocd/badil.yaml): the service catalog, and optionally the image tag format, namespace metadata, dependencies, needs and the `preview` block. Every key is in the [Configuration reference](CONFIGURATION.md#badilyaml).
- badil creates `previews/` itself; a missing directory reads as no previews.
- badil commits straight to the default branch. Its protection must let the App push directly: no required pull request, or the App on the bypass list.
- The default branch must not require signed commits: badil pushes unsigned commits, authored and committed by the App's bot.

## 3. Argo CD wiring

Skip this step if the gitops repo comes from the template: its README covers it.

- If the gitops repo or any service repo is private, register it in Argo CD first (repository credentials; see Argo CD [Private Repositories](https://argo-cd.readthedocs.io/en/stable/user-guide/private-repositories/)). The public `oci://ghcr.io/badilhq/charts` needs no credentials.
- A dependency from a private OCI registry or Helm repository needs a credential too: see [`dependencies`](CONFIGURATION.md#dependencies).
- Copy [`examples/argocd/appproject-previews.yaml`](../examples/argocd/appproject-previews.yaml) and [`applicationset-previews.yaml`](../examples/argocd/applicationset-previews.yaml), and adapt both as listed in [Adapt to your org](../examples/argocd/README.md#adapt-to-your-org).
- Take the files from the badil release tag you install (`https://github.com/badilhq/badil/tree/vX.Y.Z/examples/argocd`): they carry the matching `preview-apps` `targetRevision`. For the latest chart, use the latest release tag: `main` can be ahead of it.
- Apply both:

  ```bash
  kubectl apply -f appproject-previews.yaml
  kubectl apply -f applicationset-previews.yaml
  ```

  Re-apply after a chart bump: Argo CD does not manage the ApplicationSet.

## 4. Public URL for badil

Choose the https host GitHub will call, e.g. `https://badil.example.com`, and create its A or CNAME record pointing at the ingress controller. The chart's Ingress serves it (step 6), and it is the webhook base URL. The Ingress routes only `POST /webhooks/github`; set `ingress.api: true` (step 6) when CI jobs, agents or the CLI call `/v1` through it.

## 5. GitHub App

1. Install the CLI:

   ```bash
   go install github.com/badilhq/badil/cmd/badil@latest
   ```

   `$(go env GOPATH)/bin` must be on `PATH`. Use `@vX.Y.Z` to match a pinned chart. `badil version` prints the installed version.
2. In an empty working directory, create the App. It writes `.env` and `dev.private-key.pem` there.
   - Run it on a machine with a desktop browser signed in to GitHub, not over SSH on a remote host: it listens on `127.0.0.1` and waits 5 minutes for the browser redirect. Otherwise create the App by hand (item 5).
   - The App is private, so it can only be installed on the account that owns it. When the repos are in an org, `--owner <org>` is required, and creating the App needs org owner (or App manager) rights.

   ```bash
   APP_NAME=my-badil            # unique on GitHub
   GITOPS=https://github.com/my-org/gitops.git   # clone URL
   ORG=my-org                      # org that owns the repos
   ```

   Repos in an org:

   ```bash
   badil bootstrap-app --name "$APP_NAME" \
     --webhook-url https://badil.example.com \
     --gitops-repo-url "$GITOPS" --owner "$ORG"
   ```

   Repos in your personal account:

   ```bash
   badil bootstrap-app --name "$APP_NAME" \
     --webhook-url https://badil.example.com \
     --gitops-repo-url "$GITOPS"
   ```
3. Install the App on the gitops repo and every service repo, all under that one account. badil accepts webhooks from that single installation only.
   The App subscribes to `pull_request` and `push`. An App created before `push` was added does not receive it: in the App settings, **Permissions & events** → **Subscribe to events**, tick **Push** and click **Save changes**. `push` needs Contents read, which the App already has, so installations approve nothing. Without it, a `badil.yaml` change reaches previews only at their next deploy or sweep, or through `POST /v1/sync`.
4. In the same directory, list the installations:

   ```bash
   . ./.env
   badil verify-app
   ```

   Put the listed `id=` into `GITHUB_INSTALLATION_ID` in `.env`, then run `. ./.env` again.
5. Alternative: create the App by hand following [DEVELOPMENT.md Appendix B](../DEVELOPMENT.md#appendix-b--manual-app-creation-manifest-flow-fallback), with the webhook URL `<base>/webhooks/github`, and write the same `.env` keys yourself.

`.env` and the PEM are secrets: keep them out of git and store them in a secret manager. The PEM is the only copy; if lost, generate a new private key in the App settings.

## 6. Install badil

GitHub needs https, so set `ingress.className` and `ingress.tls` in a `values.yaml`:

```yaml
ingress:
  className: nginx                # your IngressClass
  tls:
    - hosts: [badil.example.com]
      secretName: badil-tls
  # api: true                     # also route /v1, for CI jobs, agents and the CLI outside the cluster
```

The Ingress routes only `POST /webhooks/github`, the URL GitHub calls. Set `ingress.api: true` when CI jobs, agents or the CLI call `/v1` through it. It never routes `/healthz` or `/webhooks/status`.

The chart has no `ingress.annotations`: the TLS Secret must exist beforehand (created by you, or by a cert-manager `Certificate`), or the controller's default certificate must be valid for the host. On a local test cluster you can skip the file and use http.

From the directory holding `.env`, after `. ./.env`:

```bash
kubectl create namespace badil
kubectl create secret generic badil -n badil \
  --from-file=github-private-key="$GITHUB_PRIVATE_KEY_PATH" \
  --from-literal=github-webhook-secret="$GITHUB_WEBHOOK_SECRET" \
  --from-literal=deploy-token="$BADIL_DEPLOY_TOKEN"
helm install badil oci://ghcr.io/badilhq/charts/badil -n badil \
  --set-string config.githubAppId="$GITHUB_APP_ID" \
  --set-string config.githubInstallationId="$GITHUB_INSTALLATION_ID" \
  --set config.gitopsRepoURL="$GITOPS_REPO_URL" \
  --set existingSecret=badil \
  --set ingress.enabled=true --set ingress.host=badil.example.com \
  -f values.yaml   # leave this line out on a local http cluster
```

- Without `--version`, Helm installs the latest chart. Add `--version X.Y.Z` to pin a release (do so in production), and take the step 3 example files from the same `vX.Y.Z` tag. The image tag defaults to the chart `appVersion`.
- When the Secret has `deploy-token`, badil serves `POST /v1/deploy`, the manual trigger, and `POST /v1/sync`, which syncs every preview (or one) with `badil.yaml` at once, both protected by that bearer token (see [README](../README.md)). `deploy-token` is optional: leave out the `--from-literal=deploy-token=...` line to turn both endpoints off (404). When the Secret has `read-token` or `deploy-token`, badil serves the read routes `GET /v1/previews` and `GET /v1/wait`, protected by either token (see [Wait for a preview](#wait-for-a-preview)). The Ingress routes `POST /v1/deploy`, `POST /v1/sync`, `GET /v1/previews` and `GET /v1/wait` only with `ingress.api: true`. `POST /webhooks/status`, the renderer's status callback, is protected by the optional `status-token` bearer token (see [Preview status](#preview-status-optional)); the Ingress never routes it, and Argo CD reaches it through the Service.
- badil's git calls to the gitops repo authenticate as the App installation. To use a token instead, add `--from-literal=gitops-repo-token=<token>` to the Secret, and `--set config.gitopsRepoUsername=<username>` when the host needs a username other than `x-access-token` (see [Gitops repo credentials](CONFIGURATION.md#gitops-repo-credentials)). Pushes then run as the token's owner.
- `cleanup.ttl` (e.g. `168h`) is optional: it also deletes previews with no deploy for that long.
- The cleanup sweep runs in the server every hour by default (`cleanup.interval`, `1h`): it deletes stale previews and rewrites every kept preview to match `badil.yaml`, and exports its metrics (with the Prometheus Operator, scrape them with `metrics.serviceMonitor.enabled=true` and `metrics.serviceMonitor.labels` matching its selector, e.g. `release: <kube-prometheus-stack release>`). `cleanup.enabled=true` switches it to an hourly CronJob without metrics and turns the in-server sweep off.
- The chart also installs a ClusterRole and ClusterRoleBinding, `<release>-namespaces`, that let badil get namespaces and nothing else: before creating a preview, badil reads the namespace named after its slug and refuses the preview when that namespace exists and is not a preview (see [Namespace in use](EVENTS.md#namespace-in-use)).
- Keep `replicaCount: 1`.

Once the Secret exists, delete the local `.env` and PEM. The non-secret values you set stay readable with `helm get values badil -n badil`, which the [upgrade](#10-upgrade) uses.

Check:

```bash
curl -s -o /dev/null -w '%{http_code}' -X POST https://badil.example.com/webhooks/github   # 401
```

### Preview status (optional)

To let badil know how each preview service is doing, and measure how long it takes to come up (`badil_preview_spinup_seconds`), Argo CD's notifications controller sends badil's [status callback](CONTRACT.md#status-callback) when a service starts a sync, is Healthy, turns Degraded or fails to sync. Generate a token (e.g. `openssl rand -hex 32`) as `BADIL_STATUS_TOKEN`, add it to the badil Secret as `status-token` (a merge patch keeps the other keys) and restart badil, then apply [`examples/argocd/notifications-values.yaml`](../examples/argocd/notifications-values.yaml) to your Argo CD release:

```bash
kubectl patch secret badil -n badil --type merge \
  -p "{\"stringData\":{\"status-token\":\"$BADIL_STATUS_TOKEN\"}}"
kubectl rollout restart deployment/badil -n badil
helm upgrade argocd argo/argo-cd -n argocd --version <your installed argo-cd chart version> \
  --reuse-values -f notifications-values.yaml \
  --set-string notifications.secret.items.badil-status-token="$BADIL_STATUS_TOKEN"
```

badil keeps each preview's status in a ConfigMap `badil-status-<slug>` of its namespace: the chart gives its Deployment a Role for ConfigMaps in that namespace only. Applying the notifications while pinned previews exist produces one-off spin-up samples for them, measured from their last deploy.

Check: `badil_status_events_total{result="recorded"}` increases after the next labeled PR, and `kubectl get configmaps -n badil -l badil.dev/preview` lists the preview's status. Without Prometheus, read the metric through the API server:

```bash
kubectl get --raw /api/v1/namespaces/badil/services/badil:metrics/proxy/metrics | grep badil_status_events_total
```

Without Helm, see the file's comments. To remove the setup later, revert the Argo CD release without the file, then delete the token, which Helm leaves in the Secret: `kubectl -n argocd patch secret argocd-notifications-secret --type=json -p '[{"op":"remove","path":"/data/badil-status-token"}]'`. Details: [Preview status](OPERATIONS.md#preview-status) and [Spin-up time](OPERATIONS.md#spin-up-time).

### Preview deployments on GitHub (optional)

To show each pinned preview service on its PR as a GitHub deployment (with a "View deployment" button, and a `deployment_status` event for anything that waits on it), Argo CD's notifications controller posts GitHub Deployments when the service's Application is synced and Healthy, Degraded, or fails to sync. Argo CD decides health, not badil. It requires [Preview status](#preview-status-optional).

Create a second GitHub App, for Argo CD:

- No webhook.
- Repository permissions: Deployments Read and write, Metadata Read.
- Install it on every service repo, under one installation (one owner): Argo CD takes a single installation ID. Note the App ID, the installation ID, and generate a private key (`.pem`).

Then apply [`examples/argocd/github-deployments-values.yaml`](../examples/argocd/github-deployments-values.yaml) to your Argo CD release. The file order matters: `github-deployments-values.yaml` must come after `notifications-values.yaml`, because it carries the full `subscriptions` list and Helm replaces lists.

```bash
helm upgrade argocd argo/argo-cd -n argocd --version <your installed argo-cd chart version> \
  --reuse-values -f notifications-values.yaml -f github-deployments-values.yaml \
  --set-string notifications.secret.items.github-appID="$ARGOCD_GITHUB_APP_ID" \
  --set-string notifications.secret.items.github-installationID="$ARGOCD_GITHUB_INSTALLATION_ID" \
  --set-file notifications.secret.items.github-privateKey=<path to the App's .pem>
```

On the PR, each pinned service that goes Healthy at the PR head SHA appears as a deployment to the environment `preview-<slug>-<service>`, state `success`, with `environment_url` set to `https://<service host>/`.

A service that turns Degraded at the PR head SHA (image never pushed, crash loop) shows on the same deployment as state `failure`, after its Deployment's `progressDeadlineSeconds`; one whose sync fails or whose chart fails to render at that SHA shows as state `error`.

When the service's Application is deleted, the deployment of the SHA badil last pinned is marked `inactive`. When badil unpins a service from a preview that survives (a later push that no longer touches its `paths`, or a PR closed while another PR keeps the preview), the deployment of its last pinned SHA is marked `inactive`.

Each sync at the pinned SHA posts `success` once and each unpin of a pinned sync posts `inactive` once: re-adding the label (or reopening the PR) at the same SHA posts `success` again on the same deployment once Argo CD syncs it at that SHA ([limitation 15](OPERATIONS.md#limitations)), and so does any further successful sync at that SHA.

The file uses `https://`; change every `environmentURL` line to `http://` if your previews run with `preview.tls: false`.

The known limits of this setup are listed in [Limitations](OPERATIONS.md#limitations).

Check: the PR shows the deployment, and `kubectl -n argocd logs deploy/argocd-notifications-controller` has no `github` errors. Without Helm, see the file's comments. Details: [Preview deployments on GitHub](OPERATIONS.md#preview-deployments-on-github).

To remove the setup later:

1. Re-run the [Preview status](#preview-status-optional) `helm upgrade` with `-f notifications-values.yaml`, setting the three secret items to `null` so they leave the release values:

   ```bash
   helm upgrade argocd argo/argo-cd -n argocd --version <your installed argo-cd chart version> \
     --reuse-values -f notifications-values.yaml \
     --set notifications.secret.items.github-appID=null \
     --set notifications.secret.items.github-installationID=null \
     --set notifications.secret.items.github-privateKey=null
   ```

   This restores the single-entry `subscriptions` list. `service.github` and the `template.badil-preview-*` and `trigger.on-preview-*` keys stay in the release: they are maps, so `--reuse-values` keeps them, but nothing subscribes to them.
2. Helm no longer renders these items, but removing a `stringData` key does not delete it from the live Secret's `data`, so delete them:

   ```bash
   kubectl -n argocd patch secret argocd-notifications-secret --type=json -p '[{"op":"remove","path":"/data/github-appID"},{"op":"remove","path":"/data/github-installationID"},{"op":"remove","path":"/data/github-privateKey"}]'
   ```

### Monitoring (optional)

With the Prometheus Operator (e.g. kube-prometheus-stack), add to the `helm install` (or a `helm upgrade --reuse-values`):

```bash
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.serviceMonitor.labels.release=<kube-prometheus-stack release> \
  --set dashboard.enabled=true
```

- `metrics.serviceMonitor.labels.release` must match the operator's `serviceMonitorSelector`; kube-prometheus-stack selects its own release name by default.
- `dashboard.enabled=true` renders the Grafana dashboard as a ConfigMap labelled `grafana_dashboard: "1"`, which the Grafana sidecar of kube-prometheus-stack loads from any namespace. Change the label with `dashboard.labels` if your sidecar uses another one.
- Without the sidecar, import [`charts/badil/dashboards/badil.json`](../charts/badil/dashboards/badil.json) as a Grafana dashboard and pick your Prometheus data source.

Last successful sweep needs `cleanup.interval`; the spin-up panels need [Preview status](#preview-status-optional). Details: [Dashboard](OPERATIONS.md#dashboard).

### Preview limits (optional)

A preview runs the code of its PRs in your cluster, with no limits unless you add them. badil sets no quota or network policy on previews. To cap each preview's resources and isolate its ingress, apply the Kyverno policy in [`examples/kyverno/`](../examples/kyverno/README.md) (Kyverno v1.19.0 or later): it creates a ResourceQuota, a LimitRange and an ingress NetworkPolicy in every preview namespace. Adapt its quota numbers and its ingress controller namespace first. Details: [Preview limits](CONFIGURATION.md#preview-limits).

### Namespace guard (optional)

badil refuses a new preview whose namespace already exists and is not a preview (see [Namespace in use](EVENTS.md#namespace-in-use)), but only for the previews it writes itself. A preview file written to the gitops repo by anything else, or a namespace created between badil's check and the renderer's sync, still lets the renderer adopt that namespace, and the teardown then deletes it with everything in it. The namespace guard closes that gap in the cluster: a ValidatingAdmissionPolicy refuses any update or delete of a namespace by the ServiceAccount of the renderer that deploys previews unless the namespace already carries the label `badil.dev/preview`, whoever wrote the preview file. Requests from any other user, and the renderer's creation of a namespace, are not affected.

The policy covers every namespace that identity updates or deletes, not only previews. Enable the guard only when the renderer's ServiceAccount manages no namespace other than previews.

It needs Kubernetes 1.30 or later. Add to the `helm install` (or a `helm upgrade --reuse-values`):

```bash
  --set namespaceGuard.enabled=true
```

The policy matches the renderer by its ServiceAccount, set with `namespaceGuard.renderer.namespace` and `namespaceGuard.renderer.serviceAccount`. The default is Argo CD's application controller, `system:serviceaccount:argocd:argocd-application-controller`. For Argo CD in another namespace or under another ServiceAccount, or another renderer, set both to the ServiceAccount the renderer applies the preview manifests as: its controller pod's `serviceAccountName`, unless the renderer is configured to apply as another ServiceAccount. A wrong identity leaves the guard inactive without error. Read Argo CD's application controller ServiceAccount with:

```bash
kubectl get statefulset argocd-application-controller -n argocd -o jsonpath='{.spec.template.spec.serviceAccountName}'
```

Check: `kubectl get validatingadmissionpolicy,validatingadmissionpolicybinding badil-namespace-guard` lists both. What a refused preview looks like: [Namespace guard](OPERATIONS.md#namespace-guard). Values: [Namespace guard](CONFIGURATION.md#namespace-guard).

## 7. Service repos

- Create the `preview` label (or your custom `preview.label`) in each service repo, e.g. `gh label create preview --repo <owner>/<repo>`. Create any `preview:<name>` label yourself in each repo too: badil never creates labels.
- CI must push the image tagged as the service's `imageTag` format renders the commit SHA (the full SHA by default): [Pinned and branch-tracking services](ARCHITECTURE.md#pinned-and-branch-tracking-services).
- Chart contract and the `services.<name>.chart` settings (in `badil.yaml`): [Service repo contract](CONFIGURATION.md#service-repo-contract).

## 8. First preview

1. Label a PR `preview`.
2. Expect:
   - a badil comment on the PR and `previews/<slug>/values.yaml` in the gitops repo, once badil has processed the webhook;
   - within the ApplicationSet poll (3 minutes by default), one Argo CD Application per service and per dependency.
3. With preview URLs, the comment links each exposed service.
4. Removing the label or closing the PR tears the preview down.
5. PRs from forks never get a preview.

### Install the badil CLI

`badil list` and `badil wait` ship as release binaries for linux and darwin, on amd64 and arm64. With the release `X.Y.Z`:

```bash
BADIL_VERSION=X.Y.Z
OS=linux ARCH=amd64   # or darwin, arm64
base=https://github.com/badilhq/badil/releases/download/v$BADIL_VERSION
curl -fsSLO "$base/badil_${BADIL_VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "$base/badil_${BADIL_VERSION}_checksums.txt"
sha256sum --ignore-missing -c "badil_${BADIL_VERSION}_checksums.txt"
tar -xzf "badil_${BADIL_VERSION}_${OS}_${ARCH}.tar.gz" badil
sudo install badil /usr/local/bin/
```

Each archive holds `badil`, `LICENSE` and `README.md`. With a Go toolchain instead:

```bash
go install github.com/badilhq/badil/cmd/badil@latest
```

### Wait for a preview

A CI job or an agent can wait until a PR's preview is ready at its commit, or exit at once when the preview failed or was refused, instead of sleeping for a fixed time. Generate a read token (e.g. `openssl rand -hex 32`) as `BADIL_READ_TOKEN`, add it to the badil Secret as `read-token` and restart badil. The deploy token is accepted too, but it also grants deploys. A preview reaches `ready` only through the [status callback](#preview-status-optional): without it badil never learns a service's state, so a pinned PR stays `progressing` and `badil wait` exits 2 when `--timeout` passes.

```bash
kubectl patch secret badil -n badil --type merge \
  -p "{\"stringData\":{\"read-token\":\"$BADIL_READ_TOKEN\"}}"
kubectl rollout restart deployment/badil -n badil
```

A `BADIL_SERVER` reached through the chart's Ingress needs `ingress.api: true` ([Install badil](#6-install-badil)). Then, where the job runs, with the CLI installed ([Install the badil CLI](#install-the-badil-cli)):

```bash
export BADIL_SERVER=https://badil.example.com BADIL_TOKEN="$BADIL_READ_TOKEN"
badil wait --repo acme/backend-1 --pr 42 --sha "$HEAD_SHA"   # or: badil wait --slug feature-pricing
```

`badil wait` exits 0 when the preview is ready or the PR needs none, 1 when it failed or was refused, 2 when `--timeout` (default `15m`) passes first, and 3 on an error. `badil list` lists the previews. Details: [`GET /v1/wait`](API.md#get-v1wait) and [`badil wait`](API.md#badil-wait).

### Wait for a preview in CI

Both jobs pin a release, check its checksum, then run `badil wait`, which fails the job unless the preview is ready or the PR needs none. A `BADIL_SERVER` reached through the chart's Ingress needs `ingress.api: true` ([Install badil](#6-install-badil)).

GitHub Actions, with the repository variable `BADIL_SERVER` and the secret `BADIL_READ_TOKEN`. The step writes the preview URLs to its `urls` output:

```yaml
on:
  pull_request:
    # labeled: a PR labeled after it was opened is waited for too.
    types: [opened, synchronize, reopened, labeled]

jobs:
  preview:
    runs-on: ubuntu-24.04
    outputs:
      urls: ${{ steps.wait.outputs.urls }}
    steps:
      - name: Install badil
        env:
          BADIL_VERSION: X.Y.Z
        run: |
          cd "$RUNNER_TEMP"
          base=https://github.com/badilhq/badil/releases/download/v$BADIL_VERSION
          curl -fsSLO "$base/badil_${BADIL_VERSION}_linux_amd64.tar.gz"
          curl -fsSLO "$base/badil_${BADIL_VERSION}_checksums.txt"
          sha256sum --ignore-missing -c "badil_${BADIL_VERSION}_checksums.txt"
          tar -xzf "badil_${BADIL_VERSION}_linux_amd64.tar.gz" badil
          echo "$RUNNER_TEMP" >> "$GITHUB_PATH"

      - id: wait
        name: Wait for the preview
        env:
          BADIL_SERVER: ${{ vars.BADIL_SERVER }}
          BADIL_TOKEN: ${{ secrets.BADIL_READ_TOKEN }}
        run: |
          badil wait --json --repo "$GITHUB_REPOSITORY" --pr "${{ github.event.number }}" \
            --sha "${{ github.event.pull_request.head.sha }}" > "$RUNNER_TEMP/wait.json"
          echo "urls=$(jq -c .urls "$RUNNER_TEMP/wait.json")" >> "$GITHUB_OUTPUT"
```

GitLab CI, in the [external pull request pipelines](https://docs.gitlab.com/ci/ci_cd_for_external_repos/#pipelines-for-external-pull-requests) of a GitHub repo, with the CI/CD variables `BADIL_SERVER` and `BADIL_TOKEN` (the read token, masked). BusyBox `sha256sum` has no `--ignore-missing`, so the job checks the archive's line only:

```yaml
wait-for-preview:
  image: alpine:3.24
  rules:
    - if: $CI_PIPELINE_SOURCE == "external_pull_request_event"
  variables:
    BADIL_VERSION: X.Y.Z
  script:
    - base=https://github.com/badilhq/badil/releases/download/v$BADIL_VERSION
    - wget -q "$base/badil_${BADIL_VERSION}_linux_amd64.tar.gz"
    - wget -q "$base/badil_${BADIL_VERSION}_checksums.txt"
    - grep " badil_${BADIL_VERSION}_linux_amd64.tar.gz\$" "badil_${BADIL_VERSION}_checksums.txt" | sha256sum -c -
    - tar -xzf "badil_${BADIL_VERSION}_linux_amd64.tar.gz" badil
    - ./badil wait --json --repo "$CI_EXTERNAL_PULL_REQUEST_TARGET_REPOSITORY" --pr "$CI_EXTERNAL_PULL_REQUEST_IID" --sha "$CI_EXTERNAL_PULL_REQUEST_SOURCE_BRANCH_SHA"
```

## 9. Preview URLs (optional)

Set `preview.domain` in `badil.yaml`, create a wildcard DNS record `*.<domain>` pointing to the ingress controller, and list the services under `expose`. Details: [Per-preview URLs](CONFIGURATION.md#per-preview-urls).

## 10. Upgrade

1. Upgrade badil, with the values of the installed release:

   ```bash
   helm get values badil -n badil -o yaml > badil-values.yaml
   helm upgrade badil oci://ghcr.io/badilhq/charts/badil -n badil --version X.Y.Z -f badil-values.yaml
   ```
2. Set the ApplicationSet's `preview-apps` `targetRevision` and `valueFiles` to the ones in the new tag's [`examples/argocd/applicationset-previews.yaml`](../examples/argocd/applicationset-previews.yaml), and re-apply it: Argo CD does not manage the ApplicationSet.

## 11. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Deliveries return 404 on `POST /` | The webhook URL lacks `/webhooks/github` | Set the App's webhook URL to `<base>/webhooks/github` |
| Deliveries return 401 `invalid signature` | `github-webhook-secret` differs from the App's webhook secret | Make the Secret key and the App's webhook secret match, then restart badil |
| Deliveries return 403 `installation not allowed` | `config.githubInstallationId` is wrong, or the repo is under another account | Set the ID listed by `badil verify-app`; install the App on repos of that account only |
| Deliveries time out or fail to connect | The ingress or DNS is unreachable from GitHub | Expose the host publicly over https and check from outside with `curl -s -o /dev/null -w '%{http_code}' -X POST https://<host>/webhooks/github`, which answers `401` |
| `preview reconcile failed … badil.yaml: …` in `kubectl logs -n badil deploy/badil` | `badil.yaml` is malformed; the error names the key | Fix that key in `badil.yaml` |
| `preview reconcile skipped … reason="repo not in catalog"` | The `services.<name>.repo` URL isn't `<GITHUB_URL>/<owner>/<repo>[.git]` (`GITHUB_URL` defaults to `https://github.com`) | Fix the URL in `badil.yaml` |
| `preview reconcile skipped … reason="fork pr"` | The PR comes from a fork | Expected: fork PRs never get a preview |
| `preview reconcile skipped … reason="no service changed"` | No changed file of the PR matches any `paths` of its repo's services | Fix `paths` in `badil.yaml` |
| `preview reconcile skipped … reason="namespace in use"` | A namespace named after the preview's slug already exists and is not a preview | Rename the branch, or use a `preview:<name>` label |
| `preview reconcile failed … namespace check failed: … forbidden` | badil's ServiceAccount cannot get namespaces: the chart's ClusterRole or ClusterRoleBinding is missing | Reinstall the chart, or restore `<release>-namespaces` |
| No comment after labeling | The App isn't installed on that repo, or the label differs from `preview.label` | Install the App on the repo, or use the configured label |
| Pods in `ImagePullBackOff` | The image isn't pushed yet, or the namespace can't pull from the registry | If CI is still pushing, wait; otherwise check registry access (prerequisites) |
