# Verification

```
audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
  --bucket <name> --prefix audit/<application> [--json]
```

A date, an hour (`2026-09-17T10`) or a full timestamp are all accepted, and
the range is of **ingest time**: the hours from the one `--from` is in up to,
but not including, the one `--to` is in. The command needs the archive and
nothing else: no public key, no database, no writer. Run it with read-only
credentials and your own copy of the binary, because an answer that depended
on the operator of the archive would not be worth having.

**One command, every installation.** Each installation writes one prefix of
the environment's bucket. Verifying an application means naming its
`--prefix`; no other application's records are read, and none is needed. The
two deployment shapes make no difference here either: direct and stream write
the same objects, under the same keys, so an auditor need not know which one
produced them.

For every record object under `records/<profile>/` whose ingest hour is in the
range, in key order, it checks, against the
[bucket contract](../reference/bucket-contract.md):

1. **The key.** It has the grammar the contract gives: profile, tenant, the
   ingest hour, a ULID.
2. **The metadata.** `format` is `1`, and `sha256` and `count` are present and
   well formed.
3. **The bytes.** The SHA-256 of the stored bytes is the `sha256` the object
   names.
4. **The records.** The object decompresses, has `count` lines, and each line's
   `hash` is the SHA-256 of the canonical encoding of its `record`.
5. **The lock**, when given the deployment: an object with no retention is
   `unlocked` under a profile that demands no lock, and `INVALID` under one
   that does ([0014](../decisions/0014-lock-modes-and-store-tiers.md)).

What this shows is that each object is the object that was written and each
record is the record that was hashed. What it cannot show is that nothing was
removed or added beside them: that is what seals vouch for
([0019](../decisions/0019-seals.md)), and they are not built yet.

Output: one line per object, `valid`, `unlocked` or `INVALID: <reason>`, and a
summary of the objects and records checked. Exit code non-zero on any invalid
entry; `unlocked` is information and does not count.

Auditors run it with read-only credentials scoped to the installation's
prefix. The nightly run inside the cluster (`audit verify --config`, whose
file has the same settings under their own names) does the same for the
previous day and records the outcome through the writer, per ingest hour, as
`audit.digest.verified` or `audit.digest.failed` (the target of each is
`records/<profile>/<yyyy>/<mm>/<dd>/<hh>`). Those two event names are kept
until seals replace them.

| flag | what |
|---|---|
| `--profile <p>` | required; the profile to check |
| `--from`, `--to` | the range of ingest time, `--to` not included |
| `--last 24h` | the objects ingested in the last this long, ending at the hour that has closed; instead of `--from` and `--to` |
| `--prefix <p>` | the installation's prefix in the bucket, as `archive.prefix` of the chart's configuration names it; omit only where the installation is at the bucket's root |
| `--sink <writer>` | record what was checked through the writer. The scheduled job does; an auditor's run by hand should not |
| `--deployment <file>` | the profile configuration, so the check knows what lock the profile demands. The scheduled job passes it; an auditor without it still gets keys, bytes and hashes checked, with nothing said about locks |
| `--json` | print the report as JSON |
| `--endpoint`, `--path-style` | an S3-compatible store that is not AWS, as the [S3 guide](s3-guide.md#s3-compatible-stores) has it |

`--profile` is required, and a profile is verified on its own: an
installation composing two profiles is two runs, because each profile has its
own retention to check against.

The scheduled job's configuration is `archive` (the bucket and prefix, and
nothing about a lock mode: it only reads), `deployment`, `last` (default
`24h`), optionally `profiles`, and `sink` with `require`. It has no key
settings.

**The old archive is not read.** An archive written before the v1 layout, with
`profile=<p>/tenant=<t>/year=…` keys and a signed digest chain under
`digest/`, is read by nothing in v1 and is not checked by this command. It
stays verifiable with the previous release's CLI (v0.6.x), whose `audit verify`
walks that digest chain with the public key.

What a failed verification means, and what to do about it, is in
[the runbook](runbook.md).
