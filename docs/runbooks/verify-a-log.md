# Verify a log

## Purpose

Check, from the log store alone, that a log is intact: its checkpoints are signed by the log key, its history only grew, its tree matches the tiles, and, where witnesses are configured, a witness cosigned the newest checkpoint.

## Preconditions

- Read-only credentials on the project's bucket, scoped to the log's prefix, and **your own copy of `audit`**. An answer that depended on the operator of the store would not be worth having.
- The **verifier key** of the log, given to you out of band: a C2SP signed-note verifier key of type 0x02. Nothing in the bucket can add a key to your verifier.
- The log's **origin line**: `<host>/<scope>/<project>/<kind>/<log>`.
- For the cosignatures (planned): the pinned witness keys.

## Before you start

- **A log is verified on its own.** A project has an `audit` log and a `usage` log for each writer instance, and a cluster has one log of each kind per node. Each is a run.
- **Verification needs no key to read records.** It does not open them. A log verifies after its records stop opening.
- **A rotated log key needs its history.** If the log key rotated on compromise, you need each key with the checkpoint size from which it applies (planned: several keys in one run).
- **The newest batch is covered by the writer alone until a witness cosigns it.** The window is about one seal, 5 seconds.
- **`unobserved` is information.** A seal entry that says its clock checks did not run is not a failure. It tells you the time checks were off in that batch.

## Steps

1. **Check the history and the tree.**

   ```
   audit verify --key-file LOG_KEY --origin ORIGIN LOG_DIRECTORY
   ```

   Expected: `ok:` with the origin, the number of entries, the root hash and the number of checkpoints. The exit code is zero. Verify: the number of entries is what you expect for the range, and the root equals the root of the newest checkpoint you can see in the bucket. Roll back: none. This only reads.

2. **Check the cosignatures** (planned).

   ```
   audit verify --key-file LOG_KEY --witness WITNESS_KEY --origin ORIGIN LOG_DIRECTORY
   ```

   Expected: the number of cosignatures that the log's `AuditWitnesses` requires on the newest checkpoint. A missing cosignature on the newest checkpoint is not a failure for the first seconds. A missing cosignature on an older checkpoint is a finding.

3. **If any check fails,** stop and follow [Investigate a failed verification](investigate-a-failed-verification.md). Do not repair anything.

## Afterwards

- A scheduled run does the same for each log, and records the result as a security record. A run that checked no checkpoint records nothing, because a record that said it had would be false assurance.
- What each check is, and every closed reason: [`audit verify`](../reference/audit-verify.md) and [log layout](../reference/log-layout.md).
