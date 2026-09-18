{{- define "audit.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "audit.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "audit.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "audit.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "audit.selectorLabels" -}}
app.kubernetes.io/name: {{ include "audit.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "audit.writerImage" -}}
{{ .Values.image.writer.repository }}:{{ .Values.image.writer.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "audit.registryImage" -}}
{{ .Values.image.registry.repository }}:{{ .Values.image.registry.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "audit.queryImage" -}}
{{ .Values.image.query.repository }}:{{ .Values.image.query.tag | default .Chart.AppVersion }}
{{- end -}}

{{/* The writer's pods. Every component carries the release's labels, so the
writer names itself too: a selector of the release's labels alone would take
the registry's and the query service's pods into the writer's Service. */}}
{{- define "audit.writerSelectorLabels" -}}
{{ include "audit.selectorLabels" . }}
app.kubernetes.io/component: writer
{{- end -}}

{{- define "audit.cliImage" -}}
{{ .Values.image.cli.repository }}:{{ .Values.image.cli.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "audit.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "audit.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "audit.registryServiceAccountName" -}}
{{- if .Values.registry.serviceAccount.create -}}
{{- printf "%s-registry" (include "audit.fullname" .) -}}
{{- else -}}
{{- include "audit.serviceAccountName" . -}}
{{- end -}}
{{- end -}}

{{- define "audit.queryServiceAccountName" -}}
{{- if .Values.query.serviceAccount.create -}}
{{- printf "%s-query" (include "audit.fullname" .) -}}
{{- else -}}
{{- include "audit.serviceAccountName" . -}}
{{- end -}}
{{- end -}}

{{/* The Secret holding the query service's own database URL. */}}
{{- define "audit.queryDatabaseSecretName" -}}
{{- if .Values.query.database.existingSecret -}}
{{- .Values.query.database.existingSecret -}}
{{- else -}}
{{- printf "%s-query-database" (include "audit.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "audit.digestServiceAccountName" -}}
{{- if .Values.jobs.digest.serviceAccount.create -}}
{{- printf "%s-digest" (include "audit.fullname" .) -}}
{{- else -}}
{{- include "audit.serviceAccountName" . -}}
{{- end -}}
{{- end -}}

{{- define "audit.verifyServiceAccountName" -}}
{{- if .Values.jobs.verify.serviceAccount.create -}}
{{- printf "%s-verify" (include "audit.fullname" .) -}}
{{- else -}}
{{- include "audit.serviceAccountName" . -}}
{{- end -}}
{{- end -}}

{{/* The Secret holding the database URL, whether given inline or by name. */}}
{{- define "audit.databaseSecretName" -}}
{{- if .Values.database.existingSecret -}}
{{- .Values.database.existingSecret -}}
{{- else -}}
{{- printf "%s-database" (include "audit.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "audit.hasDatabase" -}}
{{- if or .Values.database.url .Values.database.existingSecret -}}true{{- end -}}
{{- end -}}

{{/* The environment every command shares: where the archive is. */}}
{{- define "audit.archiveEnv" -}}
- name: AUDIT_BUCKET
  value: {{ .Values.bucket | quote }}
{{- if .Values.prefix }}
- name: AUDIT_PREFIX
  value: {{ .Values.prefix | quote }}
{{- end }}
{{- if .Values.region }}
- name: AWS_REGION
  value: {{ .Values.region | quote }}
{{- end }}
{{- end -}}

{{- define "audit.databaseEnv" -}}
{{- if include "audit.hasDatabase" . }}
- name: AUDIT_DATABASE
  valueFrom:
    secretKeyRef:
      name: {{ include "audit.databaseSecretName" . }}
      key: {{ .Values.database.secretKey }}
{{- end }}
{{- end -}}

{{/* The writer's sink, which every job records itself through. */}}
{{- define "audit.sinkURL" -}}
http://{{ include "audit.fullname" . }}:{{ .Values.service.port }}
{{- end -}}

{{- define "audit.podDefaults" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
securityContext:
  {{- toYaml .Values.podSecurityContext | nindent 2 }}
{{- with .Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* Whether workloads are verified at all. */}}
{{- define "audit.verifiesWorkloads" -}}
{{- if .Values.workloadIdentity.issuers }}true{{ end -}}
{{- end -}}

{{/*
The projected service-account token a pod presents to the writer, read afresh
on every request because the kubelet replaces it before it expires. Rendered
only when the services verify workloads; an anonymous trial install has no use
for it.
*/}}
{{- define "audit.tokenEnv" -}}
- name: AUDIT_TOKEN_FILE
  value: /var/run/audit/token
{{- end -}}

{{- define "audit.tokenMount" -}}
- name: audit-token
  mountPath: /var/run/audit
  readOnly: true
{{- end -}}

{{- define "audit.tokenVolume" -}}
- name: audit-token
  projected:
    sources:
      - serviceAccountToken:
          path: token
          audience: {{ .Values.workloadIdentity.audience | quote }}
          expirationSeconds: {{ .Values.workloadIdentity.expirationSeconds }}
{{- end -}}

{{/* The workloads file mount, for the writer and the registry. */}}
{{- define "audit.workloadsMount" -}}
- name: deployment
  mountPath: /etc/audit/workloads.yaml
  subPath: workloads.yaml
  readOnly: true
{{- end -}}
