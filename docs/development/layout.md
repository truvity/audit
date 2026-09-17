# Layout and build order

For whoever implements this. Design is in `docs/`; do not re-decide what
`docs/decisions/` settles.

## Repository layout (target)

```
proto/audit/v1/            contracts
gen/                       generated Go and TypeScript, committed
schemas/                   catalogue, preset, extension meta-schemas
presets/                   framework presets
catalogue/                 common catalogue
cmd/audit/                 CLI: validate, check-emitters, verify, reindex, replay
cmd/audit-writer/          split writer service
cmd/audit-query/           query service
cmd/audit-digest/          digest and verify jobs
cmd/audit-console/         standalone console server
internal/record/           canonical record helpers, bounds, negative list
internal/catalogue/        loading, validation, composition, templates
internal/preset/           loading, composition, validation
internal/emit/             emitter library core (Go)
internal/sink/             Sink interface and transports
internal/writer/           split, treat, roll, put, index, ack
internal/keys/             KeyProvider and Signer with kms, transit, local
internal/index/            Indexer and Searcher: memory, s3scan, postgres
internal/query/            QueryService, cursors, grants
internal/auth/             Authenticator and Authorizer
internal/digest/           digest job and verify
internal/metering/         rollups, statements, rating adapters
internal/export/           OCSF, ECS, OpenTelemetry, Parquet
adapters/                  openbao, keycloak, github, kubernetes
ts/                        @truvity/audit: types, Node emitter, viewer hooks, MUI skin
frontend/                  standalone console SPA
charts/audit/              writer, query, console, digest cron, registry
```

## Build order

1. `proto` → `buf generate`; generated JSON Schema of the core; a test
   corpus of records that parse as proto and validate as JSON Schema.
2. `internal/catalogue`, `internal/preset`, `cmd/audit validate`.
3. `internal/emit` + `internal/sink` (inprocess, s3, nats) + outbox.
4. `internal/writer` + `internal/keys` (local first, then kms, transit).
5. `internal/digest` + `cmd/audit verify`.
6. `internal/index` (memory, s3scan, postgres) + `cmd/audit reindex`.
7. `internal/query` + `internal/auth` + `cmd/audit-query`.
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
