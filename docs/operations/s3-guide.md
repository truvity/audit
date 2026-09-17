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

Placing a hold is an operator action recorded as `audit.hold.placed`.
Releasing one requires the break-glass role and is recorded as
`audit.hold.released`. Presets say whether holds are recommended.

## What breaks verification

Moving or renaming objects. Copying objects to another bucket without the
digest prefix. Changing the KMS key without keeping the old one decryptable.
Re-uploading an object under the same key (a new version) is detectable
and reported.

## Break-glass reads

Auditors get a read-only role scoped to the profile prefixes and the digest
prefix. Their reads appear in the bucket's access log and, when made
through the query service, as `audit.get` and `audit.search` records.
