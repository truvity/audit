# 0008. Hourly signed digest chain and an auditor-run verify command

- Status: accepted
- Date: 2026-09-17

## Context

Object Lock proves nothing was deleted. It does not prove nothing was
omitted or that an object was written when claimed. Frameworks expect
hash chains, signatures or time-stamps, and auditors ask three questions:
was it changed, when was it written, and can you prove it without trusting
the writer. The cloud provider's own trail uses hourly digest files with a
signature chain and a validation command; transparency logs (Merkle trees
with inclusion and consistency proofs) are the stronger model but a real
service to run ([research/storage.md](../research/storage.md)).

## Decision

A **digest job** runs hourly per profile prefix. It lists the hour's
objects, records each object's SHA-256, names the previous digest and the
previous digest's signature, signs the digest with an asymmetric key from a
pluggable `Signer` (KMS or a secret manager's transit engine), and writes
it under a separate digest prefix with the same Object Lock retention.
Digests are written for empty hours too, so absence is provable.

A `verify` command walks the chain newest-first over a time range, checks
each digest's signature against the published public key, each object's
hash, and each object's Object Lock retention, and prints per-object and
per-digest results and a summary. Auditors run it themselves against
read-only credentials. A nightly run marks verified windows so the viewer
can show an integrity badge, and a failure emits `audit.digest.failed`
with block delivery.

**What the chain proves depends on who holds the signing key.** With a key
the writer holds itself, the chain proves that objects have not changed since
they were signed, by a party who could also have chosen what to sign. That is
enough for a test and for a small deployment that accepts it, and it is what
the local signer is for. A managed key, in KMS or a secret manager's transit
engine, never leaves its provider, so the archive's writer cannot sign at will
and the chain proves what an auditor wants it to: the operator did not choose
what to sign. A deployment that must answer an assessor uses a managed key,
and the verifier is the same either way.

Time-stamp anchoring of the chain head with an RFC 3161 or ETSI time-stamp
is a preset option, required by none of the shipped presets and
recommended by the evidence preset.

## Consequences

- The digest job is independent of the number of writers.
- Moving objects breaks verification by design; the S3 guide says so.
- A public transparency log or Merkle proofs remain an upgrade path if a
  customer contract asks for third-party witnessing.

## Alternatives considered

- **Hash-chained rows in a database.** Detects edits but lives in a
  mutable store and needs the same external anchor.
- **A transparency log.** Strongest, but a service with its own storage
  and witnesses; not justified at this scale.
- **Signing every record.** Expensive and still needs a chain to prove
  omission.
