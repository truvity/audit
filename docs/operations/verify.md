# Verification

```
audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
  --bucket <name> --prefix audit/<application> --public-key digest-signing.pem [--json]
```

A date, an hour (`2026-09-17T10`) or a full timestamp are all accepted. The
command needs the archive and the public key and nothing else: run it with
read-only credentials and your own copy of the binary, because an answer that
depended on the operator of the archive would not be worth having.

**One command, every installation.** Each installation writes one prefix of
the environment's bucket, and its chain covers that prefix. Verifying an
application means naming its `--prefix`; no other application's records are
read, and none is needed. The two deployment shapes make no difference here
either: direct and stream write the same objects, under the same keys, sealed
by the same chain, so an auditor need not know which one produced them.

Walks the digest chain newest-first, then for each digest:

1. Verifies the signature against the public key.
2. Confirms the previous digest exists and its hash and signature match
   what this digest names.
3. Fetches each listed object, computes its SHA-256, compares.
4. Reads each object's lock, when given the deployment: an object with no
   retention is `unlocked` under a profile that demands no lock, and
   `INVALID` under one that does
   ([0014](../decisions/0014-lock-modes-and-store-tiers.md)).
5. Confirms that no object in the range is unaccounted for. This is the half a
   chain alone does not cover: the chain shows that what it names is unaltered,
   and this shows that nothing was added beside it.

Output: one line per digest and per object, `valid`, `unlocked` or
`INVALID: <reason>`, and a summary. Exit code non-zero on any invalid entry;
`unlocked` is information and does not count.

Auditors run it with read-only credentials scoped to the installation's
prefix. The nightly run inside the cluster does the same for the previous day
and records the outcome as `audit.digest.verified` or `audit.digest.failed`.

| flag | what |
|---|---|
| `--prefix <p>` | the installation's prefix in the bucket, as the chart's `prefix` names it; omit only where the installation is at the bucket's root |
| `--last 24h` | the windows of the last this long, ending at the hour that has closed; instead of `--from` and `--to` |
| `--lookback 168h` | how far before the range to look for objects keyed under an older day; at least what `audit digest` used |
| `--sink <writer>` | record what was checked through the writer. The scheduled job does; an auditor's run by hand should not |
| `--record` | also write one verification per window under `verified/`, which `Get` reports as a record's `verified_at`; needs write access there |
| `--deployment <file>` | the profile configuration, so the check knows what lock the profile demands. The scheduled job passes it; an auditor without it still gets the chain checked, with nothing said about locks |
| `--lock-mode none` | for `--record` on a store with no lock, so the verification is written without a lock header; the scheduled job takes it from the chart's `lockMode` |
| `--endpoint`, `--path-style` | an S3-compatible store that is not AWS, as the [S3 guide](s3-guide.md#s3-compatible-stores) has it |

`--profile` is required, and a profile is verified on its own: an
installation composing two profiles is two runs, because each profile has its
own chain and its own retention to check against.

After a signing key change, keep every public half: each digest names the key
it was signed with (`signed_by`), and a digest checked with the wrong half
fails its signature.

What a failed verification means, and what to do about it, is in
[the runbook](runbook.md).
