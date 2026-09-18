# audit

A company-wide audit trail: one record format, one write path, one
immutable store, and projections for security, billing, history and
regulatory evidence.

## Start here

| you want to | read |
|---|---|
| see how it fits together | [Architecture](docs/architecture.md) — the parts, what each holds, the life of one record |
| run it in a cluster | [Deploying](docs/guides/deploy.md) — what to prepare, the chart, a diagram, how to check it works |
| connect an application to an installation | [Integrating](docs/guides/integrate.md) — the catalogue, the emitter, the audit page in your console |
| record what your application does | [Emitting records](docs/guides/emit.md) — the catalogue, registering it, the Go emitter; [`examples/emit`](examples/emit/main.go) |
| carry the trail inside your application: its own writer, its own query API | [Embedding](docs/guides/embed.md) — `writer.Open`, `query.New`; [`examples/embed`](examples/embed/main.go) |
| search and read the trail, or audit it | [Reading the trail](docs/guides/read.md) — access, the API, Go and TypeScript clients, `audit verify`; [`examples/read`](examples/read/main.go) |
| understand why it is built this way | [why](docs/why.md), then [concepts](docs/concepts.md), then [the decisions](docs/decisions/) |

## Status

| part | state |
|---|---|
| Record, catalogues, presets, `audit validate` / `check-emitters` | built |
| Go emitter: block, outbox and best-effort delivery; Connect and JetStream sinks | built |
| Writer: split per profile, pseudonyms, Object Lock, index, dead letters, legal holds | built |
| Pseudonymisation keys: local (root + directory) or OpenBAO transit | built; AWS KMS envelope designed ([decision](docs/decisions/0010-key-providers.md)), not built |
| Digest chain and `audit verify`; signing with a key file, AWS KMS or OpenBAO transit | built |
| Query service: search, facets, get with provenance, export, tail, resolve; JWT sign-in with grants | built |
| Embedding: the writer and the query service as Go libraries (`writer`, `query`) | built |
| Helm chart: writer, registry, query service, digest / verify / clock / purge jobs | built |
| TypeScript `@truvity/audit`: query client, qualifier box, catalogue sentences, React hooks and an MUI view to embed | built, not yet published |
| TypeScript emitter; a standalone console | designed, not built — applications host the page in their own console |
| Metering projection | not yet |
| A published release | not yet — build the images with `just snapshot` |

## What it is

- A canonical **record** described in Protocol Buffers, with predefined
  **extension slots** described in JSON Schema so applications add their
  own data without changing the core.
- An **action catalogue** per application: which actions exist, what they
  carry, which profiles they belong to, how they read as a sentence, and
  how they meter.
- A **split writer** that turns each record into one copy per **profile**,
  each in its own Object-Locked S3 prefix with its own field allow-list,
  identity treatment and retention. Profiles are composed from
  **framework presets** shipped in [presets/](presets/).
- A **query service** with faceted search, cursor pagination, export and a
  tail cursor, behind pluggable authentication and authorization.
- A **viewer**: an embeddable React package and a standalone console.
- A **digest chain** and a `verify` command so an auditor can prove nothing
  was removed or altered without trusting the operator.
- **Metering projections** so the same records feed usage-based billing.

## What it is not

- Not an application log pipeline. Application logs keep flowing through
  the observability stack; this component records what happened, who did
  it, to what, with what outcome, and keeps it for as long as a framework
  requires.
- Not a SIEM. It exports to one.
- Not a wallet transaction log. A digital-identity wallet's own log lives on
  the user's device and is invisible to the provider by law; this component
  only ever sees operational events with pseudonymous subjects.

## Layout

```
examples/           an application that emits, a program that reads — compiled in CI
proto/audit/v1/     record, sink and query contracts (canonical schema)
schemas/            JSON Schema for catalogues, presets and the annotation vocabulary
presets/            one file per framework: required fields, retention, citations
catalogue/          the common catalogue (the component's own meta-events)
docs/guides/        deploying, emitting, reading: start here
docs/why.md         what this is and is not
docs/concepts.md    record, catalogue, slots, profiles, presets, prefixes
docs/design/        pipeline, split writer, search, authz, integrity, metering, viewer
docs/decisions/     architecture decision records
docs/research/      the surveys the design rests on, with sources
docs/reference/     record, catalogue, presets, API, configuration
docs/operations/    S3 guide, key custody, verification, runbook
docs/development/   package layout and build order for implementers
charts/audit/       writer, registry and jobs, deployable; refuses what the binaries would
```

## Licence

MIT. See [LICENSE](LICENSE).
