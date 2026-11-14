# Kyverno example

[`preview-guardrails.yaml`](preview-guardrails.yaml): one Kyverno `GeneratingPolicy` that creates, in every preview namespace, three objects named `preview-guardrails`:

- a ResourceQuota (`requests.cpu`, `requests.memory`, `limits.memory`, `pods`);
- a LimitRange with default requests and limits for every container;
- a NetworkPolicy that admits ingress only from pods of the same preview and from the ingress controller's namespace.

It selects preview namespaces by the label `app.kubernetes.io/part-of: preview-environments`, which the `preview-env` chart sets. Kyverno creates the objects, not Argo CD, so the `previews` AppProject needs no change. Teardown deletes them with the preview namespace.

## Prerequisites

- Kyverno v1.19.0 or later.
- Kyverno's background controller may create ResourceQuotas, LimitRanges and NetworkPolicies. The Kyverno Helm chart's default RBAC (`backgroundController.rbac.coreClusterRole.extraResources`) already grants them; other installs add a ClusterRole labelled `rbac.kyverno.io/aggregate-to-background-controller: "true"`.
- A CNI that enforces NetworkPolicies.

## Adapt

- The quota numbers and the LimitRange defaults. A quota on `requests.cpu`, `requests.memory` or `limits.memory` refuses pods without them; the LimitRange gives defaults, so keep both.
- The ingress controller's namespace (`ingress-nginx`) in the NetworkPolicy.

## Apply

```sh
kubectl apply -f preview-guardrails.yaml
```

Existing previews get the objects when the policy is applied (`generateExisting`). Editing the policy updates them in every preview (`synchronize`).

## Remove

```sh
kubectl delete -f preview-guardrails.yaml
```

This also deletes the generated objects in every preview, because `orphanDownstreamOnPolicyDelete` defaults to false.
