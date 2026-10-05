# Read the archive from the replica

## Purpose

Keep verifying and querying the trail when the primary bucket's region is unavailable.

## Preconditions

- Cross-region replication to a bucket in another region and account, with Object Lock
  and replicated retention metadata ([prepare the bucket](prepare-the-bucket.md)).
- Read access to the replica.

## Before you start

- **Reads only.** The writer keeps writing to the primary, and a writer that cannot
  reach it withholds acknowledgements until it can.
- **Verification against the replica is as good as against the primary:** the seals
  were replicated with the objects they cover.

## Steps

1. Point `audit verify` and the query service's `archive.bucket` at the replica.

   ```
   audit verify --profile <p> --last 24h --bucket <replica> --prefix <prefix>
   ```

   Expected: the same report as against the primary. Verify: `audit conformance`
   against the query service. Roll back: point them back at the primary.

## Afterwards

When the primary returns, point readers back and confirm replication caught up
(`audit verify` over the outage window).
