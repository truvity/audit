{{/*
The refusals.

What a component's `config` says is checked twice before anything runs: by
values.schema.json, which embeds the schema each binary validates its file
against, and by the binary at start-up. What is left here is what only the
platform side can see: whether the shape the values ask the chart to render
agrees with what the configurations say. Each is a configuration the binaries
would accept and then do something silently wrong with — or a manifest they
would reject — and a chart that renders one moves the failure from
`helm install` to a CrashLoopBackOff somebody has to read logs to understand,
or to a trail that looks fine and is not.
*/}}
{{- define "audit.checks" -}}

{{- if not (has .Values.mode (list "direct" "stream")) -}}
{{- fail (printf "audit: `mode` is `direct` or `stream`, not %q." .Values.mode) -}}
{{- end -}}

{{- if not .Values.profiles -}}
{{- fail "audit: set `profiles`. A writer with no profile keeps nothing, and every record it took would be dead-lettered." -}}
{{- end -}}

{{- $writer := .Values.writer.config | default dict -}}
{{- $mode := dig "mode" "writer" $writer -}}
{{- $database := dig "database" nil $writer -}}
{{- $pods := ternary (int .Values.writer.consumers) (int .Values.replicas) (eq .Values.mode "stream") -}}

{{- if eq .Values.mode "stream" -}}
  {{- if not .Values.receiver.config -}}
  {{- fail "audit: `mode: stream` needs `receiver.config`, with `mode: receiver`. The receiver serves the sink and publishes to the stream, and its configuration is its own." -}}
  {{- end -}}
  {{- if ne (dig "mode" "writer" .Values.receiver.config) "receiver" -}}
  {{- fail "audit: `receiver.config.mode` must be `receiver`. A receiver holds neither the archive nor a key, and that is what the mode says." -}}
  {{- end -}}
  {{- if eq $mode "receiver" -}}
  {{- fail "audit: `writer.config.mode` is `receiver` in stream mode. `writer.config` is the consumers' configuration; the receiver's is `receiver.config`." -}}
  {{- end -}}
  {{- if not (dig "stream" nil $writer) -}}
  {{- fail "audit: `mode: stream` needs `writer.config.stream`: the consumers read the stream, and without one there is nothing for them to read." -}}
  {{- end -}}
  {{- if not $database -}}
  {{- fail "audit: `mode: stream` needs `database` in `writer.config`. Several writers share one stream, and deduplication in one process only absorbs a repeat on the writer that saw the original; a redelivery landing on another would be written twice." -}}
  {{- end -}}
{{- else if eq $mode "receiver" -}}
{{- fail "audit: `writer.config.mode` is `receiver` in direct mode. A receiver only publishes to a stream; use `mode: stream`, or `writer.config.mode: writer`." -}}
{{- end -}}

{{/* The writers count themselves, to refuse to run several without the shared
state that makes it safe. The chart is what decides how many there are, so the
two numbers must say the same thing. */}}
{{- if ne (int (dig "replicas" 1 $writer)) $pods -}}
{{- fail (printf "audit: `writer.config.replicas` is %d and the chart renders %d writer pod(s) (`%s`). The writer refuses to run several without a database, and refuses in-memory keys with several; it can only do that if it is told the truth." (int (dig "replicas" 1 $writer)) $pods (ternary "writer.consumers" "replicas" (eq .Values.mode "stream"))) -}}
{{- end -}}

{{- if and (gt $pods 1) (not $database) -}}
{{- fail "audit: more than one writer pod needs `database` in `writer.config`. Deduplication in one process only absorbs a repeat on the replica that saw the original, so a redelivery landing on another would be written twice. The writer refuses to start this way." -}}
{{- end -}}

{{- if and .Values.keysVolume.enabled (gt $pods 1) (not (has "ReadWriteMany" .Values.keysVolume.accessModes)) -}}
{{- fail "audit: more than one writer pod with `keysVolume.accessModes` lacking ReadWriteMany. Data keys are random, not derived from the root, so replicas that cannot see one directory mint different keys for the same tenant and the same person gets a different pseudonym on each." -}}
{{- end -}}

{{- if and .Values.query.keysVolume (not .Values.keysVolume.enabled) -}}
{{- fail "audit: `query.keysVolume` mounts the writer's key directory, and `keysVolume.enabled` is false: there is no directory to mount." -}}
{{- end -}}
{{- if and .Values.query.keysVolume (not (has "ReadWriteMany" .Values.keysVolume.accessModes)) -}}
{{- fail "audit: `query.keysVolume` needs `keysVolume.accessModes` to include ReadWriteMany: the query service runs beside the writer, not in its place. The transit provider needs no shared volume." -}}
{{- end -}}

{{- if .Values.extensions.billing.enabled -}}
  {{- $metering := false -}}
  {{- range $name, $profile := .Values.profiles -}}
    {{- range $profile.presets -}}
      {{- if hasPrefix "billing" . -}}{{- $metering = true -}}{{- end -}}
    {{- end -}}
  {{- end -}}
  {{- if not $metering -}}
  {{- fail "audit: `extensions.billing.enabled` and no profile composes a metering preset. The statement is computed from the billing copy of each record, and without a profile that keeps one there is nothing to compute from." -}}
  {{- end -}}
{{- end -}}

{{- if and .Values.extensions.quotas.enabled (ne .Values.mode "stream") -}}
{{- fail "audit: `extensions.quotas.enabled` needs `mode: stream`. Quotas are counted by a second consumer of the same stream, and in direct mode there is no stream to consume." -}}
{{- end -}}

