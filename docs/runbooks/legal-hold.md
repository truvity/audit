# Place and release a legal hold

## Purpose

Keep objects of a log undeletable for as long as a matter lasts, whatever their retention says.

## Preconditions

- The store has a hold flag. AWS S3 with Object Lock does (`s3:PutObjectLegalHold`). A store with no hold flag, and a local file system, do not ([Without a hold flag](#without-a-hold-flag)).
- An operator role that may place a hold. Releasing needs the break-glass role. The bucket policy refuses `s3:PutObjectLegalHold` with `OFF` to everyone else ([0010](../decisions/0010-lock-modes-retention-and-compliance-floors.md)).

## Before you start

- **audit places no hold and lifts none.** A hold is placed with the store's own means, under cloud IAM or a break-glass role.
- **A reason is required.** A hold nobody can account for cannot be released safely, because whoever finds it later cannot know whether the matter is over.
- **A hold covers objects, not prefixes.** S3 holds a version of an object. New objects that the writer creates after you place the hold are not held. Repeat the sweep (step 2) until the matter ends, or until the logs involved are sealed and the range you care about is complete.
- **List holds before you delete a tenant's `evidence` wrap.** The deletion checks for a standing hold on the logs that hold the tenant's entries ([0011](../decisions/0011-evidence-encryption-and-pseudonyms.md)). This is the reason the hold exists in the model.
- **The record of the hold is yours to make,** under your own name. **Open:** the emit command for it. Until it exists, record the hold in the incident or matter record, with its time and reason, and emit the fact through the product's own emitter.

## Steps

1. **List what holds exist.** Use the store's listing of objects with a legal hold in the prefix.

   ```
   aws s3api list-object-versions --bucket BUCKET --prefix audit/LOG/ --query 'Versions[].[Key,VersionId]' --output text
   aws s3api get-object-legal-hold --bucket BUCKET --key KEY --version-id VERSION
   ```

   Verify: you know which holds exist before you add one.

2. **Place the hold** on the objects of the log range involved: the entry bundles, the hash tiles, `checkpoints/` and the `checkpoint` object.

   ```
   aws s3api put-object-legal-hold --bucket BUCKET --key KEY --version-id VERSION --legal-hold Status=ON
   ```

   Expected: each object answers without error. Verify: `get-object-legal-hold` says `ON`. Roll back: release it (step 3).

3. **Release the hold** (break-glass only), when the matter ends.

   ```
   aws s3api put-object-legal-hold --bucket BUCKET --key KEY --version-id VERSION --legal-hold Status=OFF
   ```

   Expected: `OFF`. A refused attempt by anyone else is still a fact to record. Roll back: place the hold again.

## Without a hold flag

On a store with no hold flag, such as a local file system or a store with only a bucket lock rule, suspend the expiry for the held range by hand: stop the sweeper or lifecycle rule for the log's prefix, and write down the range and the reason. A removable lock can be removed by an account administrator, so on such a store the witnesses and a copy outside the store are the protection.

## Afterwards

Tell the owner of the matter which objects and which range are held. A hold is not part of the log, and the log's checkpoints do not say it exists.
