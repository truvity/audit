{{/*
The refusals.

Every one of these is a configuration the binaries would reject at start-up, or
accept and then do something silently wrong with. A chart that renders a
manifest its own image will not run is worse than an error here: the failure
moves from `helm install` to a CrashLoopBackOff somebody has to read logs to
understand, or — worse — to a trail that looks fine and is not.
*/}}
{{- define "audit.checks" -}}

{{- if not .Values.bucket -}}
{{- fail "audit: set `bucket`. There is nowhere to write the archive." -}}
{{- end -}}

{{- if not .Values.profiles -}}
{{- fail "audit: set `profiles`. A writer with no profile keeps nothing, and every record it took would be dead-lettered." -}}
{{- end -}}

{{- if gt (int .Values.replicas) 1 -}}
  {{- if not (include "audit.hasDatabase" .) -}}
  {{- fail "audit: `replicas` above 1 needs `database`. Deduplication in one process only absorbs a repeat on the replica that saw the original, so a redelivery landing on another would be written twice. The writer refuses to start this way." -}}
  {{- end -}}
  {{- if eq .Values.keys.provider "local" -}}
    {{- if not (has "ReadWriteMany" .Values.keys.local.persistence.accessModes) -}}
    {{- fail "audit: `replicas` above 1 with the local key provider needs `keys.local.persistence.accessModes` to include ReadWriteMany. Data keys are random, not derived from the root, so replicas that cannot see one directory mint different keys for the same tenant and the same person gets a different pseudonym on each." -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{- if eq .Values.keys.provider "local" -}}
  {{- if not .Values.keys.local.existingSecret -}}
  {{- fail "audit: set `keys.local.existingSecret` to a Secret holding the 32-byte root. Without it the writer will not start, and a root generated per install would make the pseudonyms of two installs incomparable." -}}
  {{- end -}}
  {{- if and (not .Values.keys.local.persistence.enabled) (not .Values.keys.local.ephemeralIsAcceptable) -}}
  {{- fail "audit: `keys.local.persistence.enabled` is false. Data keys are random and wrapped into that directory, so losing it re-keys every tenant: the same person gets a new pseudonym and the trail stops linking across the restart. Set `keys.local.ephemeralIsAcceptable: true` if this install is disposable." -}}
  {{- end -}}
{{- else if eq .Values.keys.provider "transit" -}}
  {{- if not .Values.keys.transit.address -}}
  {{- fail "audit: set `keys.transit.address` to the OpenBAO server the keys live in." -}}
  {{- end -}}
  {{- if eq (len (compact (list .Values.keys.transit.token.existingSecret .Values.keys.transit.tokenFile))) 0 -}}
  {{- fail "audit: the transit key provider needs a token: `keys.transit.token.existingSecret`, or `keys.transit.tokenFile` where an agent writes one." -}}
  {{- end -}}
  {{- if and .Values.keys.transit.token.existingSecret .Values.keys.transit.tokenFile -}}
  {{- fail "audit: give the transit token one way, `keys.transit.token.existingSecret` or `keys.transit.tokenFile`, not both." -}}
  {{- end -}}
{{- else -}}
{{- fail (printf "audit: key provider %q is not one this chart knows: `local` or `transit`." .Values.keys.provider) -}}
{{- end -}}

{{- if .Values.jobs.digest.enabled -}}
  {{- $signers := 0 -}}
  {{- if .Values.jobs.digest.signingKey.existingSecret }}{{ $signers = add1 $signers }}{{ end -}}
  {{- if .Values.jobs.digest.kmsKey }}{{ $signers = add1 $signers }}{{ end -}}
  {{- if .Values.jobs.digest.transit.key }}{{ $signers = add1 $signers }}{{ end -}}
  {{- if eq $signers 0 -}}
  {{- fail "audit: `jobs.digest.signingKey.existingSecret`, `jobs.digest.kmsKey` or `jobs.digest.transit.key` is required while `jobs.digest.enabled`. An unsigned chain proves nothing, so the command refuses without a key and the job would only ever fail." -}}
  {{- end -}}
  {{- if gt $signers 1 -}}
  {{- fail "audit: more than one of `jobs.digest.signingKey.existingSecret`, `jobs.digest.kmsKey` and `jobs.digest.transit.key` is set. One chain has one signer; pick one." -}}
  {{- end -}}
  {{- if and .Values.jobs.digest.transit.key (not (and .Values.jobs.digest.transit.address .Values.jobs.digest.transit.token.existingSecret)) -}}
  {{- fail "audit: `jobs.digest.transit.key` needs `jobs.digest.transit.address` and `jobs.digest.transit.token.existingSecret`: the job signs through the transit engine and has to reach it and be allowed to." -}}
  {{- end -}}
{{- end -}}

{{- if .Values.jobs.verify.enabled -}}
  {{- if not .Values.jobs.verify.publicKey.existingSecret -}}
  {{- fail "audit: `jobs.verify.publicKey.existingSecret` is required while `jobs.verify.enabled`. Verification takes the public half only, which is what lets an auditor run it without being able to sign anything." -}}
  {{- end -}}
{{- end -}}

{{- if .Values.registry.enabled -}}
  {{- if not (include "audit.hasDatabase" .) -}}
  {{- fail "audit: `registry.enabled` needs `database`. Registered catalogues live in the same database as the index and share its migration chain; there is nowhere else to put them." -}}
  {{- end -}}
  {{- if not (and .Values.workloadIdentity.issuers .Values.workloadIdentity.workloads) -}}
  {{- fail "audit: `registry.enabled` needs `workloadIdentity.issuers` and `workloadIdentity.workloads`. The registry decides whose catalogue a document is from the caller's verified service account, so with no mapping it would refuse every registration; and a registry that took the document's own word for whose catalogue it is would let any workload describe another's records." -}}
  {{- end -}}
{{- end -}}

{{- if .Values.query.enabled -}}
  {{- if not (has .Values.query.searcher (list "postgres" "s3scan")) -}}
  {{- fail (printf "audit: `query.searcher` is postgres or s3scan, not %q." .Values.query.searcher) -}}
  {{- end -}}
  {{- if eq .Values.query.searcher "postgres" -}}
    {{- if not (or .Values.query.database.url .Values.query.database.existingSecret) -}}
    {{- fail "audit: the postgres searcher needs `query.database`: the query service's own role, with select on the index and nothing else. Tenant row-level security binds only a role that does not own the tables; the writer's URL is the owner's." -}}
    {{- end -}}
    {{- if or (and .Values.query.database.existingSecret (eq .Values.query.database.existingSecret .Values.database.existingSecret)) (and .Values.query.database.url (eq .Values.query.database.url .Values.database.url)) -}}
    {{- fail "audit: `query.database` names the writer's database credentials. The writer owns the tables, and an owner bypasses the tenant policies, so every tenant's isolation would rest on the service alone. Give the query service a role that does not own them." -}}
    {{- end -}}
  {{- end -}}
  {{- if not .Values.query.grants.issuers -}}
  {{- fail "audit: `query.grants.issuers` is empty, so nobody could ever sign in and the query service refuses to start. Name the issuers whose tokens it trusts; see docs/guides/read.md#access." -}}
  {{- end -}}
  {{- if not (or .Values.workloadIdentity.issuers .Values.anonymousWrites) -}}
  {{- fail "audit: the query service records every read through the writer, which must be able to take it." -}}
  {{- end -}}
  {{- if .Values.query.resolve.enabled -}}
    {{- if eq .Values.keys.provider "local" -}}
      {{- if not (and .Values.keys.local.persistence.enabled (has "ReadWriteMany" .Values.keys.local.persistence.accessModes)) -}}
      {{- fail "audit: `query.resolve` with the local key provider mounts the writer's key directory, which needs `keys.local.persistence` with ReadWriteMany: the query service runs beside the writer, not in its place. The transit provider needs no shared volume." -}}
      {{- end -}}
    {{- else if eq .Values.keys.provider "transit" -}}
      {{- if eq (len (compact (list .Values.query.resolve.transit.token.existingSecret .Values.query.resolve.transit.tokenFile))) 0 -}}
      {{- fail "audit: `query.resolve` with transit needs its own token, `query.resolve.transit.token.existingSecret` or `.tokenFile`, whose policy grants decrypt on the purposes it resolves. Not the writer's: the writer seals and must not be able to open." -}}
      {{- end -}}
      {{- if and .Values.query.resolve.transit.token.existingSecret (eq .Values.query.resolve.transit.token.existingSecret .Values.keys.transit.token.existingSecret) -}}
      {{- fail "audit: `query.resolve.transit.token` is the writer's token. Resolving and writing are separate privileges: the writer's policy seals and must not open." -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{- if and .Values.workloadIdentity.issuers .Values.anonymousWrites -}}
{{- fail "audit: `anonymousWrites` and `workloadIdentity.issuers` are both set. The writer verifies callers or it does not; pick one, and only a trial install should pick anonymous." -}}
{{- end -}}

{{- if not (or .Values.workloadIdentity.issuers .Values.anonymousWrites) -}}
{{- fail "audit: set `workloadIdentity.issuers` so the writer can verify which workload publishes, or `anonymousWrites: true` for a trial install. Without either the writer refuses to start: records taken from anybody under nobody's name are not a trail." -}}
{{- end -}}

{{- range .Values.workloadIdentity.workloads -}}
  {{- if and (not .issuer) (gt (len $.Values.workloadIdentity.issuers) 1) -}}
  {{- fail (printf "audit: workload %s names no issuer, and more than one is trusted. Two clusters can both have that namespace and service account; say which one it is." .subject) -}}
  {{- end -}}
{{- end -}}

{{- if and .Values.jobs.purge.enabled (not (include "audit.hasDatabase" .)) -}}
{{- fail "audit: `jobs.purge.enabled` needs `database`. The purge prunes the index and the deduplication table; with neither there is nothing for it to do." -}}
{{- end -}}

{{- if and .Values.jobs.clockSync.enabled (not .Values.jobs.clockSync.ntp) -}}
{{- fail "audit: `jobs.clockSync.ntp` must name at least one time reference while `jobs.clockSync.enabled`. A clock check with nothing to check against would report that the clock was not checked, every night." -}}
{{- end -}}

{{- if and .Values.governance (not .Values.kmsKey) -}}
{{- fail "audit: `governance` is on. Governance mode lets a privileged role shorten a retention, which is the property the archive exists to deny. Turn it off, or say so deliberately in a values file of your own." -}}
{{- end -}}

{{- end -}}
