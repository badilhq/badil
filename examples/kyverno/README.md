# Kyverno examples

Two Kyverno `GeneratingPolicy` examples for preview namespaces:

- [`preview-guardrails.yaml`](preview-guardrails.yaml): a ResourceQuota, a LimitRange and an ingress NetworkPolicy in every preview (see [Preview guardrails](#preview-guardrails));
- [`preview-secrets.yaml`](preview-secrets.yaml): copies of a registry pull Secret and an app Secret in every preview, with the ClusterRole Kyverno needs to write them (see [Preview secrets](#preview-secrets)).

Both select preview namespaces by the label `badil.dev/preview`, which the `preview-apps` chart sets. Kyverno creates the objects, not Argo CD, so the `previews` AppProject needs no change. Teardown deletes them with the preview namespace.

## Preview guardrails

[`preview-guardrails.yaml`](preview-guardrails.yaml) creates, in every preview namespace, three objects named `preview-guardrails`:

- a ResourceQuota (`requests.cpu`, `requests.memory`, `limits.memory`, `pods`);
- a LimitRange with default requests and limits for every container;
- a NetworkPolicy that admits ingress only from pods of the same preview and from the ingress controller's namespace.

### Prerequisites

- Kyverno v1.19.0 or later.
- Kyverno's background controller may create ResourceQuotas, LimitRanges and NetworkPolicies. The Kyverno Helm chart's default RBAC (`backgroundController.rbac.coreClusterRole.extraResources`) already grants them; other installs add a ClusterRole labelled `rbac.kyverno.io/aggregate-to-background-controller: "true"`.
- A CNI that enforces NetworkPolicies.

### Adapt

- The quota numbers and the LimitRange defaults. A quota on `requests.cpu`, `requests.memory` or `limits.memory` refuses pods without them; the LimitRange gives defaults, so keep both.
- The ingress controller's namespace (`ingress-nginx`) in the NetworkPolicy.

### Apply

```sh
kubectl apply -f preview-guardrails.yaml
```

Existing previews get the objects when the policy is applied (`generateExisting`). Editing the policy updates them in every preview (`synchronize`).

### Remove

```sh
kubectl delete -f preview-guardrails.yaml
```

This also deletes the generated objects in every preview, because `orphanDownstreamOnPolicyDelete` defaults to false.

## Preview secrets

A preview namespace starts empty. [`preview-secrets.yaml`](preview-secrets.yaml) copies two Secrets of the namespace `badil-secrets` into every preview namespace: `registry-pull`, to pull private images, and `app-env`, the app's environment. It holds two objects:

- a ClusterRole `badil-kyverno-preview-secrets`, labelled `rbac.kyverno.io/aggregate-to-background-controller: "true"`, that gives Kyverno's background controller read and write on every Secret in the cluster. The Kyverno Helm chart's default RBAC grants no Secrets;
- a GeneratingPolicy `badil-preview-secrets` that clones both Secrets. The copies stay in sync with the sources: editing a source updates its copies, deleting a source deletes them.

### Prerequisites

- Kyverno v1.19.0 or later.
- The source namespace and Secrets, created before the policy:

```sh
kubectl create namespace badil-secrets
kubectl create secret docker-registry registry-pull -n badil-secrets \
  --docker-server=<registry> --docker-username=<user> --docker-password=<token>
kubectl create secret generic app-env -n badil-secrets \
  --from-literal=<KEY>=<value>
```

### Adapt

- The source namespace `badil-secrets` and the Secret names `registry-pull` and `app-env`, in the policy's `resource.Get` calls.

### Apply

```sh
kubectl apply -f preview-secrets.yaml
```

Existing previews get the copies when the policy is applied (`generateExisting`).

### Check

```sh
kubectl auth can-i create secrets --as system:serviceaccount:kyverno:kyverno-background-controller
kubectl get secret registry-pull app-env -n <preview namespace>
```

The first answers `yes`; the second lists both copies.

### Use

In the service chart, reference the copies by name: the pull Secret on the pod, the app Secret on the container.

```yaml
spec:
  imagePullSecrets:
    - name: registry-pull
  containers:
    - name: app
      envFrom:
        - secretRef:
            name: app-env
```

A service chart built on badil's [`app`](../../charts/app) chart can set the app Secret with `app.envFrom`, but has no `imagePullSecrets` value yet: for private images it needs node-level registry credentials, or a chart that renders its own pod spec.

### Remove

```sh
kubectl delete -f preview-secrets.yaml
```

This keeps the copies in every preview: deleting a policy retains its clones. To delete them too, delete the sources first, while the policy is applied, which removes the synchronized copies; otherwise they go with each preview namespace at teardown.
