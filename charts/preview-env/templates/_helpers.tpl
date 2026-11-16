{{- /*
The labels of every resource of a preview: name is the resource's app name,
instance the preview (the release name).
*/}}
{{- define "preview-env.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .instance }}
app.kubernetes.io/part-of: preview-environments
app.kubernetes.io/managed-by: argocd
{{- end }}

{{- /*
The finalizer of the AppProject and of every Application: Argo CD deletes
their resources with them.
*/}}
{{- define "preview-env.finalizers" -}}
finalizers:
  - resources-finalizer.argocd.argoproj.io
{{- end }}

{{- /*
The destination and sync policy of a service or dependency Application: the
preview namespace (the release name, passed as the context), synced
automatically.
*/}}
{{- define "preview-env.applicationSync" -}}
destination:
  server: https://kubernetes.default.svc
  namespace: {{ . }}
syncPolicy:
  automated:
    prune: true
    selfHeal: true
  syncOptions:
    - CreateNamespace=false
{{- end }}
