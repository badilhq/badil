{{/*
Resource name.
*/}}
{{- define "badil.fullname" -}}
{{- .Release.Name -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "badil.labels" -}}
app.kubernetes.io/name: badil
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: badil-{{ .Chart.Version }}
{{- end -}}

{{/*
Selector labels.
*/}}
{{- define "badil.selectorLabels" -}}
app.kubernetes.io/name: badil
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Container image.
*/}}
{{- define "badil.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{/*
Server environment.
*/}}
{{- define "badil.env" -}}
{{- $secret := required "existingSecret is required" .Values.existingSecret -}}
- name: GITHUB_APP_ID
  value: {{ required "config.githubAppId is required" .Values.config.githubAppId | int64 | quote }}
- name: GITHUB_INSTALLATION_ID
  value: {{ required "config.githubInstallationId is required" .Values.config.githubInstallationId | int64 | quote }}
- name: GITOPS_REPO
  value: {{ required "config.gitopsRepo is required" .Values.config.gitopsRepo | quote }}
- name: LISTEN_ADDR
  value: ":{{ .Values.service.port }}"
- name: GITHUB_PRIVATE_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: github-private-key
- name: GITHUB_WEBHOOK_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: github-webhook-secret
- name: BADIL_DEPLOY_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: deploy-token
{{- end -}}

{{/*
Pod securityContext.
*/}}
{{- define "badil.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{/*
Container securityContext.
*/}}
{{- define "badil.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop:
    - ALL
{{- end -}}
