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
index/              Indexer and Searcher interfaces; memory/ in this package,
                    postgres/ the default index and the shared dedupe table,
                    s3scan to come with the read side
auth/               Authenticator and Authorizer interfaces, with the defaults

internal/writer/    split, treat, roll, put, index, dead-letter, dedupe, ack
internal/query/     the query service behind auth
internal/digest/    the digest chain: builder and verifier
internal/hold/      legal holds: the records, and the writer's view of them
internal/registry/  the catalogue registry: validation, storage, the service
internal/clock/     an SNTP client, for the daily check ETSI asks be recorded
internal/metering/  rollups, statements, rating adapters
internal/export/    OCSF, ECS, OpenTelemetry, Parquet
internal/cli/       the commands of cmd/audit

cmd/audit/          validate, check-emitters, verify, replay, migrate, reindex, digest,
                    purge, clock-sync (conformance to come)
cmd/protoc-gen-audit-jsonschema/  the buf plugin that writes the record's JSON Schema
cmd/audit-writer/   split writer service
cmd/audit-registry/ catalogue registry service
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
4. `internal/writer` + `keys` (local) + `store` (s3) + `cmd/audit-writer` —
   **done**. Payload detach was dropped; the split-writer page says why.
5. `internal/digest` + `cmd/audit verify` + `audit digest` + `audit
   clock-sync` — **done**, each job keeping an account of itself through
   `--sink` (`internal/cli/selfreport.go`).
6. `index`, write side: the `Indexer` interface, the Postgres schema, the
   facet counts, the shared deduplication table, `audit migrate` and
   `audit reindex` — **done**. This closes the write path: it is what lets
   the writer run with more than one replica.
7. `charts/audit`, write side: writer, migrate hook, digest, verify, purge and
   clock-sync jobs — **done**. `just chart` holds it: golden renders, and the
   refusals for every configuration the binaries would reject or get quietly
   wrong.
8. Legal holds and `audit key destroy` — **done**. What is left of
   the key providers is `kms` and `transit`, which want a fake of each.
9. `index`, read side: the `Searcher`, cursors, facets, tail.
10. `internal/query` + `auth` + `cmd/audit-query`.
11. `internal/metering`. It reads only what the write path already produces,
    and it is what validates the design's central claim, so it comes before
    the adopters rather than after them.
12. The conformance suite, as its own recipe and CI job. It signs off the
    first adoption, so it precedes it.
13. `ts/` types and Node emitter; viewer hooks; MUI skin; console; the read
    side of the chart.
14. `adapters/`, `internal/export`.

## Test infrastructure

A test double must be no kinder than the thing it stands in for. Two bugs in
the archive walks — a digest covering one tenant, a listing stopping at S3's
first thousand keys — passed every test because the memory store returned
everything a caller asked for, in any layout, on one page. The S3 double in
`store/s3store` now sorts, pages and groups as S3 does, with a page size a test
can lower, and the conformance harness should run the archive walks against
MinIO for the same reason.

Object Lock: MinIO with object locking enabled, or LocalStack. Stream:
`nats-server` in-process with JetStream. Postgres: a container. Keys:
`local` provider. A conformance corpus under `testdata/` exercised by every
searcher and every transport.

## Dogfooding

This is used by its authors before it is offered to anyone else, and the
first adopters replace an audit trail they already had rather than starting
from nothing. Neither migrates its old records: the formats differ, a
translation layer would have to be trusted, and the old objects age out
under their own retention. A component whose authors have not lived with it
is a component whose rough edges are still everybody else's to find.
