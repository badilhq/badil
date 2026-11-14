# ArgoCD example

The ArgoCD side of badil: an AppProject and an ApplicationSet that turn every `previews/<slug>/` directory of your gitops repo into one preview environment, rendered by the `preview-env` chart.

Requires Argo CD 3.1 or later (OCI chart source).

## Gitops repo contract

- [`badil.yaml`](badil.yaml) at the repo root, read by badil only: the service catalog (each service's `repo` and optional `branch`) and the optional `preview` block. The copy here lists the demo services.
- [`preview-env.yaml`](preview-env.yaml) at the repo root, read by `preview-env` only: the optional `serviceCharts` and `previewNamespace` blocks. The copy here sets none (`{}`).
- badil writes `previews/<slug>/values.yaml`, one per preview, with the services it runs and their PR metadata. Sample: [`values.golden.yaml`](../../internal/preview/testdata/values.golden.yaml).
- The ApplicationSet renders `preview-env` with `preview-env.yaml` and `previews/<slug>/values.yaml` as value files.

## Apply

1. `badil.yaml` and `preview-env.yaml`: commit both to the root of your gitops repo.
2. `appproject-previews.yaml`: `kubectl apply -f appproject-previews.yaml`
3. `applicationset-previews.yaml`: `kubectl apply -f applicationset-previews.yaml`

## Preview URLs

To give each preview its own URL, uncomment the `preview` block in `badil.yaml` and set `domain`, then create a wildcard DNS record for `*.<domain>` pointing at your ingress controller. Each service listed under `expose` gets the ingress host `<service>-<slug>.<domain>` (capped to a 63-character label, see ARCHITECTURE) and a link in the PR comment; its chart must enable ingress and honor `app.ingress.host` (or its `hostParam`). This needs `preview-env` 0.3.0 or later and badil 0.10.0 or later: after changing the chart version (`targetRevision`) in `applicationset-previews.yaml`, re-apply it with `kubectl apply -f applicationset-previews.yaml`, as Argo CD does not manage the ApplicationSet itself.

Per-service chart settings go in `serviceCharts` of `preview-env.yaml`; a service's branch goes in `services.<name>.branch` of `badil.yaml`.

Preview Namespace labels and annotations go in `previewNamespace` of `preview-env.yaml`. For per-preview limits on top of them, see the Kyverno example in [`../kyverno/`](../kyverno/README.md).

## Spin-up time

[`notifications-values.yaml`](notifications-values.yaml): Argo CD notifications that report each preview service's spin-up time to badil (`/webhooks/argocd`); see [INSTALL](../../docs/INSTALL.md#spin-up-time-optional).

## Adapt to your org

- Replace `https://github.com/badilhq/demo-gitops.git` with your gitops repo URL, in both the AppProject and the ApplicationSet.
- Replace `oci://ghcr.io/badilhq/charts/preview-env` if you host the chart elsewhere.
- Replace the `services` entries in `badil.yaml` with your services.
- If Argo CD runs in a namespace other than `argocd`, set it in: the ApplicationSet's `metadata.namespace`, its `destination.namespace`, and its `argocdNamespace` Helm parameter; the AppProject's `metadata.namespace`, and its destination namespace when it is `argocd`.

Charts are published to `oci://ghcr.io/badilhq/charts/*`.

Install badil: [docs/INSTALL.md](../../docs/INSTALL.md).
