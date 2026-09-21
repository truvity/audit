# Layout, and how to add to it

For whoever changes this repository. [CONTRIBUTING](../../CONTRIBUTING.md) has
the gate and the rules; this page says where things are and how the usual
additions are made.

## Where things are

```
proto/audit/v1/       the contracts: record, sink, registry, query
gen/                  generated Go (and ts/src/gen: generated TypeScript), committed
schemas/              meta-schemas: catalogue, preset, extension slot
presets/              the framework presets
catalogue/            catalogue loading, validation, composition, sentences;
                      common.yaml, the component's own actions

record/               the canonical record: identifiers, bounds, negative list, canonical form
preset/               presets, profile composition, the deployment document
emit/                 the emitter an application imports; the async queue; request
                      middleware; Register
sink/                 the write contract; Connect client and handler; sink/natssink (JetStream)
keys/                 pseudonymisation providers (local, OpenBAO transit) and digest signers
                      (key file, AWS KMS, OpenBAO transit)
store/                the object store interface and archive layout; s3store/ the bucket;
                      storetest/ a memory store a test writes to and can tamper with
index/                Indexer and Searcher; memory; postgres/ the index, searcher, dedupe,
                      migrations; s3scan/ a searcher over the archive; indextest/ the
                      conformance suite every searcher runs
auth/                 Authenticator, Authorizer, grants; JWT; workload tokens; the
                      access-roster grants preset
wire/                 the Connect JSON codec (snake_case)
writer/               the writer as a library: Open(Config)
query/                the query service as a library: New(Config)

internal/writer/      split, identity treatment, roll, put, index, dead letters, dedupe,
                      retention addenda, the writer's own account of itself
internal/query/       search, facets, get, export, resolve, behind grants
internal/digest/      the digest chain: builder, verifier, provenance
internal/identity/    sealed identities, for resolve
internal/hold/        legal holds, and the writer's view of them
internal/registry/    registered catalogues: validation, storage, the archive copy —
                      served by the writer, not by a service of its own
internal/clock/       an SNTP client for the daily clock check
internal/telemetry/   OTLP metrics
internal/cli/         the commands of cmd/audit
internal/corpus/      the record corpus (testdata/records) for transport tests
internal/s3test/      a real S3 for the archive walks; internal/pgtest/ a database
internal/authtest/    token issuers for tests
internal/metaschema/, internal/schemagen/   meta-schema validation; the record's JSON Schema

cmd/audit/            the operator's command: validate, check-emitters, messages, profile,
                      verify, digest, conformance, replay, migrate, reindex, purge,
                      clock-sync, hold, key
cmd/audit-writer/     the receiver and the writer (one binary, two modes), and
                      RegisterCatalogue
cmd/audit-query/      the query service
cmd/protoc-gen-audit-jsonschema/   the buf plugin for the record's JSON Schema

charts/audit/         the installation an application's own chart instantiates:
                      receiver, writer, query service, the four jobs
ts/                   @truvity/audit: client, qualifier box, sentences, React hooks and view
examples/             emit, read — compiled and tested by the gate
testdata/             the record corpus; the template fixture both scanners share
hack/                 the leak canary
```

**Public and internal.** A package a third party implements against or an
application imports is a top-level package and part of the compatibility
promise: `record`, `catalogue`, `preset`, `emit`, `sink`, `keys`, `store`,
`index`, `auth`, `writer`, `query`. Everything only this repository's own
binaries use is under `internal/`. A helper only a test should use lives in a
`*test` package beside what it helps (`store/storetest`, `index/indextest`).
`examples/` imports only public packages, and a test fails if that stops being
true. `writer` and `query` are public because the two binaries are built on
them, not because a deployment should run a writer inside an application: there
is no such shape
([0011](../decisions/0011-one-installation-per-service-or-product.md)).

**The tree above is the one this documentation specifies.** Two parts of it
arrive with the rest of the code rewrite: `examples/embed` is still in the
checkout and is being deleted, and `emit/` still carries the file outbox that
[0012](../decisions/0012-two-deliveries-and-a-durable-ack.md) retires.

## Tests, by what they need

| recipe | needs | runs |
|---|---|---|
| `just check` | the checkout | build, unit tests, lint, proto, drift, schemas, chart, vuln, leak canary |
| `just race` | a C toolchain | the tests under the race detector |
| `just test-postgres` | Postgres (started under `.devbox`) | the index, the dedupe table, the writer against a database |
| `just test-s3` | Docker (LocalStack) | the archive walks, the Object Lock refusal, KMS signing |
| `just conformance` | Docker, Postgres | everything, with Postgres, LocalStack and OpenBAO |
| `just chart` | the checkout | the chart's goldens and its list of refusals |
| `just ts` | the npm registry | the TypeScript package: typecheck, tests, build, what a publish ships |

A test skips when its service is absent and runs in CI, where every service
has a job with a guard that fails if the tests skipped. A double must be no
kinder than the thing it stands in for: two archive-walk bugs once passed every
test because a memory store returned everything on one page.

## How to add

**An action to the common catalogue.** Add it to `catalogue/common.yaml`
(template arguments with underscores), emit it from the code with the name as
a literal, and run `just schemas` (validate, and `check-emitters` over this
repository) and `just sentences` (the TypeScript copy of the templates).

**A searcher.** Implement `index.Searcher`, declare what it cannot do in
`Capabilities` (the suite requires a refusal, not a narrower answer), return
`index.ErrNotFound` for a missing record, and run
`indextest.Run(t, "name", searcher)` in its test. Every case in
`index/indextest` is then asked of it.

**A key provider.** Implement `keys.Provider` (and `keys.Sealer` to support
resolve). It must be stable across replicas and restarts, separate tenants and
purposes, never mint a key for an erased (tenant, purpose), and be tested
against the real service it wraps. Add it to `cli.KeyFlags` and the chart's
`keys.provider`, with refusals for configurations it cannot run in.

**A signer.** Implement `keys.Signer`; `KeyID` names the key version, so a
verifier can pick the public half after a change.

**A chart value.** Add it to `values.yaml` with a comment, use it in the
templates, add a refusal to `testdata/refusals.txt` for any combination the
binary would reject, and run `just chart` to update the goldens (commit them).

**A command.** A function in `cmd/audit/main.go` that parses flags and calls a
type in `internal/cli` that does the work and is tested there. Add it to the
usage text.

## Dogfooding

This is used by its authors before it is offered to anyone else, and the first
adopters replace an audit trail they already had rather than starting from
nothing. None migrates its old records: the formats differ, a translation
layer would have to be trusted, and the old objects age out under their own
retention.
