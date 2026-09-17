# Pipeline

```
 apps ──emit──▶ stream ──▶ split writer ──▶ S3 prefixes ──▶ projections ──▶ query API ──▶ viewers
 adapters ─┘    (ack=done)  (one consumer)   per profile,    facet index    search/facets  embedded
 (secret mgr,               validate, split, per tenant,    counts table   get/export     console
  IdP, code                 pseudonymise,    locked, with    metering       tail cursor    Grafana
  host, k8s)                digest, index    schemas beside  SIEM/Parquet   authorizer     auditors
```

## Emit

The Go and TypeScript libraries expose one `Sink` interface. The emitter
library:

1. Fills `id` (UUIDv7), `occurred_at`, `schema_version`, `catalogue_version`,
   `source`, `sequence` and the producer instance.
2. Validates the record against the composed schema for its action.
3. Enforces the negative list (no secrets, tokens, passwords, session ids
   in clear, attribute values, user content) and size caps in the
   documented truncation order.
4. Chooses the delivery mode from the catalogue: block, outbox or
   best_effort.

Transports: in-process (the split writer embedded), direct object storage
(no stream available; break-glass), stream publisher (NATS JetStream).
Adapters are separate binaries using the same library.

## Stream

A file-backed JetStream stream with discard-new, TLS, a short horizon sized
to the longest tolerated writer outage, and exactly one permitted consumer.
Publish acknowledgement is the fail-closed boundary for block delivery.

## Split writer

See [split-writer.md](split-writer.md). Dedupe, validate, split per
profile, pseudonymise, roll objects, set Object Lock retention, copy
schemas on first use, index, count, ack.

## Prefixes

```
s3://<bucket>/
  tenant=<id>/profile=<name>/year=/month=/day=/<first-occurred-nanos>-<writer>-<seq>.ndjson.zst
  payload/sha256=<hash>                              large blobs, referenced by hash
  schema/<source>/<catalogue_version>/...             catalogues and extension schemas
  schema/audit/v<major>/record.schema.json, record.proto   the record's own schema and proto
  digest/profile=<name>/year=/month=/day=/hour=/...   hourly signed digests
  dlq/year=/month=/day=/...                            records the writer could not process
```

## Projections

The facet index and counts table (Postgres), metering rollups and
statements, exporters (OCSF, ECS, OpenTelemetry), Parquet for analytics.
All idempotent by event id, all rebuildable by replaying prefixes.

## Read

`QueryService` behind an `Authenticator` and an `Authorizer`. Every read
emits a `log_access` record.

## Meta-events

The pipeline records itself: writer start and stop, dead letters, digests
written and verified, registrations, profile changes, key destruction,
holds, and the daily clock check.
