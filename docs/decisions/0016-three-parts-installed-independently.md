# 0016. Three parts, installed independently; the bucket layout is the contract

- Status: accepted; refines [0011](0011-one-installation-per-service-or-product.md)
- Date: 2026-10-02

Refines [0011](0011-one-installation-per-service-or-product.md): an
installation is still one per application, and is now made of parts that can
be installed on their own.

## Context

[0011](0011-one-installation-per-service-or-product.md) describes an
installation as a receiver, a writer, a query service, an index database and
a set of CronJobs, rendered by one chart. In practice three different jobs
are hidden in that list, and they have different owners, different
privileges and different platforms:

- taking records in and keeping them, which needs write rights on the
  archive and nothing else;
- vouching for the archive, which needs a signing key and read rights on
  the archive and must be unable to write a record;
- reading the archive back, which needs a database, read rights and the
  readers' grants.

Today the indexer runs inside the writer, so a reader's schema change is a
write-path rollout. The digest CronJob is a signer in everything but name.
Neither can run where the others do not: a deployment on a function platform
cannot host a CronJob, and a deployment with no database has no use for an
indexer. What binds the jobs together already is the archive's layout, which
every one of them reads or writes.

## Decision

An installation has **three parts**, each installable without the other two:

| part | does | holds | never holds |
|---|---|---|---|
| **ingest** | takes records through a sink chain and writes them to the archive | write on `records/` and `catalogue/` | the signing key, a database |
| **notary** | lists the archive, seals each hour, publishes the trust anchors | read on `records/`, write on `seals/` and `keys/`, a signer | write on `records/` |
| **observe** | follows the archive and projects it into a search index, serves queries | read on the archive, a database, the grants | write on the archive |

**The bucket layout is the contract between them.** No part calls another.
Ingest writes objects, the notary reads them and writes seals, observe reads
both. A part can be absent: ingest alone is a complete, provable-by-hand
archive; adding the notary makes it verifiable by an auditor; adding observe
makes it searchable. A part can be a different implementation, or run on a
different platform, or at a different version, provided it keeps the
[contract](../reference/bucket-contract.md) — which is why the contract has a
conformance suite and not only a description.

The emitter, the sink chain and the catalogue stay as
[0011](0011-one-installation-per-service-or-product.md) and
[0017](0017-sink-durability-and-transports.md) say. The query service and
the index are observe; the digest and verify jobs become the notary and the
`verify` command ([0019](0019-seals.md)).

## Consequences

- **Each part ships and scales on its own**: its own binary (or a mode of
  the one binary), its own chart mode, its own credentials. A rollout of
  observe cannot stop ingest.
- **Least privilege is structural.** The notary cannot write a record, so a
  compromised notary can at worst refuse to seal; ingest cannot sign, so a
  compromised writer cannot vouch for what it wrote.
- **The indexer is decoupled from the writer.** It follows the bucket
  ([0020](0020-observe-follows-the-bucket.md)), so it can be rebuilt, paused
  or moved without a record being lost.
- **The contract is now a compatibility surface.** A change to it is a new
  version of the layout ([0018](0018-v1-bucket-layout.md)), not a refactor.
- Packaging is per part: the chart gains a mode for each, and a function
  platform runs ingest and notary as functions with no chart at all.

## Alternatives considered

- **Keep one installation unit.** Simplest to describe, and it is what 0011
  has. But it forces the notary's key and the writer's write rights into one
  trust boundary, and makes the indexer a write-path dependency.
- **Two parts, with the notary inside observe.** Both read the archive, but
  observe holds a database and the readers' grants, and a signer must not
  sit beside either.
- **Parts that call each other.** A writer that notifies the indexer
  directly is faster, and is a coupling that makes the notification the
  source of truth, which [0020](0020-observe-follows-the-bucket.md) refuses.
