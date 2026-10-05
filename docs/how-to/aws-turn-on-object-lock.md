# Turn the Object Lock on, and move to COMPLIANCE

## Purpose

Take an installation from no lock (`NONE`) to `GOVERNANCE`, and from there to
`COMPLIANCE` as a new installation, in the order the retention decision
([0023](../decisions/0023-archive-retention-and-lifecycle.md)) asks for.

## Preconditions

- A stack with `Archive.ObjectLockMode` set (it is required; there is no default).
- For `COMPLIANCE`: sign-off of the retentions, the keys and the lifecycle, and
  `AcknowledgeCompliance: true` in the new stack.

## Before you start

- **The switch to Object Lock is one way.** AWS allows it on an existing
  versioned bucket and it can never be disabled again. Setting `ObjectLockMode`
  back to `NONE` removes the resource from the program, but S3 will not turn
  the lock off, so do not.
- **Objects written before stay unlocked.** They can be locked by hand with
  `PutObjectRetention` or an S3 Batch Operations job.
- **`COMPLIANCE` is a new bucket, not an edit.** An existing object keeps the
  mode it was written with whatever the bucket's rule says later, and nobody,
  including the account's root, can shorten or remove a retention until each
  object's date. A retention wrong in the long direction is paid for until it
  expires.
- **A profile that demands a stricter mode than the bucket's** makes the writer
  refuse to start, and the first rollout is where that shows
  ([0014](../decisions/0014-lock-modes-and-store-tiers.md)).
- **A replace of the bucket in the preview means something other than the mode
  was edited: stop.**

## Steps

### Turning the lock on: NONE to GOVERNANCE

AWS allows Object Lock to be enabled on an existing bucket that has versioning,
and **it can never be disabled again**. The library never sets the bucket's own
`objectLockEnabled` (changing it forces the provider to replace the bucket);
Object Lock is a separate resource, so the switch is one edit and the bucket is
not replaced.

1. Change `ObjectLockMode` from `auditpulumi.None` to `auditpulumi.Governance`
   (and set `DefaultRetentionDays` if there is to be a floor).
2. `pulumi preview`. It should show: one resource created, the Object Lock
   configuration (`aws:s3/bucketObjectLockConfiguration`); the two functions
   updated in place (their `archive.lockMode` is now `governance`); the two role
   policies updated in place (the retention and legal-hold grants appear). The
   bucket, its versioning and the keys show no change, and nothing is replaced
   or deleted. A replace of the bucket means something other than the mode was
   edited: stop.
3. `pulumi up`. From then on the writer writes each object with the retention
   its profile demands. Objects written before stay unlocked; they can be locked
   by hand with `PutObjectRetention` or an S3 Batch Operations job if that is
   wanted.

The switch to Object Lock is one-way. Setting `ObjectLockMode` back to `NONE`
removes the resource from the program, but S3 will not turn the lock off, so do
not.

### GOVERNANCE to COMPLIANCE

COMPLIANCE is a **new bucket**, not an edit of the governance one: an existing
object keeps the mode it was written with whatever the bucket's rule says later,
and the step cannot be undone.

The way across:

1. Run the installation with GOVERNANCE for as long as it takes to see the
   retentions, the keys and the lifecycle behave, and sign the retentions off.
2. Create a second installation (a new component name, and a new `BucketName`)
   with `ObjectLockMode: auditpulumi.Compliance` **and**
   `AcknowledgeCompliance: true`.
3. Point the receivers and observe at the new queue and bucket. The trial bucket
   is emptied, or kept until its own retentions lapse; the trial doubles the
   storage for its length.

The lock mode reaches both functions' configuration (`archive.lockMode`), so the
writer writes objects in the mode the bucket was built for, and refuses to start
when a profile's frameworks demand a stricter one
([0014](../decisions/0014-lock-modes-and-store-tiers.md)).

## Afterwards

Check that the writer writes each object with the retention its profile
demands (`audit verify` reports a lock that ends sooner than the profile
requires). The modes themselves are explained in
[AWS Lambda](../explanation/aws-lambda.md#the-lock-modes).
