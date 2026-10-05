# Switch the archive's encryption from a KMS key to `aws-managed` or `s3`

## Purpose

Change `Archive.Encryption` on a running installation without making old
objects unreadable.

## Preconditions

- The installation runs with `Encryption: kms` (the default) and has objects.
- A principal of your own that can use the old archive key, for the copy.
- A decision recorded on why the AWS-managed key or SSE-S3 satisfies your key
  policy (for ISO 27001 A.8.24 the control asks for a documented policy, not a
  customer-managed key; see [the encryption table](../reference/aws-pulumi-library.md#encryption)).

## Before you start

- **S3 does not re-encrypt existing objects.** Changing the field changes only the
  bucket default for objects written from then on. Existing objects still need
  the old key to be read.
- **The roles lose their grant on the old key** with the change, so the writer
  and observe cannot read old objects until they are rewritten. The old
  archive key is protected and no longer managed by the stack: it must not be
  removed yet.
- **Versions keep their retention under the old key.** A copy makes a new version;
  the old versions, with their Object Lock retention, stay under the old key
  until they expire, so the old key must outlive the longest retention of the
  old versions.
- **`REPLACE` drops user metadata** unless it is restated, and S3 refuses a
  copy onto itself that changes nothing.

## Steps

1. Before applying, preview the change: it updates the bucket's default
   encryption and drops the roles' grants on the old key. Keep a principal of
   your own that can use the old key, for the copy.
2. Apply, then copy each object over itself so it is rewritten under the new
   default: S3 Batch Operations "Copy" for a large bucket, or `aws s3 cp
   s3://<bucket>/<prefix>/ s3://<bucket>/<prefix>/ --recursive --sse aws:kms
   --metadata-directive REPLACE` per prefix (S3 refuses a copy onto itself that
   changes nothing, and `REPLACE` drops user metadata unless it is restated).
   A copy makes a new version, and the old versions, with their Object Lock
   retention, stay under the old key until they expire, so the old key must
   outlive the longest retention of the old versions. Set the new versions'
   retention to match, so nothing is shortened.
3. Verify with `aws s3api head-object` that the current versions report
   `ServerSideEncryption: aws:kms` and no `SSEKMSKeyId` of the old key, then run
   `audit verify`.
4. Only then schedule the old key's deletion (a 30-day window), and only after
   every version that it encrypted has expired or been rewritten. Deleting it
   earlier makes those objects permanently unreadable.

## Afterwards

Keep a note of the date and the old key's alias in the installation's own
decision record: the old key's deletion window is the last step and cannot be
undone.
