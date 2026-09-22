{{/*
audit.deployment renders one of the write path's Deployments.

An installation has one in direct mode and two in stream mode, from the same
image and the same configuration, differing in what they are trusted with:

  writer    direct mode: the front door and the write path in one process. It
            serves the sink, holds the archive's credentials and the keys, and
            puts every batch it takes before it answers.
  receiver  stream mode: the front door alone. It serves the sink and
            publishes to the stream, and holds neither the bucket nor a key,
            so a compromised front door cannot reach the archive.
  consumer  stream mode: the write path alone. It reads the stream, gathers,
            puts and indexes. Nothing calls it, so it has no Service.

Takes a dict: root (the chart context) and role.
*/}}
{{- define "audit.deployment" -}}
{{- $ := .root -}}
{{- $role := .role -}}
{{- $front := ne $role "consumer" -}}
{{- $archive := ne $role "receiver" -}}
{{- $keysClaim := $.Values.keys.local.persistence.existingClaim | default (printf "%s-keys" (include "audit.fullname" $)) -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ if $front }}{{ include "audit.fullname" $ }}{{ else }}{{ include "audit.fullname" $ }}-consumer{{ end }}
  labels:
    {{- include "audit.labels" $ | nindent 4 }}
spec:
  replicas: {{ if $front }}{{ $.Values.replicas }}{{ else }}{{ $.Values.writer.consumers }}{{ end }}
  {{- if and $archive (eq $.Values.keys.provider "local") $.Values.keys.local.persistence.enabled (not (has "ReadWriteMany" $.Values.keys.local.persistence.accessModes)) }}
  # One writer at a time may hold a ReadWriteOnce volume, so the old pod goes
  # before the new one arrives. A rolling update would deadlock on the claim.
  strategy:
    type: Recreate
  {{- end }}
  selector:
    matchLabels:
      {{- if $front }}
      {{- include "audit.writerSelectorLabels" $ | nindent 6 }}
      {{- else }}
      {{- include "audit.consumerSelectorLabels" $ | nindent 6 }}
      {{- end }}
  template:
    metadata:
      labels:
        {{- if $front }}
        {{- include "audit.writerSelectorLabels" $ | nindent 8 }}
        {{- else }}
        {{- include "audit.consumerSelectorLabels" $ | nindent 8 }}
        {{- end }}
      annotations:
        # A profile change must reach a running writer, which only happens if
        # the pod is replaced.
        checksum/deployment: {{ toYaml $.Values.profiles | sha256sum }}
        # Likewise who may write: a changed issuer or mapping must reach it.
        checksum/workloads: {{ toYaml $.Values.workloadIdentity | sha256sum }}
        {{- with $.Values.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ include "audit.serviceAccountName" $ }}
      {{- include "audit.podDefaults" $ | nindent 6 }}
      containers:
        - name: {{ $role }}
          image: {{ include "audit.writerImage" $ }}
          imagePullPolicy: {{ $.Values.image.pullPolicy }}
          args:
            - --deployment=/etc/audit/deployment.yaml
            - --listen=:{{ $.Values.service.port }}
            {{- if eq $role "receiver" }}
            - --mode=receiver
            {{- else }}
            - --roll-interval={{ $.Values.roll.interval }}
            - --roll-max-records={{ $.Values.roll.maxRecords }}
            - --replicas={{ if $front }}{{ $.Values.replicas }}{{ else }}{{ $.Values.writer.consumers }}{{ end }}
            {{- end }}
            {{- if include "audit.verifiesWorkloads" $ }}
            - --workloads=/etc/audit/workloads.yaml
            {{- else if $.Values.anonymousWrites }}
            - --anonymous-writes
            {{- end }}
            {{- if $archive }}
            {{- if $.Values.kmsKey }}
            - --kms-key={{ $.Values.kmsKey }}
            {{- end }}
            {{- if $.Values.governance }}
            - --governance
            {{- end }}
            {{- end }}
            {{- if $.Values.catalogues }}
            - --catalogues=/etc/audit/catalogues
            {{- end }}
            {{- if $archive }}
            - --key-provider={{ $.Values.keys.provider }}
            {{- if eq $.Values.keys.provider "local" }}
            - --key-root=/etc/audit/keys/{{ $.Values.keys.local.secretKey }}
            - --key-dir=/var/lib/audit/keys
            {{- else if eq $.Values.keys.provider "transit" }}
            - --transit-prefix={{ $.Values.keys.transit.prefix }}
            {{- include "audit.openbaoArgs" (dict "root" $ "creds" $.Values.keys.transit) | nindent 12 }}
            {{- end }}
            {{- end }}
            {{- if $.Values.stream.url }}
            - --stream-url={{ $.Values.stream.url }}
            - --stream={{ $.Values.stream.name }}
            - --consumer={{ $.Values.stream.consumer }}
            - --stream-batch={{ $.Values.stream.batch }}
            - --stream-ack-wait={{ $.Values.stream.ackWait }}
            {{- end }}
            - --version={{ $.Chart.AppVersion }}
          env:
            {{- if $archive }}
            {{- include "audit.archiveEnv" $ | nindent 12 }}
            {{- end }}
            {{- include "audit.databaseEnv" $ | nindent 12 }}
            {{- include "audit.trustEnv" $ | nindent 12 }}
            {{- with $.Values.telemetry.otlpEndpoint }}
            - name: OTEL_EXPORTER_OTLP_ENDPOINT
              value: {{ . | quote }}
            {{- end }}
          ports:
            - name: http
              containerPort: {{ $.Values.service.port }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
          securityContext:
            {{- toYaml $.Values.securityContext | nindent 12 }}
          resources:
            {{- toYaml $.Values.resources | nindent 12 }}
          volumeMounts:
            - name: deployment
              mountPath: /etc/audit/deployment.yaml
              subPath: deployment.yaml
              readOnly: true
            {{- if include "audit.verifiesWorkloads" $ }}{{ include "audit.workloadsMount" $ | nindent 12 }}{{- end }}
            {{- if $.Values.catalogues }}
            - name: catalogues
              mountPath: /etc/audit/catalogues
              readOnly: true
            {{- end }}
            {{- if $archive }}
            {{- if eq $.Values.keys.provider "local" }}
            - name: key-root
              mountPath: /etc/audit/keys
              readOnly: true
            - name: keys
              mountPath: /var/lib/audit/keys
            {{- else if eq $.Values.keys.provider "transit" }}
            {{- include "audit.openbaoMount" (dict "root" $ "creds" $.Values.keys.transit) | nindent 12 }}
            {{- end }}
            {{- end }}
            {{- include "audit.trustMount" $ | nindent 12 }}
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: deployment
          configMap:
            name: {{ include "audit.fullname" $ }}-deployment
        {{- if $.Values.catalogues }}
        - name: catalogues
          configMap:
            name: {{ include "audit.fullname" $ }}-catalogues
        {{- end }}
        {{- if $archive }}
        {{- if eq $.Values.keys.provider "local" }}
        - name: key-root
          secret:
            secretName: {{ $.Values.keys.local.existingSecret }}
        - name: keys
          {{- if $.Values.keys.local.persistence.enabled }}
          persistentVolumeClaim:
            claimName: {{ $keysClaim }}
          {{- else }}
          # Acknowledged as disposable: pseudonyms change on every restart.
          emptyDir: {}
          {{- end }}
        {{- else if eq $.Values.keys.provider "transit" }}
        {{- include "audit.openbaoVolume" (dict "root" $ "creds" $.Values.keys.transit) | nindent 8 }}
        {{- end }}
        {{- end }}
        {{- include "audit.trustVolume" $ | nindent 8 }}
        - name: tmp
          emptyDir: {}
{{- end -}}
