# Verify the trail

## Purpose

Check, from the archive alone, that each record object is the object that was written, that each
record is the record that was hashed, and (with the seals) that nothing was removed or added
beside them.

## Preconditions

- Read-only credentials scoped to the installation's prefix, and your own copy of the `audit`
  binary: an answer that depended on the operator of the archive would not be worth having.
- The bucket and the installation's `--prefix` (its `archive.prefix`).
- For the seals: the **thumbprints** of the notary's root keys, given to you out of band
  (`audit key public --thumbprint` on the operator's side). Nothing in the bucket, `keys/roots.jwks`
  included, can add one.

## Before you start

- **A profile is verified on its own.** An installation composing two profiles is two runs.
- **Without `--root` it checks bytes and hashes only.** It cannot show that an object was removed or
  one added beside the others: that is what seals vouch for ([0019](../decisions/0019-seals.md)).
- **A quiet hour has a seal too**, so a missing seal is a fault and never silence; but a seal is only
  due once its settle window (`--settle`, default 10m) and grace (`--grace`, default 1h) have passed.
- **An archive written before the v1 layout is not read** by this command: use the previous
  release's CLI (v0.6.x).
- **`unlocked` is information, not a failure,** on a profile that demands no lock; under one that
  does, an object with no lock is `INVALID`. Pass `--deployment` for the check to know.
- **No `--sink` on a run by hand:** it records what was checked through the writer, which the
  scheduled job does and an auditor's run should not.

## Steps

1. **Check records.**

   ```
   audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
     --bucket <name> --prefix audit/<application> [--json]
   ```

   Expected: one line per object, `valid`, `unlocked` or `INVALID: <reason>`, and a summary of the
   objects and records checked; exit code non-zero on any invalid entry. Verify: the summary's counts
   match the range you asked for. Roll back: none; this reads.

2. **Check the seals as well.**

   ```
   audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
     --bucket <name> --prefix audit/<application> \
     --root <thumbprint>[,<thumbprint>...]
   ```

   Expected: the same, plus a line per seal. Verify: no `seal.*` finding. A finding is an incident:
   [investigate a failed verification](investigate-a-failed-verification.md). Roll back: none.

3. **For a store that is not AWS** add `--endpoint` and `--path-style`
   ([prepare the bucket](prepare-the-bucket.md#s3-compatible-stores)).

## Afterwards

- The nightly job inside the cluster (`audit verify --config`) does the same for the previous day and
  records `audit.seal.verified` or `audit.seal.failed` per ingest hour; a run that checked no seal
  records none, because a record claiming it had would be false assurance.
- What each check is and every flag: [`audit verify` reference](../reference/verify-command.md).
