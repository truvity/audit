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

{{- /* telemetry.otlp: an http(s) URL naming the collector, and OTEL_* variables
only. The endpoint has a value of its own so that one place sets it, and a
secret reaches a pod through secretEnv, never through a value rendered into the
manifest. */ -}}
{{- $otlp := (.Values.telemetry | default dict).otlp | default dict -}}
{{- $endpoint := $otlp.endpoint | default "" -}}
{{- if and $endpoint (not (regexMatch "^https?://[^/?#[:space:]]+" $endpoint)) -}}
{{- fail (printf "audit: telemetry.otlp.endpoint must be an http(s) URL naming the collector or gateway, such as http://gateway.observability.svc:4318 (got %q)." $endpoint) -}}
{{- end -}}
{{- range $name, $_ := ($otlp.extraEnv | default dict) -}}
{{- if eq $name "OTEL_EXPORTER_OTLP_ENDPOINT" -}}
{{- fail "audit: telemetry.otlp.extraEnv must not carry OTEL_EXPORTER_OTLP_ENDPOINT: set telemetry.otlp.endpoint, which is where the chart takes it from." -}}
{{- end -}}
{{- if not (hasPrefix "OTEL_" $name) -}}
{{- fail (printf "audit: telemetry.otlp.extraEnv holds OpenTelemetry SDK variables only: %q does not start with OTEL_. A secret reaches a pod through secretEnv, and the rest through config." $name) -}}
{{- end -}}
{{- end -}}

{{- /* The writer takes up to 30s to stop and flush (audit-writer's shutdown
budget), after the front door's preStop sleep. A grace period that does not
outlast both has the kubelet kill the process mid-write. */ -}}
{{- if le (int .Values.terminationGracePeriodSeconds) (add 30 (int .Values.preStopSleepSeconds)) -}}
{{- fail (printf "audit: terminationGracePeriodSeconds (%d) must be longer than the writer's 30s shutdown budget plus preStopSleepSeconds (%d): the kubelet would kill it while it writes what it holds." (int .Values.terminationGracePeriodSeconds) (int .Values.preStopSleepSeconds)) -}}
{{- end -}}

{{- if not (has .Values.mode (list "direct" "stream")) -}}
{{- fail (printf "audit: `mode` is `direct` or `stream`, not %q." .Values.mode) -}}
{{- end -}}

{{- if not .Values.profiles -}}
{{- fail "audit: set `profiles`. A writer with no profile keeps nothing, and every record it took would be dead-lettered." -}}
{{- end -}}

{{- $on := .Values.writer.enabled -}}
{{- $writer := .Values.writer.config | default dict -}}
{{- $mode := dig "mode" "writer" $writer -}}
{{- $database := dig "database" nil $writer -}}
{{- $pods := ternary (int .Values.writer.consumers) (int .Values.replicas) (eq .Values.mode "stream") -}}

{{- if $on -}}
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
  {{- if not (or (dig "stream" nil $writer) (dig "consume" nil $writer)) -}}
  {{- fail "audit: `mode: stream` needs `writer.config.stream` or `writer.config.consume`: the consumers read the stream or the queue, and without one there is nothing for them to read." -}}
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

{{- if $on -}}
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


{{/* A receiver is the front door: it takes records from applications and
publishes them. It must not hold the identity that writes the archive, or a
compromised front door writes the archive directly. On AWS the identity is the
ServiceAccount (Pod Identity, IRSA), so the two must be different accounts. */}}
{{- if and (eq .Values.mode "stream") (eq (include "audit.receiverServiceAccountName" .) (include "audit.serviceAccountName" .)) -}}
{{- fail (printf "audit: the receiver and the writer run as the same ServiceAccount, %q. A receiver must not hold the archive's write identity: whatever cloud role is bound to that account (Pod Identity, IRSA) would let a compromised front door write the archive directly. Give the receiver its own: leave `receiver.serviceAccount.create` true with a `receiver.serviceAccount.name` that is not the writer's `serviceAccount`." (include "audit.serviceAccountName" .)) -}}
{{- end -}}

{{- end -}}

