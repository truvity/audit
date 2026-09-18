# S3 guide

What the bucket must look like for the record to be a record. The writer
sets per-object retention; the bucket must allow and protect it.

## Bucket

- Versioning enabled. Object Lock enabled at creation with a **default
  retention in compliance mode** equal to the shortest profile's
  retention; the writer sets longer per object.
- SSE-KMS with a customer-managed key whose policy allows the writer to
  encrypt and the query service, digest job and verify role to decrypt,
  and nobody to schedule deletion of the key without a break-glass role.
- Public access blocked. Bucket policy denies `s3:DeleteObject`,
  `s3:DeleteObjectVersion`, `s3:PutBucketObjectLockConfiguration` changes
  that shorten, and `s3:BypassGovernanceRetention` to everyone; denies
  `s3:PutObjectLegalHold` removal except to the break-glass role with MFA.
- Server access logging or data-event trail enabled on the bucket, so
  direct reads are logged even when they bypass the query service.
- Cross-region replication to a bucket in another region and account with
  Object Lock, with replication of retention metadata.

## Prefixes and retention

| prefix | retention |
|---|---|
| `profile=<p>/tenant=*/...` | the profile's, set per object at PUT |
| `payload/` | the longest referencing profile |
| `schema/` | the longest profile any action in the catalogue belongs to |
| `digest/profile=<p>/...` | the profile's |
| `dlq/` | the longest profile |

Lifecycle: transition to an infrequent-access tier after the hot window;
never to deep archive for objects under a few megabytes; expiration only
after lock expiry, which S3 enforces anyway.

Write one rule per profile, filtered on `profile=<name>/`. This is why the
profile is the leading component of every key: a lifecycle filter matches a
literal prefix and takes no wildcards, so a rule per profile is possible only
in that order. A role scoped to one customer is unaffected, because a policy's
resource may carry a wildcard: `arn:aws:s3:::<bucket>/*/tenant=<id>/*`.

## Legal hold

```
audit hold place --profile <p> [--tenant <t>] --reason <why> --by <who> --bucket <b> --sink <writer>
audit hold list [--profile <p>] --bucket <b>
audit hold release --id <id> --by <who> --bucket <b> --sink <writer>
```

A hold keeps objects undeletable for as long as it is on, whatever their
retention says, and it has no expiry of its own. Placing one is an operator
action recorded as `audit.hold.placed`; releasing one requires the break-glass
role, which the archive's own policy enforces, and is recorded as
`audit.hold.released` — including when the archive refuses it, so that nobody
holding the role can try quietly. Presets say whether holds are recommended.

Both events are declared `block`, so `place` and `release` refuse to run
without `--sink`, and wait for the writer to confirm the record. The record is
made after the hold changes, not before, so the trail never claims a hold that
then failed; if the writer cannot take it, the command fails with an error
saying the hold **is** placed (or released) and must be recorded by hand. The
hold's own record under `holds/` in the archive is there either way.

A hold is placed on a prefix and the archive holds objects, so it has two
halves. `place` sweeps what is already there. The writer sets the hold on
objects it writes afterwards, re-reading the active holds every minute: an
object held only by a later sweep was deletable in between, and that window is
the whole thing a hold is for. The writer reads the holds once before it writes
anything and refuses to start if it cannot; after that, a refresh that fails
keeps the last answer, because forgetting a hold is worse than acting on a list
a minute old. The writer's role therefore needs `s3:PutObjectLegalHold` — to
set a hold on, never off — and read access to `holds/`.

The record of a hold lives in the archive under the same lock as everything
else, and is append-only like everything else: `holds/<id>/placed.json`, and
`holds/<id>/released.json` when it comes off. A reason is required — a hold
nobody can account for cannot be safely released, because whoever finds it
later has no way to know whether the matter is over.

**Before erasing a tenant's keys, list the holds.** Crypto-shredding a tenant
whose copies are under legal hold destroys evidence that may not be destroyed.

## What breaks verification

Moving or renaming objects. Copying objects to another bucket without the
digest prefix. Changing the KMS key without keeping the old one decryptable.
Re-uploading an object under the same key (a new version) is detectable
and reported.

## Break-glass reads

Auditors get a read-only role scoped to the profile prefixes and the digest
prefix. Their reads appear in the bucket's access log and, when made
through the query service, as `audit.get` and `audit.search` records.
