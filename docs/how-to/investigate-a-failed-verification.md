# Investigate a failed verification

## Purpose

Work out what a failing `audit verify` (or the verify job) found, and what to
record and fix.

## Preconditions

- The report (`--json` gives it as data), the bucket and the profile and range it
  checked.
- Read access to the archive and, for versions, `s3:ListBucketVersions`.

## Before you start

- **Treat it as an incident.** Nothing in the archive can be repaired: that is its
  point. Record what was found and when.
- **`audit verify` checks what is in a range of ingest time.** It does not by itself
  find an object that was removed or one added beside the others; seals do
  ([0019](../decisions/0019-seals.md)), with `--root` or `seals.roots`.
- **The check covers one installation's prefix.** A problem under another
  application's prefix in a shared bucket is not yours, and the two are verified
  separately.
- **An archive written before the v1 layout** is not checked by this command; use
  the previous release's CLI (v0.6.x), which also walks its digest chain.

## Steps

1. **Read each finding.** One line each (`--json` for the same as data):

| the report says (rule: finding) | look for |
|---|---|
| `object.sha256`: the object's bytes do not match its `sha256` | an object changed since it was written: a new version under the same key (only possible if Object Lock was off or in governance mode when it was written; compare versions with `aws s3api list-object-versions`), or damage in storage or transit |
| `record.hash`: a record's hash does not match its record | a line altered inside an object, or an object not written by the writer |
| `key.grammar`, `object.metadata`, `record.placement` or `object.body`: the key, the metadata or a line is malformed, or a record is under another profile or tenant | an object put by something other than the writer, or a copy that lost its user metadata (`format`, `sha256`, `count`) |
| `object.count`: the object has a different number of records than its `count` | the same: the writer sets both |
| the lock ends sooner than the profile requires | an object written with a shorter retention than its profile asks: check the writer's version and the profile at that time (`schema/profile/<name>/`) |

   Seals add three findings: a `seal.root` finding is exactly an object added to, removed from or changed in
   an hour after it was sealed, with the seal's count and the hour's count side
   by side; `seal.missing` is an hour with no seal when it should have one (look
   for the notary having stopped, `AuditSealStale`, and failing that for a seal
   removed: the bucket's access log and versioning say who); `seal.signature` is a
   seal signed by a key the verifier does not pin or whose delegation has lapsed
   or was revoked, and is a failure to take seriously, because the only thing the
   bucket's writer cannot do is sign as a pinned root.

   Verify: you can say which rule failed on which object. Roll back: none; this reads.

2. **Preserve the evidence.** Place a legal hold on the affected prefix if it may be
   needed ([place a legal hold](place-a-legal-hold.md)).

3. **Fix what let it happen** (a role with a delete, a bucket without the policy,
   a copy that lost its metadata).

## Afterwards

Re-run `audit verify` over the range and record the incident where your
deployment keeps them.
