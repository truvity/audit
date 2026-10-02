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

{{/* An image: the digest-pinned one packaging wrote into `images`, else the
one `image` names, at the chart's appVersion unless given a tag. Takes (dict
"root" $ "name" <key in images> "image" <the matching entry of image>). */}}
{{- define "audit.image" -}}
{{- $pinned := index .root.Values.images .name | default dict -}}
{{- if $pinned.digest -}}
{{ with $pinned.registry }}{{ . }}/{{ end }}{{ $pinned.repository }}:{{ $pinned.tag }}@{{ $pinned.digest }}
{{- else -}}
{{ .image.repository }}:{{ .image.tag | default .root.Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "audit.writerImage" -}}
{{ include "audit.image" (dict "root" . "name" "audit-writer" "image" .Values.image.writer) }}
{{- end -}}

{{- define "audit.queryImage" -}}
{{ include "audit.image" (dict "root" . "name" "audit-query" "image" .Values.image.query) }}
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
{{ include "audit.image" (dict "root" . "name" "audit" "image" .Values.image.cli) }}
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

{{/* The trust bundle: a CA the configs name by path, mounted on every pod. */}}
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


{{/* The pods that answer the sink: the writer in direct mode, the receiver in
stream mode. Their configuration is the front door's. */}}
{{- define "audit.frontName" -}}
{{- if eq .Values.mode "stream" -}}receiver{{- else -}}writer{{- end -}}
{{- end -}}

{{/* The container port a component listens on, read from its own
configuration, because that is where the address is chosen: the chart derives
the port from the file rather than asking for it twice. Takes the component's
`config`. */}}
{{- define "audit.port" -}}
{{- $cfg := . | default dict -}}
{{- regexFind "[0-9]+$" (dig "listen" "address" ":8080" $cfg) -}}
{{- end -}}

{{/* A component's configuration is a ConfigMap of its own, rendered as it
stands: toYaml of the `config` block and nothing else. Takes (dict "root" $
"name" "writer" "config" <the block>). */}}
{{- define "audit.configName" -}}
{{ include "audit.fullname" .root }}-{{ .name }}-config
{{- end -}}

{{- define "audit.configMap" -}}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "audit.configName" . }}
  labels:
    {{- include "audit.labels" .root | nindent 4 }}
    app.kubernetes.io/component: {{ .name }}
  {{- with .hook }}
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-10"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
  {{- end }}
data:
  # What the binary reads with --config: validated against
  # schemas/config/ at start-up, and by this chart's values.schema.json before
  # it renders.
  config.yaml: |
    {{- toYaml .config | nindent 4 }}
{{- end -}}

{{- define "audit.configMount" -}}
- name: config
  mountPath: /etc/audit/config.yaml
  subPath: config.yaml
  readOnly: true
{{- end -}}

{{- define "audit.configVolume" -}}
- name: config
  configMap:
    name: {{ include "audit.configName" . }}
{{- end -}}

{{/* What a component takes from the platform beyond its configuration. Each
takes the component's values: `secretEnv` puts a Secret's key in the variable
the config names, `secretMounts` mounts a Secret as a directory, and `tokens`
projects a service-account token. */}}
{{- define "audit.secretEnv" -}}
{{- range .secretEnv }}
- name: {{ .name }}
  valueFrom:
    secretKeyRef:
      name: {{ .secretName }}
      key: {{ .key }}
      {{- if .optional }}
      optional: true
      {{- end }}
{{- end }}
{{- end -}}

{{- define "audit.extraMounts" -}}
{{- range $i, $m := .secretMounts }}
- name: secret-{{ $i }}
  mountPath: {{ $m.mountPath }}
  readOnly: true
{{- end }}
{{- range $i, $t := .tokens }}
- name: token-{{ $i }}
  mountPath: {{ $t.mountPath }}
  readOnly: true
{{- end }}
{{- end -}}

{{- define "audit.extraVolumes" -}}
{{- range $i, $m := .secretMounts }}
- name: secret-{{ $i }}
  secret:
    secretName: {{ $m.secretName }}
{{- end }}
{{- range $i, $t := .tokens }}
- name: token-{{ $i }}
  projected:
    sources:
      - serviceAccountToken:
          path: {{ $t.path | default "token" }}
          audience: {{ $t.audience | quote }}
          expirationSeconds: {{ $t.expirationSeconds | default 3600 }}
{{- end }}
{{- end -}}

{{/* The profile document, which every component that reads profiles mounts. */}}
{{- define "audit.deploymentMount" -}}
- name: deployment
  mountPath: /etc/audit/deployment.yaml
  subPath: deployment.yaml
  readOnly: true
{{- end -}}

{{- define "audit.deploymentVolume" -}}
- name: deployment
  configMap:
    name: {{ include "audit.fullname" . }}-deployment
{{- end -}}

{{/* The claim the local key directory lives in. */}}
{{- define "audit.keysClaim" -}}
{{ .Values.keysVolume.existingClaim | default (printf "%s-keys" (include "audit.fullname" .)) }}
{{- end -}}

{{/* Who an OpenBAO client signs in as: its role on the JWT mount, the file its
token is read from, or the Secret the variable it names comes from. Takes
(dict "bao" <the openbao block> "env" <the component's secretEnv>). Empty when
there is none to compare. */}}
{{- define "audit.baoIdentity" -}}
{{- $bao := .bao | default dict -}}
{{- if dig "login" "role" "" $bao -}}
role:{{ dig "login" "mount" "" $bao }}/{{ dig "login" "role" "" $bao }}
{{- else if dig "tokenFile" "" $bao -}}
file:{{ dig "tokenFile" "" $bao }}
{{- else if dig "tokenEnv" "" $bao -}}
{{- $name := dig "tokenEnv" "" $bao -}}
{{- range .env -}}{{- if eq .name $name -}}secret:{{ .secretName }}/{{ .key }}{{- end -}}{{- end -}}
{{- end -}}
{{- end -}}

{{/* The role a database URL connects as. */}}
{{- define "audit.databaseUser" -}}
{{- regexReplaceAll "^[a-z]+://([^:@/]*).*$" (. | default "") "${1}" -}}
{{- end -}}