{{/* Who is calling the writer: the document and the config must agree. */}}
{{- $verifies := dig "workloads" "" $writer -}}
{{- if and $verifies (not .Values.workloadIdentity.issuers) -}}
{{- fail "audit: `writer.config.workloads` names the workloads file, and `workloadIdentity.issuers` is empty, so the chart renders none. Name the issuers whose tokens the writer trusts." -}}
{{- end -}}
{{- if and .Values.workloadIdentity.issuers (not $verifies) -}}
{{- fail "audit: `workloadIdentity.issuers` is set and `writer.config.workloads` is not, so nothing reads the file the chart renders. Set `workloads: /etc/audit/workloads.yaml` in the writer's config, or drop the issuers for a trial install with `anonymousWrites: true`." -}}
{{- end -}}
{{- if and .Values.workloadIdentity.issuers $database (not .Values.workloadIdentity.workloads) -}}
{{- fail "audit: map every workload that registers a catalogue in `workloadIdentity.workloads`. The writer serves RegisterCatalogue beside the sink, and takes whose catalogue a document is from the caller's verified service account and never from the document, so with no mapping every registration would be refused." -}}
{{- end -}}
{{- range .Values.workloadIdentity.workloads -}}
  {{- if and (not .issuer) (gt (len $.Values.workloadIdentity.issuers) 1) -}}
  {{- fail (printf "audit: workload %s names no issuer, and more than one is trusted. Two clusters can both have that namespace and service account; say which one it is." .subject) -}}
  {{- end -}}
{{- end -}}


{{/* Separation of duties. Whoever writes the archive and can also sign its
digests can choose what to sign, so the digest job and the query service's
resolve each sign in as themselves, never as the writer. Only the chart sees
both configurations. */}}
{{- $writerBao := dig "transit" "openbao" nil (dig "keys" nil $writer | default dict) -}}
{{- $writerWho := include "audit.baoIdentity" (dict "bao" $writerBao "env" .Values.writer.secretEnv) -}}
{{- $digest := .Values.jobs.digest.config | default dict -}}
{{- $digestSigner := dig "signer" nil $digest | default dict -}}
{{- if and .Values.jobs.digest.enabled $digestSigner -}}
  {{- $digestWho := include "audit.baoIdentity" (dict "bao" (dig "transit" "openbao" nil $digestSigner) "env" .Values.jobs.digest.secretEnv) -}}
  {{- if and $digestWho $writerWho (eq $digestWho $writerWho) -}}
  {{- fail "audit: the digest job signs in as the writer. Whoever writes the archive and can also sign its digests can choose what to sign; give the job its own role." -}}
  {{- end -}}
  {{- if and (or (hasKey $digestSigner "kmsKey") (hasKey $digestSigner "transit")) (not .Values.jobs.digest.serviceAccount.create) -}}
  {{- fail "audit: the digest job signs in as the writer: `jobs.digest.serviceAccount.create` is false, so it runs as the release's own service account. Whoever writes the archive and can also sign its digests can choose what to sign; give the job its own service account." -}}
  {{- end -}}
{{- end -}}
{{- if and .Values.query.enabled .Values.query.config -}}
  {{- $queryKeys := dig "keys" nil .Values.query.config | default dict -}}
  {{- $queryWho := include "audit.baoIdentity" (dict "bao" (dig "transit" "openbao" nil $queryKeys) "env" .Values.query.secretEnv) -}}
  {{- if and $queryWho $writerWho (eq $queryWho $writerWho) -}}
  {{- fail "audit: `query.config.keys` signs in as the writer. Resolving and writing are separate privileges: the writer's policy seals and must not open." -}}
  {{- end -}}
{{- end -}}

{{/* Keys held in a directory are the only copy: losing it re-keys every
tenant. */}}
{{- $writerKeys := dig "keys" nil $writer | default dict -}}
{{- if and (eq (dig "provider" "none" $writerKeys) "local") (not .Values.keysVolume.enabled) (not .Values.keysVolume.ephemeralIsAcceptable) -}}
{{- fail "audit: the local key provider with `keysVolume.enabled` false. Data keys are random and wrapped into that directory, so losing it re-keys every tenant: the same person gets a new pseudonym and the trail stops linking across the restart. Set `keysVolume.ephemeralIsAcceptable: true` if this install is disposable." -}}
{{- end -}}

{{- if .Values.query.enabled -}}
  {{- if not .Values.query.grants.issuers -}}
  {{- fail "audit: `query.grants.issuers` is empty, so nobody could ever sign in and the query service refuses to start. Name the issuers whose tokens it trusts; see docs/guides/read.md#access." -}}
  {{- end -}}
  {{- $query := .Values.query.config | default dict -}}
  {{- $queryDatabase := dig "database" nil $query -}}
  {{- $sameUser := and $queryDatabase $database (eq (include "audit.databaseUser" (dig "url" "" $queryDatabase)) (include "audit.databaseUser" (dig "url" "" $database))) -}}
  {{- if and $queryDatabase $database (or (eq (dig "url" "" $queryDatabase) (dig "url" "" $database)) $sameUser) -}}
  {{- fail "audit: the query service's `database.url` is the writer's. The writer owns the tables, and an owner bypasses the tenant policies, so every tenant's isolation would rest on the service alone. Give the query service a role that does not own them." -}}
  {{- end -}}
{{- end -}}

{{- end -}}
