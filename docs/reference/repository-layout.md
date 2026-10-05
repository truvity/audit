# Layout, and how to add to it

For whoever changes this repository. [CONTRIBUTING](../../CONTRIBUTING.md) has
the gate and the rules; this page says where things are and how the usual
additions are made.

## Where things are

The repository is three Go modules. The root, `github.com/truvity/audit`, is the
installation: the writer, the query service, their stores and the operator's
command. `sdk/`, `github.com/truvity/audit/sdk`, is what an application
imports to emit records, and nothing else: it carries no database driver, no
stream server, no object-store client and no JWT library. See
[the SDK module](#the-sdk-module) below. `deploy/pulumi/` is the third, the AWS
shape as a Pulumi library, a module of its own so that Pulumi is in nobody
else's dependency graph. The committed `go.work` does not list it, for the same
reason: a workspace's module graph is one graph. Run its tests with
`just pulumi-test`, which turns the workspace off.

```
proto/audit/v1/       the contracts: record, sink, registry, query, seal (seals, delegations, revocations)
gen/jsonschema/       the record's JSON Schema, generated, committed
schemas/config/       each binary's configuration file schema
presets/              the framework profiles (the directory keeps its old name)

sdk/                  MODULE github.com/truvity/audit/sdk, tagged sdk/vX.Y.Z
  gen/                generated Go (ts/src/gen is the generated TypeScript), committed
  schemas/            meta-schemas: catalogue, preset, extension slot
  catalogue/          catalogue loading, validation, composition, sentences;
                      common.yaml, the component's own actions
  record/             the canonical record: identifiers, bounds, negative list, canonical form
  emit/               the emitter an application imports; the async queue; request
                      middleware; Register
  sink/               the write contract and its durability; the Connect client; Memory,
                      Discard, Func; logsink (log lines), sinktest (the conformance suite)
  auth/               Principal, Authenticator, Authorizer, grants, rules; workload
                      tokens (TokenFile, Middleware)
  telemetry/          span attribute names and the Connect trace interceptors
  metaschema/         validation against the meta-schemas
  embed.go            the meta-schemas and the common catalogue, embedded

authn/                the JWT authenticator and the `access-roster` grants preset (sluis's group grammar)
sinkserver/           the SinkService handler and the Receiver (what the writer mounts)
sink/natssink/        the NATS JetStream publisher
sink/sqssink/         the SQS publisher
dedupe/dynamodbdedupe/ the writer's deduplication on DynamoDB, for a writer with no database
cmd/audit-writer-lambda/, cmd/audit-notary-lambda/
                      the writer and the notary as AWS Lambda functions

deploy/pulumi/        MODULE github.com/truvity/audit/deploy/pulumi, tagged deploy/pulumi/vX.Y.Z:
                      the AWS shape as a Pulumi Go library; it imports nothing of this
                      repository, and `just pulumi-test` runs it against Pulumi's mocks
preset/               presets, profile composition, the deployment document
keys/                 pseudonymisation providers (local, OpenBAO transit) and signers, for seals
                      (P-384 key file, AWS KMS ECC_NIST_P384, OpenBAO transit ecdsa-p384)
store/                the object store interface and the v1 archive layout, seals and keys
                      included; s3store/ the bucket;
                      storetest/ a memory store a test writes to and can tamper with
index/                Indexer and Searcher; memory; postgres/ the index, searcher, dedupe,
                      migrations; s3scan/ a searcher over the archive; indextest/ the
                      conformance suite every searcher runs
wire/                 the Connect JSON codec (snake_case)
writer/               the writer as a library: Open(Config)
query/                the query service as a library: New(Config)

internal/writer/      split, identity treatment, roll, put, dead letters, dedupe,
                      retention addenda, the writer's own account of itself
internal/observe/     the cursor indexer: list from a cursor behind a settle window, read objects,
                      index and advance in one transaction; wake-ups; observetest/ the cases it
                      is held to over memory, S3 and Postgres
internal/query/       search, facets, get, export, resolve, behind grants
internal/recobj/      a record object: the key, the metadata, the body, encoded and decoded
internal/merkle/      the RFC 6962 tree over SHA-256: root, audit paths, the vectors
internal/seal/        seals, delegations and revocations as JWS (ES384): signing, parsing,
                      thumbprints, JWK Sets, what an hour holds, and which keys to believe
internal/bucketcontract/
                      the check of the bucket contract, which is also its
                      conformance suite (memory store and LocalStack S3)
internal/identity/    sealed identities, for resolve
internal/hold/        legal holds, and the writer's view of them
internal/registry/    registered catalogues: validation, storage, the archive copy —
                      served by the writer, not by a service of its own
internal/clock/       an SNTP client for the daily clock check
internal/telemetry/   OTLP export and the writer's metrics; re-exports the SDK's names
internal/cli/         the commands of cmd/audit, and the notary's run (cmd/audit-notary)
internal/config/      each binary's configuration: the types, the loader, and the
                      schema generator behind schemas/config/
internal/corpus/      the record corpus (testdata/records) for transport tests
internal/s3test/      a real S3 for the archive walks and the conformance suite;
                      internal/pgtest/ a database
internal/authtest/    token issuers for tests
internal/schemagen/   the record's JSON Schema

cmd/audit/            the operator's command: validate, check-emitters, messages, profile,
                      verify, conformance, replay, migrate, reindex, purge,
                      clock-sync, hold, key
cmd/audit-writer/     the receiver and the writer (one binary, two modes), and
                      RegisterCatalogue
cmd/audit-observe/    the indexer
cmd/audit-query/      the query service
cmd/audit-notary/     the notary: seals each closed hour, once per run
cmd/protoc-gen-audit-jsonschema/   the buf plugin for the record's JSON Schema

charts/audit/         the installation an application's own chart instantiates:
                      receiver, writer, indexer, query service, the notary and the other three jobs
ts/                   @truvity/audit: client, qualifier box, sentences, React hooks and view
examples/             emit, read — compiled and tested by the gate
testdata/             the record corpus; the template fixture both scanners share
hack/                 the leak canary
```

(The root's `sink/` holds only the two stream publishers: the rest of the old
`sink/` moved to `sdk/sink/`, and the handler to `sinkserver/`.)

### The SDK module

An application that reports what it does needs the record, the generated
types, the emitter, the sink client and the catalogue. It does not need the
writer's dependencies, and before the split it paid for them: importing
`emit` resolved a module that required pgx, a NATS server, the AWS SDK, the
OpenTelemetry SDK and exporters, a JWT library and a product's client.
Now it resolves `sdk/`, whose non-test dependencies are Connect, protobuf,
the OpenTelemetry API and the Connect interceptor, a JSON Schema validator
and a YAML reader.

`just sdk-closure` holds that: it lists the packages `sdk/` builds and fails,
naming them, if any belongs to the server set (database drivers, NATS, the AWS
SDK, OpenBAO, Helm, gRPC, the OpenTelemetry SDK and exporters, `lestrrat-go`,
and the root module's own server packages). It also refuses to pass on a list
too short to be a real closure. CI runs it as its own job.

#### How the two modules are joined, and how an SDK change ships

The root's `go.mod` requires `github.com/truvity/audit/sdk` at a released
version and carries no `replace`: Go refuses `go run` and `go install` of a
package from a module whose `go.mod` has one, and consumers run the CLI by
version (`go run github.com/truvity/audit/cmd/audit@vX.Y.Z`). For development a
committed `go.work` (`use . ./sdk`) makes the root see `sdk/` as it is on disk,
so an edit to `sdk/` and the root code that uses it build and test together,
locally and in CI. Every Justfile recipe uses the workspace except two, which
turn it off to speak for a consumer:

- `just installable` builds the root and runs `go run ./cmd/audit version` with
  `GOWORK=off`, against the SDK version the root requires, fetched from the
  proxy. It also fails on a `replace` in `go.mod`.
- `just sdk-require` is the guard on the flow below.

How an SDK change flows:

1. A change to `sdk/` that the root depends on is released with the root's
   `require` of the SDK bumped to the upcoming version (`go mod edit
   -require=github.com/truvity/audit/sdk@vX.Y.Z`, then `GOWORK=off go mod
   tidy`), in the same release PR. A change to `sdk/` that the root does not
   need needs no bump.
2. The release job tags `sdk/vX.Y.Z` at the same commit as `vX.Y.Z`, so
   `go run .../cmd/audit@vX.Y.Z` resolves the SDK at `sdk/vX.Y.Z`.
3. `just sdk-require` fails when `sdk/` differs from the last `sdk/v*` tag and
   the root's required version is not newer than that tag, and says what to
   do. Until the release job tags the new version it cannot be fetched, so
   `just installable` says it is skipping rather than failing; it runs for
   real on every PR that does not change `sdk/`, and on the next one after.

Where each piece went, and why:

- **The Connect client stays the emitter's default and is in the SDK.**
  `sink.NewClient` needs only Connect, protobuf and the OpenTelemetry API.
- **The Connect handler and the Receiver are in `sinkserver/`** (root). They
  need the verified caller (`auth`), the JSON codec (`wire`) and the server's
  telemetry, and only a service mounts them.
- **`natssink` and `sqssink` stay in the root.** They pull the NATS client and
  the AWS SDK, and an emitter does not publish to a stream: it calls the
  receiver, which does. An application that really wants to publish directly
  imports the root module for those two packages, and says so.
- **`auth` is split.** The types an authorizer and an emitter's HTTP client
  share (`Principal`, `Grant`, `Rule`, `TokenFile`, `Middleware`) are in the
  SDK; the JWT authenticator and the `access-roster` grants preset, which need the JWT
  library and sluis's group grammar, are `authn/` in the root.
- **`catalogue` needed `Category` and `Class`,** which were in `preset`, and
  the meta-schemas, which were embedded from the root. The two types are now
  defined in `catalogue` and re-exported by `preset`; the three meta-schemas
  moved to `sdk/schemas/`, because a module cannot embed a file outside its own
  directory. `schemas/config/` stays at the root with the binaries it describes.
- **`internal/telemetry` is split:** the span attribute names, the allowlist
  and the Connect interceptors are `sdk/telemetry`; starting the exporters and
  the writer's metrics stay internal.

The root requires the SDK with `replace github.com/truvity/audit/sdk => ./sdk`,
so a change to both is one pull request and the root always builds against the
SDK beside it. The consequence is that the root module is not an import target
(a `replace` is ignored by whoever imports it, and `go install …@version` of
a module with one is refused): it is consumed as binaries, images and a chart,
which is all it was ever published for. `GOWORK` stays `off`; there is no
`go.work`.

The two modules are released together: `vX.Y.Z` for the root, and `sdk/vX.Y.Z`
for the SDK, at the same commit and the same version
([release contract §1](https://github.com/truvity/policy/blob/master/docs/contracts/release.md)).
`release.yaml` pushes the second after the first succeeds. An SDK consumer pins
`github.com/truvity/audit/sdk vX.Y.Z`.

**Releasing by hand: push the root tag only.** The root `vX.Y.Z` is the only
tag a person pushes. The `sdk-tag` job of the Release workflow creates
`sdk/vX.Y.Z` and `deploy/pulumi/vX.Y.Z` at the same commit; do not push them
yourself. The job accepts a tag that already peels to the release commit
(skips it) and fails on one at another commit. goreleaser ignores `sdk/*` and
`deploy/*` tags (`git.ignore_tags`), so the version only ever comes from a root
tag.

**Public and internal.** A package a third party implements against or an
application imports is a top-level package and part of the compatibility
promise: `sdk/record`, `sdk/catalogue`, `preset`, `sdk/emit`, `sdk/sink`, `keys`,
`store`, `index`, `sdk/auth`, `authn`, `writer`, `query`. Everything only this repository's own
binaries use is under `internal/`. A helper only a test should use lives in a
`*test` package beside what it helps (`store/storetest`, `index/indextest`).
`examples/` imports only public packages, and a test fails if that stops being
true. `writer` and `query` are public because the two binaries are built on
them, not because a deployment should run a writer inside an application: there
is no such shape
([0011](../decisions/0011-one-installation-per-service-or-product.md)).

**The tree above is the one this documentation specifies.** One part of it
arrives with the rest of the code rewrite: `examples/embed` is still in the
checkout and is being deleted.

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

**An action to the common catalogue.** Add it to `sdk/catalogue/common.yaml`
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
against the real service it wraps. Add it to the `keys` block of
`internal/config/schema/schema.go` (then `just config-schemas`) and its
loader's refusals, with refusals for configurations it cannot run in.

**A signer.** Implement `keys.Signer`; `KeyID` names the key version, so a
verifier can pick the public half after a change.

**A configuration key.** Add it to the type in `internal/config/types.go` and
the schema in `internal/config/schema/schema.go`, run `just config-schemas`,
and document it in the [configuration reference](configuration.md).
The chart takes it through `config:` with no change.

**A chart value.** For what is the platform's and not the binary's, add it to
`values.yaml` with a comment, use it in the
templates, add a refusal under `tests/invalid/audit/` for any
combination the binary would reject, and run `just chart` to update the
goldens (commit them).

**A command.** A function in `cmd/audit/main.go` that parses flags (a scheduled command
also takes `--config`, with its schema in `schemas/config/`) and calls a
type in `internal/cli` that does the work and is tested there. Add it to the
usage text.

## Dogfooding

This is used by its authors before it is offered to anyone else, and the first
adopters replace an audit trail they already had rather than starting from
nothing. None migrates its old records: the formats differ, a translation
layer would have to be trusted, and the old objects age out under their own
retention.
