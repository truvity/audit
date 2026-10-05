# `audit verify`: what it checks

The command, the checks it makes on records and on seals, its flags and its output. The procedure
for an auditor or an operator is [verify the trail](../how-to/verify-the-trail.md).

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
[bucket contract](bucket-contract.md):

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
([0019](../decisions/0019-seals.md)), and `--root` checks them.

## Seals

```
audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
  --bucket <name> --prefix audit/<application> \
  --root <thumbprint>[,<thumbprint>...]
```

With `--root` the command also checks the seals of the range, for every tenant
of the profile, and the thumbprints are the **only** keys it trusts: the ones
you were given by the operator out of band (`audit key public --thumbprint` on
their side). Nothing in the bucket, `keys/roots.jwks` included, can add one.
For every hour in the range:

1. **A seal exists**, when the hour is due one: it has ended, the notary's
   settle window (`--settle`, default 10m) has passed and so has a grace for its
   hourly schedule (`--grace`, default 1h). A quiet hour has a seal too, so a
   missing seal is a fault and never silence, from the hour of the tenant's first
   object on. This is what finds a seal that was removed, and a notary that
   stopped.
2. **It is signed by a key to be believed**: a pinned root, or a key a pinned
   root delegated to, for this profile and tenant, within the delegation's
   window (at most 25 hours) and not revoked as of the seal. The bucket's own
   time of writing the seal is checked beside the seal's `sealed_at`.
3. **It chains**: its `prev` is the SHA-256 of the seal of the hour before, and
   the first seal of a tenant has none.
4. **It says what the hour holds now**: the count, the first and last key and
   the Merkle root are recomputed from the objects in the bucket, so that an
   object added to a sealed hour, removed from it or changed breaks the seal. A
   changed object whose metadata was made to agree is caught here, by the
   root, and not by its own sha256.
5. **The lock**, when given the deployment: a seal is locked as long as the
   records it covers.

A range in the middle of a chain checks its first seal against the seal just
before the range. Each finding names the rule it breaks (`seal.signature`,
`seal.chain`, `seal.root`, `seal.missing`, `seal.key`, `seal.payload`,
`seal.jws`, `seal.lock`, `keys.roots`, `keys.statement`), the seal's key and the
tenant.

Output: one line per object and per seal, `valid`, `unlocked` or
`INVALID: <reason>`, and a summary of the objects and records checked. Exit code
non-zero on any invalid entry; `unlocked` is information and does not count.

Auditors run it with read-only credentials scoped to the installation's
prefix. The nightly run inside the cluster (`audit verify --config`, whose
file has the same settings under their own names) does the same for the
previous day and, when it checks seals (`seals.roots` in its file), records the
outcome through the writer, per ingest hour, as `audit.seal.verified` or
`audit.seal.failed` (the target of each is
`records/<profile>/<yyyy>/<mm>/<dd>/<hh>`, the prefix of the hour). These
events say what they name: a run that checked no seal records none, because a
record claiming it had would be false assurance. They replace the v0
`audit.digest.*` events (common catalogue 2.0.0).

| flag | what |
|---|---|
| `--profile <p>` | required; the profile to check |
| `--from`, `--to` | the range of ingest time, `--to` not included |
| `--last 24h` | the objects ingested in the last this long, ending at the hour that has closed; instead of `--from` and `--to` |
| `--prefix <p>` | the installation's prefix in the bucket, as `archive.prefix` of the chart's configuration names it; omit only where the installation is at the bucket's root |
| `--root <thumbprints>` | also check the seals, trusting only the root keys with these thumbprints, comma separated; `--settle` and `--grace` set when a seal is due |
| `--sink <writer>` | record what was checked through the writer. The scheduled job does; an auditor's run by hand should not |
| `--deployment <file>` | the profile configuration, so the check knows what lock the profile demands. The scheduled job passes it; an auditor without it still gets keys, bytes and hashes checked, with nothing said about locks |
| `--json` | print the report as JSON |
| `--endpoint`, `--path-style` | an S3-compatible store that is not AWS, as [prepare the bucket](../how-to/prepare-the-bucket.md#s3-compatible-stores) has it |

`--profile` is required, and a profile is verified on its own: an
installation composing two profiles is two runs, because each profile has its
own retention to check against.

The scheduled job's configuration is `archive` (the bucket and prefix, and
nothing about a lock mode: it only reads), `deployment`, `last` (default
`24h`), optionally `profiles`, `seals` (`roots`, the thumbprints to pin, and
`settle` and `grace`) and `sink` with `require`. It holds no key, only the
thumbprints of public ones.

**The old archive is not read.** An archive written before the v1 layout, with
`profile=<p>/tenant=<t>/year=…` keys and a signed digest chain under
`digest/`, is read by nothing in v1 and is not checked by this command. It
stays verifiable with the previous release's CLI (v0.6.x), whose `audit verify`
walks that digest chain with the public key.

What a failed verification means, and what to do about it, is in
[investigate a failed verification](../how-to/investigate-a-failed-verification.md).
