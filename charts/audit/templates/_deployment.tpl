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
{{- $comp := ternary $.Values.receiver $.Values.writer (eq $role "receiver") -}}
{{- $name := ternary "receiver" "writer" (eq $role "receiver") -}}
{{- $replicas := ternary $.Values.replicas $.Values.writer.consumers $front -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ if $front }}{{ include "audit.fullname" $ }}{{ else }}{{ include "audit.fullname" $ }}-consumer{{ end }}
  labels:
    {{- include "audit.labels" $ | nindent 4 }}
spec:
  replicas: {{ $replicas }}
  {{- if and $archive $.Values.keysVolume.enabled (not (has "ReadWriteMany" $.Values.keysVolume.accessModes)) }}
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
        # A changed configuration must reach a running pod, and a file mounted
        # with subPath is never updated in place, so the pod is replaced.
        checksum/config: {{ toYaml $comp.config | sha256sum }}
        # Likewise a profile change.
        checksum/deployment: {{ toYaml $.Values.profiles | sha256sum }}
        # And who may write: a changed issuer or mapping must reach it.
        checksum/workloads: {{ toYaml $.Values.workloadIdentity | sha256sum }}
        {{- with $.Values.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ ternary (include "audit.receiverServiceAccountName" $) (include "audit.serviceAccountName" $) (eq $role "receiver") }}
      {{- include "audit.podDefaults" $ | nindent 6 }}
      containers:
        - name: {{ $role }}
          image: {{ include "audit.writerImage" $ }}
          imagePullPolicy: {{ $.Values.image.pullPolicy }}
          args:
            - --config=/etc/audit/config.yaml
          {{- with include "audit.env" (dict "root" $ "comp" $comp "service" "audit-writer") }}
          env:
            {{- . | nindent 12 }}
          {{- end }}
          ports:
            - name: http
              containerPort: {{ include "audit.port" $comp.config }}
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
            {{- include "audit.configMount" $ | nindent 12 }}
            {{- include "audit.deploymentMount" $ | nindent 12 }}
            {{- if $.Values.workloadIdentity.issuers }}
            - name: deployment
              mountPath: /etc/audit/workloads.yaml
              subPath: workloads.yaml
              readOnly: true
            {{- end }}
            {{- if $.Values.catalogues }}
            - name: catalogues
              mountPath: /etc/audit/catalogues
              readOnly: true
            {{- end }}
            {{- if and $archive (or $.Values.keysVolume.enabled $.Values.keysVolume.ephemeralIsAcceptable) }}
            - name: keys
              mountPath: /var/lib/audit/keys
            {{- end }}
            {{- include "audit.trustMount" $ | nindent 12 }}
            {{- include "audit.extraMounts" $comp | nindent 12 }}
            - name: tmp
              mountPath: /tmp
      volumes:
        {{- include "audit.configVolume" (dict "root" $ "name" $name) | nindent 8 }}
        {{- include "audit.deploymentVolume" $ | nindent 8 }}
        {{- if $.Values.catalogues }}
        - name: catalogues
          configMap:
            name: {{ include "audit.fullname" $ }}-catalogues
        {{- end }}
        {{- if and $archive $.Values.keysVolume.enabled }}
        - name: keys
          persistentVolumeClaim:
            claimName: {{ include "audit.keysClaim" $ }}
        {{- else if and $archive $.Values.keysVolume.ephemeralIsAcceptable }}
        - name: keys
          # Acknowledged as disposable: pseudonyms change on every restart.
          emptyDir: {}
        {{- end }}
        {{- include "audit.trustVolume" $ | nindent 8 }}
        {{- include "audit.extraVolumes" $comp | nindent 8 }}
        - name: tmp
          emptyDir: {}
{{- end -}}
