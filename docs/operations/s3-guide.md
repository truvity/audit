# S3 guide

What the bucket must look like for the record to be a record. The writer
sets per-object retention; the bucket must allow and protect it.

The bucket is on one of two tiers
([0014](../decisions/0014-lock-modes-and-store-tiers.md)), and which one is
the profiles' decision, not the operator's:

| tier | `archive.lockMode` | the store must answer | enough for |
|---|---|---|---|
| **record** | `compliance` (the default; `governance` for a non-production bucket) | `PutObject` with the Object Lock headers, `PutObjectRetention`, `PutObjectLegalHold`, `GetObject`, `HeadObject`, `ListObjectsV2`, presigned `GetObject` | every profile |
| **attested** | `none` | `PutObject`, `GetObject`, `HeadObject`, `ListObjectsV2`, presigned `GetObject` | profiles composed only from presets that demand no lock: `security`, `history`, `billing-nl` |

The writer refuses to start when a
composed profile demands a stricter lock than the deployment writes with,
naming the profile and both modes. Everything below describes the record
tier unless it says otherwise; [the attested tier](#the-attested-tier) says
what changes.

**The environment owns the bucket; an installation owns a prefix in it.** One
bucket per environment, with Object Lock, versioning, replication and a
deny-delete policy configured once, and one prefix per application —
`audit/<application>/` — with each installation's IAM scoped under its own.
That is the normal arrangement, not a variation on one bucket per
installation
([0011](../decisions/0011-one-installation-per-service-or-product.md)).

## Bucket

- Versioning enabled. Object Lock enabled at creation with a **default
  retention in compliance mode** equal to the shortest profile's
  retention; the writer sets longer per object.
- SSE-KMS with a customer-managed key whose policy allows the writer to
  encrypt and the query service and the verify role to decrypt,
  and nobody to schedule deletion of the key without a break-glass role.
- Public access blocked. Bucket policy denies `s3:DeleteObject`,
  `s3:DeleteObjectVersion`, `s3:PutBucketObjectLockConfiguration` changes
  that shorten, and `s3:BypassGovernanceRetention` to everyone; denies
  `s3:PutObjectLegalHold` removal except to the break-glass role with MFA.
- Server access logging or data-event trail enabled on the bucket, so
  direct reads are logged even when they bypass the query service.
- Cross-region replication to a bucket in another region and account with
  Object Lock, with replication of retention metadata.

None of that is per installation. An application arriving in the environment
gets a prefix and three roles, and changes nothing about the bucket.

## Sharing a bucket

Each installation is given a prefix of its own and told about it once, as the
chart's `prefix`:

```yaml
audit:
  bucket: audit-eu-example-1
  prefix: audit/<application>
```

Under that prefix the layout is the same for every installation, which is
what lets two applications on different versions of this component share one
bucket: the archive's layout is the contract between them, not the code.

| what | where | who may see it |
|---|---|---|
| one application's records and catalogues | `audit/<application>/…` | that installation's three roles, and an auditor's read-only role |
| another application's | `audit/<other>/…` | its own, and nobody from the first |

Lifecycle rules filter on `<prefix>/records/<profile>/`, one rule per profile
per application. A bucket-wide rule would apply the shortest profile's
transition to every application in it, which is why the filter names the
prefix as well.

`prefix: ""` puts an installation at the bucket's root. That is for a bucket
with exactly one installation in it and nothing else, and it forecloses ever
adding a second.

## Prefixes and retention

Everything one installation writes, beneath its `prefix`:

| prefix | written by | what | retention |
|---|---|---|---|
| `records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>` | writer | one object per ingest batch, by the hour of ingest ([the contract](../reference/bucket-contract.md)) | the profile's, per object at PUT (years after expiry for an `after_expiry` profile) |
| `catalogue/<app>/<version>` | writer | the application's catalogue at that version, written once | the longest profile |
| `schema/…` | writer | extension schemas and the record schema the records were written under | the longest profile |
| `dlq/year=/month=/day=/…` | writer | records the writer could not take | the longest profile |
| `holds/<id>/…` | `audit hold` | legal holds placed and released | the longest profile |
| `identity/tenant=<t>/purpose=<p>/<pseudonym>` | writer | the sealed identity behind a pseudonym, for resolve | the longest profile |

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
`endpoint`, `pathStyle` and `credentialsEnv` as the archive's bucket. It
inherits none of them from the archive: name each explicitly, and the exports
bucket has credentials of its own.

## The attested tier

The same archive, the same keys under the same prefix — on a store that holds no lock. Either the store has no Object Lock API,
which is most S3-compatible stores, or the deployment composes only profiles
that demand none and chooses not to lock. The writer is told with
`archive.lockMode: none` (the interactive commands' `--lock-mode none`), sends no lock header on
any put, and answers a retention extension or a legal hold with
`store.ErrNotLockable`: the extension is recorded in the trail as not made,
and `audit hold place` is refused and records the attempt.

What the deployment supplies in place of the lock:

- **Seals under a managed key.** Without the lock, only a seal made with a key
  the operator cannot re-sign with proves the operator did not choose what the
  archive holds. Seals ([0019](../decisions/0019-seals.md)) are not built yet,
  so on this tier the per-object `sha256` and per-record hashes that
  `audit verify` checks are what there is today.
- **No delete permission on any component**, exactly as on the record tier,
  and versioning on where the store offers it.
- **A bucket-level no-delete rule where the store has one.** Several stores
  let an administrator forbid deletes on a bucket or a prefix by a rule the
  same administrator can remove. That is governance-shaped protection, worth
  having and not to be mistaken for compliance mode; record in the
  deployment's own runbook that it is set.
- **Retention as a lifecycle rule** rather than a lock: the store expires
  objects when the rule says, and nothing stops the rule being shortened. A
  profile that cannot accept that demands the lock, and says so.

`audit verify --deployment <file>` then reports each object as `unlocked`
rather than pretending to have checked a lock; under a profile that demands
one, an object with no lock is `INVALID`.

## S3-compatible stores

Any store that speaks the S3 API takes the archive on the attested tier, and
on the record tier if it implements Object Lock. Three things differ from
AWS, and every component that touches the archive takes all three from the
`bucket` block of its configuration (`endpoint`, `pathStyle`,
`credentialsEnv`; the interactive commands take `--endpoint` and
`--path-style`, with credentials from the environment):

```yaml
archive:
  bucket:
    name: audit-example
    region: auto
    endpoint: https://s3.example.test
    pathStyle: true
    credentialsEnv:
      accessKeyID: AUDIT_S3_ACCESS_KEY_ID
      secretAccessKey: AUDIT_S3_SECRET_ACCESS_KEY
```

- **The endpoint.** `bucket.endpoint`. Unset is the SDK's own resolution for
  the region, which is AWS.
- **Path-style addressing**, when the store's certificate does not cover a
  bucket subdomain: `bucket.pathStyle` sends `endpoint/bucket/key` rather than
  `bucket.endpoint/key`.
- **Static credentials**, when the store has no pod identity:
  `bucket.credentialsEnv` names the two environment variables that hold the
  access key id and the secret, and the component's `secretEnv` puts a
  Secret's keys there. Unset, the SDK's ambient credentials are used, which is
  what a workload identity provides.
- **A private CA.** `bucket.ca` is the path to a bundle trusted for the
  endpoint, mounted by the platform (the chart's `trust` puts one at
  `/etc/audit/trust/<key>`).

With an endpoint set, the SDK's default CRC32 request checksum — which AWS
answers and other stores may refuse — is sent only where an operation
requires one. The SHA-256 the archive names on every put is still sent, and
still stored as the object's checksum where the store keeps one.

One region-shaped trap: a store that serves one region and does not answer
a bucket-location lookup wants `region` set to whatever it documents
(often `auto`), so that the SDK skips the lookup. Set it as
`bucket.region`.

Where the store's documentation names an S3 feature it does not implement
— conditional writes (`If-None-Match`), `ListObjectsV2` continuation, SSE-KMS
with a customer key — check before choosing it: the writer relies on the
first two, and `archive.kmsKey` on the third.

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

## Legal hold

```
audit hold place --profile <p> [--tenant <t>] --reason <why> --by <who> --bucket <b> --sink <writer>
audit hold list [--profile <p>] --bucket <b>
audit hold release --id <id> --by <who> --bucket <b> --sink <writer>
```

A hold keeps objects undeletable for as long as it is on, whatever their
retention says, and it has no expiry of its own. Placing one is an operator
action recorded as `audit.hold.placed`; releasing one requires the break-glass
role, which the archive's own policy enforces, and is recorded as
`audit.hold.released` — including when the archive refuses it, so that nobody
holding the role can try quietly. Presets say whether holds are recommended.

A hold is placed within one installation's prefix and affects that
installation only.

Both events are declared `block`, so `place` and `release` refuse to run
without `--sink`, and wait for the writer to confirm the record. The record is
made after the hold changes, not before, so the trail never claims a hold that
then failed; if the writer cannot take it, the command fails with an error
saying the hold **is** placed (or released) and must be recorded by hand. The
hold's own record under `holds/` in the archive is there either way.

A hold is placed on a prefix and the archive holds objects, so it has two
halves. `place` sweeps what is already there. The writer sets the hold on
objects it writes afterwards, re-reading the active holds every minute: an
object held only by a later sweep was deletable in between, and that window is
the whole thing a hold is for. The writer reads the holds once before it writes
anything and refuses to start if it cannot; after that, a refresh that fails
keeps the last answer, because forgetting a hold is worse than acting on a list
a minute old. The writer's role therefore needs `s3:PutObjectLegalHold` — to
set a hold on, never off — and read access to `holds/`.

The record of a hold lives in the archive under the same lock as everything
else, and is append-only like everything else: `holds/<id>/placed.json`, and
`holds/<id>/released.json` when it comes off. A reason is required — a hold
nobody can account for cannot be safely released, because whoever finds it
later has no way to know whether the matter is over.

**Before erasing a tenant's keys, list the holds.** Crypto-shredding a tenant
whose copies are under legal hold destroys evidence that may not be destroyed.
This applies only where the deployment runs pseudonymisation keys at all; see
[key custody](key-custody.md).

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
