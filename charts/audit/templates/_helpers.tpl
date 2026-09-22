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

{{- define "audit.queryImage" -}}
{{ .Values.image.query.repository }}:{{ .Values.image.query.tag | default .Chart.AppVersion }}
{{- end -}}

{{/* The writer's pods. Every component carries the release's labels, so the
writer names itself too: a selector of the release's labels alone would take
the query service's and the jobs' pods into the writer's Service. */}}
{{- define "audit.writerSelectorLabels" -}}
{{ include "audit.selectorLabels" . }}
app.kubernetes.io/component: writer
{{- end -}}

{{/* The consumer's pods, in stream mode. Nothing calls them, so they are not
in any Service; the label is what a deployment greps for. */}}
{{- define "audit.consumerSelectorLabels" -}}
{{ include "audit.selectorLabels" . }}
app.kubernetes.io/component: consumer
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

{{/* The workloads file mount, for the writer. */}}
{{- define "audit.workloadsMount" -}}
- name: deployment
  mountPath: /etc/audit/workloads.yaml
  subPath: workloads.yaml
  readOnly: true
{{- end -}}

{{/*
How a component reaches OpenBAO: the shared connection, and the component's own
way of signing in — a role on the JWT auth mount with its projected token, a
token Secret, or a token file. Called with (dict "root" $ "creds" <values>).
*/}}
{{- define "audit.openbaoArgs" -}}
- --transit-address={{ .root.Values.openbao.address }}
- --transit-mount={{ .root.Values.openbao.mount }}
{{- with .root.Values.openbao.namespace }}
- --transit-namespace={{ . }}
{{- end }}
{{- if .root.Values.trust.configMap }}
- --transit-ca-file=/etc/audit/trust/{{ .root.Values.trust.key }}
{{- end }}
{{- if .creds.role }}
- --transit-auth-mount={{ .root.Values.openbao.auth.mount }}
- --transit-auth-role={{ .creds.role }}
- --transit-jwt-file=/var/run/openbao/token
{{- else if .creds.tokenFile }}
- --transit-token-file={{ .creds.tokenFile }}
{{- else }}
- --transit-token-file=/etc/audit/transit/{{ .creds.token.secretKey }}
{{- end }}
{{- end -}}

{{- define "audit.openbaoMount" -}}
{{- if .creds.role }}
- name: openbao-token
  mountPath: /var/run/openbao
  readOnly: true
{{- else if .creds.token.existingSecret }}
- name: transit-token
  mountPath: /etc/audit/transit
  readOnly: true
{{- end }}
{{- end -}}

{{- define "audit.openbaoVolume" -}}
{{- if .creds.role }}
- name: openbao-token
  projected:
    sources:
      - serviceAccountToken:
          path: token
          audience: {{ .root.Values.openbao.auth.audience | quote }}
          expirationSeconds: {{ .root.Values.openbao.auth.expirationSeconds }}
{{- else if .creds.token.existingSecret }}
- name: transit-token
  secret:
    secretName: {{ .creds.token.existingSecret }}
{{- end }}
{{- end -}}

{{/* Refuses a component's OpenBAO credentials unless there is exactly one way. */}}
{{- define "audit.checkOpenBAO" -}}
{{- if not .root.Values.openbao.address -}}
{{- fail (printf "audit: %s signs through OpenBAO: set `openbao.address`." .what) -}}
{{- end -}}
{{- $ways := len (compact (list .creds.role .creds.token.existingSecret .creds.tokenFile)) -}}
{{- if ne $ways 1 -}}
{{- fail (printf "audit: %s needs exactly one way to sign in to OpenBAO: `%s.role` (a JWT login with its projected token, the estate's way), `%s.token.existingSecret`, or `%s.tokenFile`." .what .at .at .at) -}}
{{- end -}}
{{- if and .creds.role (not .root.Values.openbao.auth.mount) -}}
{{- fail (printf "audit: `%s.role` signs in on `openbao.auth.mount`, which is not set." .at) -}}
{{- end -}}
{{- end -}}

{{/* The trust bundle, for OpenBAO and Postgres alike. */}}
{{- define "audit.trustMount" -}}
{{- if .Values.trust.configMap }}
- name: trust
  mountPath: /etc/audit/trust
  readOnly: true
{{- end }}
{{- end -}}

{{- define "audit.trustVolume" -}}
{{- if .Values.trust.configMap }}
- name: trust
  configMap:
    name: {{ .Values.trust.configMap }}
{{- end }}
{{- end -}}

{{- define "audit.trustEnv" -}}
{{- if .Values.trust.configMap }}
- name: PGSSLROOTCERT
  value: /etc/audit/trust/{{ .Values.trust.key }}
{{- end }}
{{- end -}}

{{/* Seconds from a Go duration the chart accepts: 30s, 2m, 1h. Helm has no
duration type, and comparing "2m" with "30s" as strings would pass silently. */}}
{{- define "audit.seconds" -}}
{{- $d := . | toString -}}
{{- if hasSuffix "h" $d -}}
{{- mul (trimSuffix "h" $d | float64 | int) 3600 -}}
{{- else if hasSuffix "ms" $d -}}
{{- div (trimSuffix "ms" $d | float64 | int) 1000 -}}
{{- else if hasSuffix "m" $d -}}
{{- mul (trimSuffix "m" $d | float64 | int) 60 -}}
{{- else -}}
{{- trimSuffix "s" $d | float64 | int -}}
{{- end -}}
{{- end -}}
