# Investigate a failed verification

## Purpose

Work out what a failing `audit verify` found, and what to record and fix.

## Preconditions

- The report of the run and the log it checked.
- Read access to the project's bucket and, for versions, the right to list object versions.

## Before you start

- **Treat it as an incident.** Nothing in a log can be repaired, and that is its point. Record what was found and when.
- **Do not write to the log's prefix.** Not even to "fix" a name.
- **Verification checks one log.** A problem in another project's bucket is not yours, and the two are verified separately.
- **Keep the evidence.** If the findings may be needed, place a legal hold first ([Place and release a legal hold](legal-hold.md)).

## Steps

1. **Read each finding.** The closed reason is the suffix of the file's path.

   | Reason | What to look for |
   |---|---|
   | `.name` | A file under `checkpoints/` that is not a size, or a directory. Something other than the writer put it there. |
   | `.length` | A checkpoint over 1 MiB. The same. |
   | `.signature` | A note that the log key did not sign, or a key that rotated. Check whether the log key changed on a declared compromise, and which checkpoint size starts the new key. If it did not, this is the most serious finding: only the key can sign. |
   | `.format` | A text that is not a checkpoint |
   | `.size` | The size in the text is not the size in the name |
   | `.origin` | A checkpoint for another log, copied in |
   | `.fork` | Two checkpoints of one size with different roots. A writer, or someone with its key, signed two histories. Treat as a compromise. |
   | `.inconsistent` | The root of a checkpoint is not the hash of the entries below it: a tile was changed, removed or added. With a compliance lock this means a version was written under the same key when the lock was off or in governance mode. List the versions of the object. |
   | A cosignature finding (planned) | A witness did not cosign, or cosigned a different root. A different root is a split view: ask the witness for the checkpoints it holds. |

   Verify: you can say which rule failed on which object. Roll back: none. This reads.

2. **Find who could have changed it.** Read the bucket's access log and the version history of the object. Compare the credentials that reach the bucket with the ones [0013](../decisions/0013-access.md) says there should be: the project's writer, the witness for its cosignatures, and the sweeper for expired objects.

3. **Compare with another copy.** If a witness, a replica or another auditor holds a checkpoint of the same log, compare its root with yours at the same size. A mismatch is a split view or a rewrite.

4. **Fix what let it happen.** A role with a delete right, a bucket without the guard, a credential that was not scoped, a lock that was off. Rotate the log key if it may be compromised: the verifier then needs both keys with their sizes.

## Afterwards

Run `audit verify` again over the log and record the incident where your installation keeps incidents. Tell the owners of the tenants whose records the finding covers. Nothing in the log is edited. A later checkpoint cosigned by a witness shows that the log grew consistently from a size you trust.
