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
keys/               Provider and Signer interfaces: local and OpenBAO transit providers;
                    local, AWS KMS and transit signers
store/              the object store interface; s3store/ the bucket; storetest/ the memory
                    store a test writes to, which can also be tampered with on purpose
index/              Indexer and Searcher interfaces, and the memory implementation
                    of both; postgres/ the default index, searcher and shared
                    dedupe table; s3scan/ a searcher with no index at all
auth/               Authenticator and Authorizer interfaces, with the defaults

internal/writer/    split, treat, roll, put, index, dead-letter, dedupe, ack
internal/query/     the query service behind auth
internal/digest/    the digest chain: builder and verifier
internal/hold/      legal holds: the records, and the writer's view of them
internal/registry/  the catalogue registry: validation, storage, the service
internal/s3test/    a real S3 for the archive walks; internal/pgtest/ a database
internal/clock/     an SNTP client, for the daily check ETSI asks be recorded
internal/metering/  rollups, statements, rating adapters
internal/export/    OCSF, ECS, OpenTelemetry, Parquet
internal/cli/       the commands of cmd/audit

cmd/audit/          validate, check-emitters, verify, replay, migrate, reindex, digest,
                    purge, clock-sync (conformance to come)
cmd/protoc-gen-audit-jsonschema/  the buf plugin that writes the record's JSON Schema
cmd/audit-writer/   split writer service
cmd/audit-registry/ catalogue registry service
cmd/audit-query/    the read service
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
   the key providers is `kms`; `transit` is built and tested against a real
   OpenBAO dev server.
9. `index`, read side: the `Searcher`, cursors, facets, tail — **done**, in
   memory, Postgres and an object-storage scan. What is left is the one
   corpus asked of all three, which is the conformance suite's.
10. `internal/query` + `auth` + `cmd/audit-query` — **done** but for `resolve`,
    which needs the identity map, and for the two real authenticators: only the declarative
    authorizer and the tests-only `none` exist, so a deployment puts its own
    authentication in front until `jwt` and `trusted-upstream` land.
11. The conformance suite, as its own recipe and CI job. It signs off the
    first adoption, so it precedes it.
    It moved ahead of metering (2026-09-18): every exit the write and read
    paths could not meet without a container harness now lives here, and the
    metering projection should be tested against this corpus rather than
    against fixtures of its own — a fixture kinder than reality is how every
    bug of the last day hid.
12. `internal/metering`. It reads only what the write path already produces,
    and it is what validates the design's central claim, so it comes before
    the adopters rather than after them.
13. `ts/` types and Node emitter; viewer hooks; MUI skin; console; the read
    side of the chart.
14. `adapters/`, `internal/export`.

## Test infrastructure

`internal/s3test` runs the archive walks against a real S3 (`AUDIT_S3_URL` —
an endpoint, not a product, so LocalStack, MinIO or a real bucket all satisfy
it). `just test-s3` starts one; the tests skip without it, so `check` stays
hermetic.

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
