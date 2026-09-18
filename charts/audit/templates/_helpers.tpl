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
