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
{{- else -}}
{{- fail (printf "audit: key provider %q is not built yet. Only `local` is." .Values.keys.provider) -}}
{{- end -}}

{{- if .Values.jobs.digest.enabled -}}
  {{- if not .Values.jobs.digest.signingKey.existingSecret -}}
  {{- fail "audit: `jobs.digest.signingKey.existingSecret` is required while `jobs.digest.enabled`. An unsigned chain proves nothing, so the command refuses without a key and the job would only ever fail." -}}
  {{- end -}}
{{- end -}}

{{- if .Values.jobs.verify.enabled -}}
  {{- if not .Values.jobs.verify.publicKey.existingSecret -}}
  {{- fail "audit: `jobs.verify.publicKey.existingSecret` is required while `jobs.verify.enabled`. Verification takes the public half only, which is what lets an auditor run it without being able to sign anything." -}}
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
