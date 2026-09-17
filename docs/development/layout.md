# Layout and build order

For whoever implements this. Design is in `docs/`; do not re-decide what
`docs/decisions/` settles.

## Repository layout (target)

```
proto/audit/v1/     contracts (the schema of record)
gen/                generated Go and TypeScript, committed
schemas/            catalogue, preset and extension meta-schemas
presets/            framework presets
catalogue/          the common catalogue

record/             canonical record: identifiers, bounds, negative list, canonical form
catalogue/          catalogue loading, validation, composition, templates
preset/             preset loading and profile composition
emit/               the emitter an application imports
sink/               Sink interface and transports (inprocess, s3, nats)
keys/               Provider and Signer interfaces, with local (kms, transit to come)
store/              the object store interface; s3store/ the bucket; storetest/ the memory
                    store a test writes to, which can also be tampered with on purpose
index/              Indexer and Searcher interfaces, with memory, s3scan, postgres
auth/               Authenticator and Authorizer interfaces, with the defaults

internal/writer/    split, treat, roll, put, dead-letter, dedupe, ack (index to come)
internal/query/     the query service behind auth
internal/digest/    the digest chain: builder and verifier
internal/metering/  rollups, statements, rating adapters
internal/export/    OCSF, ECS, OpenTelemetry, Parquet
internal/cli/       the commands of cmd/audit

cmd/audit/          validate, check-emitters, verify, reindex, replay, conformance
cmd/protoc-gen-audit-jsonschema/  the buf plugin that writes the record's JSON Schema
cmd/audit-writer/   split writer service
cmd/audit-query/    query service
cmd/audit-console/  standalone console server
adapters/           openbao, keycloak, github, kubernetes
ts/                 @truvity/audit: types, Node emitter, viewer hooks, MUI skin
frontend/           standalone console SPA
charts/audit/       writer, query, console, digest cron, registry
```

**Public and internal.** A package a third party implements against or an
emitter imports is a top-level package and part of the compatibility promise.
A package only this repository's own services use is under `internal/`. A
helper that only a test should use lives in a `*test` package beside what it
helps with, as `store/storetest` does, so that importing it from production
code reads as wrong in the import path itself. This
follows the other public repositories in the estate: they publish a small
surface and keep the rest private.

## Build order

1. `proto` → `buf generate`; generated JSON Schema of the core; a test
   corpus of records that parse as proto and validate as JSON Schema.
2. `record`, `preset`, `catalogue`, `cmd/audit validate` — **done**.
3. `emit` + `sink` (inprocess, connect, nats) + outbox — **done**.
4. `internal/writer` + `keys` (local) + `store` (s3) — **done** but for payload
   detach, schema archiving on first use, the index step and the service
   binary.
5. `internal/digest` + `cmd/audit verify` — **done** but for the scheduled
   jobs and their meta-events, which come with the chart.
6. `index` (memory, s3scan, postgres) + `cmd/audit reindex`.
7. `internal/query` + `auth` + `cmd/audit-query`.
8. `ts/` types and Node emitter; viewer hooks; MUI skin; console.
9. `charts/audit`.
10. `internal/metering`.
11. `adapters/`, `internal/export`.

## Test infrastructure

Object Lock: MinIO with object locking enabled, or LocalStack. Stream:
`nats-server` in-process with JetStream. Postgres: a container. Keys:
`local` provider. A conformance corpus under `testdata/` exercised by every
searcher and every transport.

## Dogfooding

The first consumer is access-roster's audit trail, replacing its internal
package; the second is a multi-tenant product with billing. Neither
migrates old records.
