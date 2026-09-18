# audit

A company-wide audit trail: one record format, one write path, one
immutable store, and projections for security, billing, history and
regulatory evidence.

**Status: write path.** A record can be emitted, validated against its
catalogue, carried over a stream or an outbox, split into one copy per
profile, written to a locked bucket with per-profile retention, indexed for
search, and later proved unaltered by an auditor who trusts nobody. The index
is a projection: `audit reindex` rebuilds it from the archive, and a test holds
the rebuild equal to what the writer wrote. The write side of the chart deploys all of it. The read path, the viewer and
the metering projection do not exist yet. Read [docs/why.md](docs/why.md) first, then
[docs/concepts.md](docs/concepts.md), then the decisions in
[docs/decisions/](docs/decisions/), then
[docs/development/layout.md](docs/development/layout.md) for what is built
and in what order the rest comes.

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
proto/audit/v1/     record, sink and query contracts (canonical schema)
schemas/            JSON Schema for catalogues, presets and the annotation vocabulary
presets/            one file per framework: required fields, retention, citations
catalogue/          the common catalogue (the component's own meta-events)
docs/why.md         what this is and is not
docs/concepts.md    record, catalogue, slots, profiles, presets, prefixes
docs/design/        pipeline, split writer, search, authz, integrity, metering, viewer
docs/decisions/     architecture decision records
docs/research/      the surveys the design rests on, with sources
docs/reference/     record, catalogue, presets, API, configuration
docs/operations/    S3 guide, key custody, verification, runbook
docs/development/   package layout and build order for implementers
charts/audit/       the write path, deployable; refuses what the binaries would
```

## Licence

MIT. See [LICENSE](LICENSE).
