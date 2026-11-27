# Namespace guard example

[`namespace-guard.yaml`](namespace-guard.yaml) holds a ValidatingAdmissionPolicy and a ValidatingAdmissionPolicyBinding, both cluster-scoped and named `badil-namespace-guard`. The policy refuses any `UPDATE` or `DELETE` of a Namespace by the ServiceAccount of the renderer that deploys previews unless the namespace already carries the label `badil.dev/preview`, with the message `namespace is not a badil preview`. The renderer then cannot adopt or delete a namespace badil did not create, whoever wrote the preview file. Requests from any other user, and the renderer's creation of a namespace, are not affected.

badil's own check stays: it refuses a new preview whose namespace already exists and is not a preview (see [Namespace in use](../../docs/EVENTS.md#namespace-in-use)), but only for the previews it writes itself. Why the guard closes the rest of the gap: [Namespace guard](../../docs/INSTALL.md#namespace-guard-optional).

## Prerequisites

- Kubernetes 1.30 or later.
- A renderer whose ServiceAccount manages no namespace other than previews: the policy covers every namespace that identity updates or deletes, not only previews.

## Adapt

The policy matches the renderer by its ServiceAccount, the username in its `preview-renderer` match condition. The example is Argo CD's application controller, `system:serviceaccount:argocd:argocd-application-controller`. For Argo CD in another namespace or under another ServiceAccount, or another renderer, set it to the ServiceAccount the renderer applies the preview manifests as: its controller pod's `serviceAccountName`, unless the renderer is configured to apply as another ServiceAccount. A wrong identity leaves the guard inactive without error. Read Argo CD's application controller ServiceAccount with:

```sh
kubectl get statefulset argocd-application-controller -n argocd -o jsonpath='{.spec.template.spec.serviceAccountName}'
```

## Apply

```sh
kubectl apply -f namespace-guard.yaml
```

## Check

```sh
kubectl get validatingadmissionpolicy,validatingadmissionpolicybinding badil-namespace-guard
```

It lists both. What a refused preview looks like: [Namespace guard](../../docs/OPERATIONS.md#namespace-guard).

## Remove

```sh
kubectl delete -f namespace-guard.yaml
```
