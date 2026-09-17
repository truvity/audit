# Integrity

## Digest chain

Hourly, per profile prefix, the digest job writes:

```json
{
  "digest_version": "1",
  "profile": "security",
  "window_start": "2026-09-17T13:00:00Z",
  "window_end":   "2026-09-17T14:00:00Z",
  "objects": [
    { "key": "tenant=.../...ndjson.zst", "sha256": "…", "size": 123456,
      "retain_until": "2027-09-17T14:00:00Z" }
  ],
  "previous_digest_key": "digest/profile=security/.../13.json",
  "previous_digest_sha256": "…",
  "previous_digest_signature": "…",
  "signed_by": "arn:…:key/… | transit/keys/audit-digest",
  "signature": "…"
}
```

Empty windows produce a digest with no objects. The digest object is locked
with the same retention as the objects it covers.

## Verify

`audit verify --profile <p> --from <t> --to <t> [--public-key <pem>]`
walks the chain newest-first and prints per digest and per object `valid`,
`INVALID: hash mismatch`, `INVALID: signature`, `INVALID: missing`,
`INVALID: retention shorter than profile`, and a summary. Exit code is
non-zero on any invalid entry. Auditors run it with read-only credentials.

## Nightly

The nightly run verifies the previous day, records `audit.digest.verified`
or `audit.digest.failed`, and marks the windows in the index so the viewer
shows a badge.

## Time

Emitters and writers run on synchronised clocks. A daily job checks the
offset against UTC and emits `audit.clock.synchronised`, which the evidence
and PCI presets require.

## Anchoring

Presets may require or recommend anchoring the chain head with an RFC 3161
or ETSI time-stamp. The digest carries an optional `anchor` field for the
token.
