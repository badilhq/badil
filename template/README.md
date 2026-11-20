# Gitops repo for badil

A starting point for the gitops repo of [badil](https://github.com/badilhq/badil), generated from badil {{VERSION}}.

- `badil.yaml`: badil's configuration: the service catalog and the optional keys, commented.
- `previews/`: badil writes `previews/<slug>/values.yaml`, one per preview.
- `argocd/`: the AppProject and the ApplicationSet that turn each `previews/<slug>/` into a preview environment.

## Setup

1. Replace `https://github.com/YOUR-ORG/YOUR-GITOPS-REPO.git` with this repo's URL in both `argocd/` files.
2. If Argo CD runs in a namespace other than `argocd`, or the `preview-apps` chart is hosted elsewhere than `oci://ghcr.io/badilhq/charts`, adapt both `argocd/` files as listed in [Adapt to your org](https://github.com/badilhq/badil/blob/{{VERSION}}/examples/argocd/README.md#adapt-to-your-org).
3. List your services in `badil.yaml` (replace `my-service`).
4. Let the badil GitHub App push straight to the default branch: no required pull request in its protection, or the App on the bypass list. Do not require signed commits on it: badil pushes unsigned commits.
5. If this repo or any service repo is private, register it in Argo CD first (Argo CD [Private Repositories](https://argo-cd.readthedocs.io/en/stable/user-guide/private-repositories/)). A dependency from a private OCI registry or Helm repository needs a credential too: see [Dependencies](https://github.com/badilhq/badil/blob/{{VERSION}}/docs/CONFIGURATION.md#dependencies).
6. Apply both `argocd/` files:

   ```bash
   kubectl apply -f argocd/appproject-previews.yaml -f argocd/applicationset-previews.yaml
   ```

   Re-apply them after a chart bump: Argo CD does not manage the ApplicationSet.
7. Continue the badil install at [§4 Public URL for badil](https://github.com/badilhq/badil/blob/{{VERSION}}/docs/INSTALL.md#4-public-url-for-badil).
8. Optional: preview status, spin-up time and GitHub deployments through Argo CD notifications, set up as [Preview status](https://github.com/badilhq/badil/blob/{{VERSION}}/docs/INSTALL.md#preview-status-optional) and [Preview deployments on GitHub](https://github.com/badilhq/badil/blob/{{VERSION}}/docs/INSTALL.md#preview-deployments-on-github-optional) describe.
