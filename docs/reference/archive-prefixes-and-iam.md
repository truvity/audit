# Archive prefixes and IAM

What an installation writes under its prefix, who may put and get what, and what
breaks verification. For the procedure of making a bucket see
[prepare the bucket](../how-to/prepare-the-bucket.md); the key grammar itself is
the [bucket contract](bucket-contract.md).

## Prefixes and retention

Everything one installation writes, beneath its `prefix`:

| prefix | written by | what | retention |
|---|---|---|---|
| `records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>` | writer | one object per ingest batch, by the hour of ingest ([the contract](../reference/bucket-contract.md)) | the profile's, per object at PUT (years after expiry for an `after_expiry` profile) |
| `catalogue/<app>/<version>` | writer | the application's catalogue at that version, written once | the longest profile |
| `seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws` | notary | one signed seal per profile, tenant and hour, empty hours too, chained through `prev` | that of the records it covers, per object at PUT |
| `keys/roots.jwks` | the notary, once, if absent; otherwise the operator | the root public keys, as a JWK Set: distribution, not trust | none; the bucket versions it |
| `keys/delegations/<thumbprint>/<ULID>.jws`, `keys/revocations/<ULID>.jws` | a root | statements about keys a verifier checks | none |
| `schema/…` | writer | extension schemas and the record schema the records were written under | the longest profile |
| `dlq/year=/month=/day=/…` | writer | records the writer could not take | the longest profile |
| `holds/<id>/…` | `audit hold` | legal holds placed and released | the longest profile |
| `identity/tenant=<t>/purpose=<p>/<pseudonym>` | writer | the sealed identity behind a pseudonym, for resolve | the longest profile |

The notary is a fourth identity beside the writer, the verifier and the query
service: it may get and list under the prefix, put under `seals/` and the one
object `keys/roots.jwks`, decrypt, and use the seal key, and it may not put under
`records/`, which is the writer's. The writer may not put under `seals/` or
`keys/`: whoever writes the archive and can also seal it can choose what to seal.
The lifecycle rules for seals are those of the records they cover.

A record's own date does not decide where it lives: a reader finds it by the
hour it was ingested. A profile's name is a key component, so it must not
contain `/`, and a record whose tenant id contains `/` is dead-lettered.

A catalogue object is written once. The same bytes again are a success; other
bytes under the same version make the writer refuse to start, which it checks
at start-up for the catalogues it runs with and at the first record of any
other.

The archive written before the v1 layout (`profile=<p>/tenant=<t>/year=…`,
with `digest/` and `verified/`) is read by nothing in v1. It stays readable
with the previous release's CLI (v0.6.x), and a bucket that holds both needs
the lifecycle rules of both until the old objects expire.

The `identity/` prefix exists only where the deployment configured a key provider.
`keys.provider: none` is the default (no `keys` block), and an installation running without
keys writes no `identity/` prefix at all
([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)).

Exports go to a **separate bucket with no Object Lock** and a lifecycle rule
that expires `export/`: an export is a copy meant to be collected and cleared,
and the archive's policy denies every delete. It may be on a store of its
own: `exports.bucket` of the query service's configuration takes the same
`endpoint`, `pathStyle` and `credentialsSecret` as the archive's bucket. It
inherits none of them from the archive: name each explicitly, and the exports
bucket has credentials of its own.


## IAM per component

Four roles per installation, each bound to its own service account (Pod
Identity or IRSA); the chart has a `serviceAccount` per component for it.
The receiver (stream mode) and the clock-sync job get a service account too, and
no role: they hold no S3 rights. The purge job works on the index database only
and needs no S3 rights.
Every one of them is scoped **under that installation's prefix** — write
`arn:aws:s3:::<bucket>/<prefix>/*` in the resource, and condition
`s3:ListBucket` on `s3:prefix` being `<prefix>/*` — so that an application
cannot read or write another application's records even though the bucket is
one.

