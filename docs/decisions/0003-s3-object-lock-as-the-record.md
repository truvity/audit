# 0003. S3 Object Lock in compliance mode is the record; everything else is a projection

- Status: accepted
- Date: 2026-09-17

## Context

The store must be append-only, provable, cheap for years, and acceptable
as evidence. Options surveyed ([research/storage.md](../research/storage.md)):
object storage with Object Lock, log stores (Loki, VictoriaLogs, Quickwit,
OpenSearch), analytical databases (ClickHouse), ledger databases (QLDB,
retired), and hosted audit lakes (CloudTrail Lake, closing to new
customers). Regulators' mental model is WORM storage; the Cohasset
assessment accepts S3 Object Lock in compliance mode without compensating
controls and governance mode only with them.

## Decision

Profile copies are written to a versioned, KMS-encrypted S3 bucket with
Object Lock in **compliance mode**, with the retention set per object at
write time from the profile, deletes denied by policy, and the bucket's own
access logged. Objects are NDJSON compressed with zstd, rolled every one to
five minutes or by size, under `tenant=/profile=/year=/month=/day=`.

Every other store is a **projection**: the facet index and counts table in
Postgres, the metering rollups and statements, SIEM exports, Parquet for
analytics. Projections are idempotent and rebuildable from the prefixes by
a reindex command. Nothing that bills or proves anything lives only in a
queue, a database or a log pipeline.

## Consequences

- Retention cannot be shortened after write, only extended, so the
  profile's retention must be right when the record is written and
  extended by addendum when expiry becomes known.
- Legal hold and its release are governed outside S3, by IAM policy and a
  break-glass role, and are recorded as meta-events.
- Lifecycle transitions to colder tiers apply only to already-large
  objects; small objects in deep archive cost more than they save.
- A log store may be added as a search tier later, fed by an
  OpenTelemetry mirror, but it is never authoritative.

## Alternatives considered

- **A log store as the record.** VictoriaLogs has no object-storage
  backend; Loki's retention is a compactor job that cannot prove
  immutability; neither is what a regulator recognises.
- **A ledger database.** QLDB was retired in 2025; immudb is another
  database to run and still needs an off-database evidence copy.
- **A hosted audit lake.** Immutable and serverless, but the bytes are not
  yours, the schema is theirs, and the offering surveyed is closing to new
  customers.
- **Governance mode.** Bypassable by anyone with the permission, and the
  console sends the bypass header by default.