{{/* The writer runs somewhere else (the writer Lambda behind SQS): nothing in
this release hosts the write path, so nothing may be configured as if it did,
and everything that records has to reach the writer's queue (or an external
front door) rather than the Service this release would have rendered. */}}
{{- if not $on -}}
  {{- if ne .Values.mode "direct" -}}
  {{- fail "audit: `writer.enabled: false` with `mode: stream`. Stream mode renders a receiver and consumers, which are write path; with the writer elsewhere leave `mode` at `direct`." -}}
  {{- end -}}
  {{- if .Values.keysVolume.enabled -}}
  {{- fail "audit: `writer.enabled: false` and `keysVolume.enabled`: the key directory belongs to the writer, and there is none in this release. Use the transit provider for the query service's resolve." -}}
  {{- end -}}
  {{- if .Values.workloadIdentity.issuers -}}
  {{- fail "audit: `writer.enabled: false` and `workloadIdentity.issuers` is set: that document is read by the writer only, and there is none in this release. The external writer verifies callers on its own." -}}
  {{- end -}}
  {{- if .Values.extensions.billing.enabled -}}
  {{- fail "audit: `writer.enabled: false` and `extensions.billing.enabled`: the extension is part of the write path, which is not in this release." -}}
  {{- end -}}
  {{- /* Each recording component, with the identity it runs as. */ -}}
  {{- $recorders := list -}}
  {{- if .Values.query.enabled -}}{{- $recorders = append $recorders (dict "name" "query" "key" "query" "comp" .Values.query "sink" (dig "sink" nil (.Values.query.config | default dict)) "must" true) -}}{{- end -}}
  {{- range $job, $suffix := dict "notary" "notary" "verify" "verify" "clockSync" "clock-sync" -}}
    {{- $comp := index $.Values.jobs $job -}}
    {{- if $comp.enabled -}}{{- $recorders = append $recorders (dict "name" (printf "jobs.%s" $job) "comp" $comp "sink" (dig "sink" nil ($comp.config | default dict)) "must" false) -}}{{- end -}}
  {{- end -}}
  {{- range $recorders -}}
    {{- $sink := .sink -}}
    {{- if and .must (not $sink) -}}
    {{- fail (printf "audit: `%s.config.sink` is not set. The service records every read before answering, and with `writer.enabled: false` there is no in-cluster writer to record through: name the writer's queue, `sink: {sqs: {queueUrl: ..., region: ...}}`." .name) -}}
    {{- end -}}
    {{- if and $sink (not (dig "sqs" nil $sink)) -}}
      {{- $host := regexReplaceAll "^[a-z]+://([^/:?#]+).*$" (dig "url" "" $sink) "${1}" -}}
      {{- $fn := include "audit.fullname" $ -}}
      {{- if or (eq $host $fn) (and (hasPrefix (printf "%s." $fn) $host) (or (contains ".svc" $host) (eq $host (printf "%s.%s" $fn $.Release.Namespace)))) -}}
      {{- fail (printf "audit: `%s.config.sink.url` is %q, this release's own front door, and `writer.enabled: false` renders no writer or Service behind it. Point it at the writer's queue with `sink: {sqs: {queueUrl: ..., region: ...}}` (the pod's identity needs sqs:SendMessage on it), or at an external front door's URL." .name (dig "url" "" $sink)) -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
  {{- /* An account that is not created and not named is the release's own,
  which is the writer's and is not rendered either. */ -}}
  {{- $accounts := list (dict "name" "observe" "on" .Values.observe.enabled "comp" .Values.observe) (dict "name" "query" "on" .Values.query.enabled "comp" .Values.query) -}}
  {{- range $job := (list "notary" "verify" "purge" "clockSync") -}}
    {{- $accounts = append $accounts (dict "name" (printf "jobs.%s" $job) "on" (index $.Values.jobs $job).enabled "comp" (index $.Values.jobs $job)) -}}
  {{- end -}}
  {{- range $accounts -}}
    {{- if and .on (not .comp.serviceAccount.create) (not .comp.serviceAccount.name) -}}
    {{- fail (printf "audit: `%s.serviceAccount.create` is false and no `name` is given, so it would run as the release's own ServiceAccount, which is the writer's and is not rendered with `writer.enabled: false`. Leave `create` true (and bind the cloud role in `annotations`) or name an existing account." .name) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{/* Separation of duties. Whoever writes the archive and can also sign for it
can choose what to sign, so the notary signs in as itself, never as the writer;
and whoever writes it and can also open what it sealed can read what the writer
must not, so the query service's resolve does too. Only the chart sees both
configurations. */}}
{{- $writerBao := dig "transit" "openbao" nil (dig "keys" nil $writer | default dict) -}}
{{- $writerWho := include "audit.baoIdentity" (dict "bao" $writerBao "env" .Values.writer.secretEnv) -}}
{{- if .Values.jobs.notary.enabled -}}
  {{- $notary := .Values.jobs.notary.config | default dict -}}
  {{- if eq (include "audit.notaryServiceAccountName" .) (include "audit.serviceAccountName" .) -}}
  {{- fail (printf "audit: the notary runs as the writer's ServiceAccount, %q. Whoever writes the archive and can also sign for it can choose what to sign; give the notary its own: leave `jobs.notary.serviceAccount.create` true, or name an account that is not the writer's." (include "audit.serviceAccountName" .)) -}}
  {{- end -}}
  {{- $notaryRole := .Values.jobs.notary.serviceAccount.annotations | default dict -}}
  {{- if and $notaryRole (eq (toJson $notaryRole) (toJson (.Values.serviceAccount.annotations | default dict))) -}}
  {{- fail "audit: the notary's ServiceAccount carries the writer's annotations, so it would bind the writer's cloud role (Pod Identity, IRSA). Whoever writes the archive and can also sign for it can choose what to sign; bind the notary to a role of its own." -}}
  {{- end -}}
  {{- $notaryWho := include "audit.baoIdentity" (dict "bao" (dig "signer" "transit" "openbao" nil $notary) "env" .Values.jobs.notary.secretEnv) -}}
  {{- if and $notaryWho $writerWho (eq $notaryWho $writerWho) -}}
  {{- fail "audit: the notary signs in to OpenBAO as the writer. Whoever writes the archive and can also sign for it can choose what to sign; give the notary its own role." -}}
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

{{/* The indexer holds the index's write credential. It is a part of its own,
and the separation has to be real at both places an identity lives: the
ServiceAccount, which on AWS is the cloud identity (Pod Identity, IRSA), and
the database role. */}}
{{- if .Values.observe.enabled -}}
  {{- $observeSA := include "audit.observeServiceAccountName" . -}}
  {{- if eq $observeSA (include "audit.serviceAccountName" .) -}}
  {{- fail (printf "audit: the indexer and the writer run as the same ServiceAccount, %q. The writer writes the archive and the indexer reads it and writes the index: whatever cloud role is bound to that account (Pod Identity, IRSA) would give each the other's rights. Give the indexer its own: leave `observe.serviceAccount.create` true with an `observe.serviceAccount.name` that is not the writer's `serviceAccount`." $observeSA) -}}
  {{- end -}}
  {{- if and .Values.query.enabled (eq $observeSA (include "audit.queryServiceAccountName" .)) -}}
  {{- fail (printf "audit: the indexer and the query service run as the same ServiceAccount, %q. The indexer writes the index and the query service faces callers and may only read it; one identity for both would put the index's write credential in the process that parses what callers send." $observeSA) -}}
  {{- end -}}
  {{- if and (eq .Values.mode "stream") (eq $observeSA (include "audit.receiverServiceAccountName" .)) -}}
  {{- fail (printf "audit: the indexer and the receiver run as the same ServiceAccount, %q. The receiver is the front door and holds nothing of the archive or the index." $observeSA) -}}
  {{- end -}}

  {{- $observe := .Values.observe.config | default dict -}}
  {{- $observeDatabase := dig "database" nil $observe -}}
  {{- $observeUser := include "audit.databaseUser" (dig "url" "" ($observeDatabase | default dict)) -}}
  {{- if and $observeDatabase $database (or (eq (dig "url" "" $observeDatabase) (dig "url" "" $database)) (eq $observeUser (include "audit.databaseUser" (dig "url" "" $database)))) -}}
  {{- fail "audit: the indexer's `database.url` connects as the writer's role. The writer's role has the deduplication table and the registry and none of the index, and the indexer's the index and none of those: one role for both is the separation in name only. Give the indexer a role of its own, named in `migrate.config.observe`." -}}
  {{- end -}}
  {{- if and $observeDatabase .Values.query.enabled .Values.query.config -}}
    {{- $queryDatabase := dig "database" nil .Values.query.config -}}
    {{- if and $queryDatabase (eq $observeUser (include "audit.databaseUser" (dig "url" "" $queryDatabase))) -}}
    {{- fail "audit: the indexer's `database.url` connects as the query service's role. The query service reads the index as a role that can write nothing, bound by row-level security; the indexer writes it. They must not share a role." -}}
    {{- end -}}
  {{- end -}}
  {{- if and $observeDatabase .Values.migrate.enabled -}}
    {{- $owner := dig "database" nil (.Values.migrate.config | default dict) -}}
    {{- if and $owner (eq $observeUser (include "audit.databaseUser" (dig "url" "" $owner))) -}}
    {{- fail "audit: the indexer's `database.url` connects as the migration's role, which owns the tables. An owner is bound by no grant and no row-level security, so the indexer would hold more than the index. Give it the role `migrate.config.observe` names." -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{/* The owner of the tables is the migration's, and nobody else's: the writer
that connected as it would hold the index it must not touch. */}}
{{- if and .Values.migrate.enabled $database -}}
  {{- $owner := dig "database" nil (.Values.migrate.config | default dict) -}}
  {{- if and $owner (eq (include "audit.databaseUser" (dig "url" "" $owner)) (include "audit.databaseUser" (dig "url" "" $database))) -}}
  {{- fail "audit: the writer's `database.url` connects as the migration's role, which owns the tables. An owner is bound by no grant, so the writer would hold the whole index, which it must not write and the indexer does. Give the writer the role `migrate.config.writer` names, and the migration an owner of its own." -}}
  {{- end -}}
{{- end -}}

{{- end -}}