| role | on the archive, under its prefix | elsewhere |
|---|---|---|
| **writer** | `s3:PutObject`, `s3:PutObjectRetention`, `s3:GetObjectRetention`, `s3:PutObjectLegalHold` under `records/`, `catalogue/`, `schema/`, `dlq/`, `holds/` and `identity/` (where there are keys); `s3:GetObject` and `s3:ListBucket` on `records/`, `catalogue/`, `holds/`, `schema/`, and `identity/` where there are keys | `kms:GenerateDataKey`, `kms:Encrypt` on the bucket's key |
| **verify job** | `s3:GetObject`, `s3:ListBucket`; nothing is put | `kms:Decrypt` on the bucket's key |
| **indexer** (`audit-observe`) | `s3:GetObject`, `s3:ListBucket` on `records/`, `catalogue/` and `schema/`; nothing is put | `kms:Decrypt` on the bucket's key |
| **query service** | `s3:GetObject`, `s3:ListBucket` | `s3:PutObject`, `s3:GetObject` on the exports bucket; `kms:Decrypt` |

Only the **writer** holds `s3:PutObjectLegalHold`, and only because it places
holds. A put carries the legal-hold header solely when it is placing one, so
no other component needs it. If
a component that places no holds is refused `s3:PutObjectLegalHold` on a plain
put, it is running a version that sent the header as OFF on every put; upgrade
it rather than granting the right.

`schema/` is easy to miss and the writer does not start without it. It records
each profile's composition there, and reads the last one back on **every
start** to decide whether the profile has changed since it last wrote. A policy
that lets it put that object and not get it produces a writer that writes one
object, takes a 403 and dies, on a loop -- which reads as a broken archive
rather than a missing verb.

The separations inside that table are the point of it. The writer may put
objects and may lengthen a lock; the verify job, the indexer and the query service may read
and may write nothing into the archive at all. And **nobody, including the writer, gets
`s3:DeleteObject`, `s3:DeleteObjectVersion` or
`s3:BypassGovernanceRetention`** — not on its own prefix, and not on anyone
else's.

Two roles belong to people rather than to components, and the purge job
needs nothing here at all:

| role | on the archive, under the installation's prefix |
|---|---|
| purge job | none — it works on the index database |
| an operator running `audit hold` | `s3:PutObjectLegalHold` (placing), `s3:GetObjectLegalHold`, `s3:ListBucket`, `s3:PutObject` on `holds/` |
| break-glass | `s3:PutObjectLegalHold` with `s3:object-lock-legal-hold` = `OFF` (releasing) |

Lifecycle ([0023](../decisions/0023-archive-retention-and-lifecycle.md)).
The chart creates no buckets, so these are rules the environment's bucket
carries: Glacier Instant Retrieval at 30 days and Deep Archive at 1 year, one
rule per profile, filtered on `<prefix>/records/<profile>/`. Expiration only
after lock expiry, which S3 enforces anyway. This is why the profile is the
leading component of `records/`: a lifecycle filter matches a literal prefix
and takes no wildcards, so a rule per profile is possible only in that order.

Per-tenant credentials follow from the tenant being the next component: a
role scoped to one customer names
`arn:aws:s3:::<bucket>/<prefix>/records/<profile>/<tenant>/*` as its resource.


## What breaks verification

Moving or renaming objects: the key carries the profile, tenant and ingest
hour, and a reader finds a record by it. Changing the KMS key without keeping
the old one decryptable. Re-uploading an object under the same key (a new
version) is refused by the writer's conditional put, and a changed object is
reported by `audit verify`, whose check of the stored bytes no longer matches
the object's `sha256`.

Moving one installation to a different prefix moves every key with it. A
prefix is chosen when an installation is created and not changed afterwards.

## Break-glass reads

Auditors get a read-only role scoped to one installation's prefix — its
`records/` and `catalogue/` prefixes. Their reads appear in the bucket's
access log and, when made through the query service, as `audit.get` and
`audit.search` records.
