# audit

A tamper-evident record of what a system did and what it used. Products emit records, a writer seals them into a signed, witnessed log, and auditors read and verify the log with standard tools.

audit keeps two kinds of record, and they fail differently:

- **Security audit records** say what happened: a sign-in, a grant, a change, a refusal. They are OCSF events. A record may be lost when the transport is full, but the loss is counted, and audit never blocks an operation that does not need its record.
- **Usage records** say what was used: a meter, a unit and an amount for a customer and a product, and for an organisation when the emitting product knows it. They are CloudEvents. A usage record is never dropped. The operation that produces it fails when the record is not accepted.

Both go into logs of the same shape: a [C2SP `tlog-tiles`](https://c2sp.org/tlog-tiles) log in a blob store, written once, signed per batch, cosigned by witnesses, and sealed per tenant so that a tenant's exit destroys what is personal and keeps what is evidence.

## What audit is not

- **Not a SIEM.** It exports OCSF to one.
- **Not a log pipeline.** Telemetry stays in OpenTelemetry. A record points at a trace and never carries one.
- **Not billing.** It holds no price, plan, balance or invoice. A product builds its bills from the usage log.
- **Not an identity product and not a key store.** Sign-in is [sluis](https://github.com/truvity/sluis) or any other issuer. Keys are atoom's `seal` block.

## The pieces

| Piece | Language | What it does |
|---|---|---|
| Emitter | Go and Rust | A library in each emitting process. Builds one message from one call. |
| Transport | atoom port | Carries messages from emitters to the writer. |
| Writer | Go | Validates, removes duplicates, seals batches into the logs. |
| Witness | Go | Cosigns the writer's checkpoints. Optional. |
| Indexer and reader | Rust | The indexer builds the search index from the log. The reader serves the audit API. |

audit is built on [atoom](https://github.com/truvity/atoom) ports (blob, transport, index, key, source, store) and its `seal` and `state` blocks. [Record 0002](docs/decisions/0002-pieces-and-holders.md) says who holds what.

## Status

This repository starts as design. The code that this design describes runs today inside [sluis](https://github.com/truvity/sluis) (the emitters, the writer, the first indexer and reader, `audit verify`) and in atoom (the blob, transport and index ports and their first drivers). It moves here in steps. Each page and record says what is built and what is planned. The requirement rows in [docs/requirements/audit.yaml](docs/requirements/audit.yaml) carry a status for each rule: `done`, `partial` or `planned`.

## Read next

- [Decision records](docs/decisions/README.md): the design, one subject per record, 0001 to 0016.
- [Reference](docs/reference/README.md): record fields, the log layout, the reader API, the usage event, `audit verify`, configuration kinds, compliance profiles, threats and the glossary.
- [Runbooks](docs/runbooks/README.md): verify a log, investigate a failed verification, place a legal hold, rebuild the index, handle quarantine, recover from a writer or witness outage.
- [Requirements](docs/requirements/README.md): the testable rules.

## Neighbours

| Repository | Relation |
|---|---|
| [atoom](https://github.com/truvity/atoom) | Ports, drivers, blocks and conformance kits that audit is built on. |
| [sluis](https://github.com/truvity/sluis) | The identity product. It emits records, maps its boundary to an audit scope, runs the limit check on the usage counters, and embeds the audit console page. |
| [session](https://github.com/truvity/session) | Browser sessions. It emits security records and never usage records. |
| [audit-v1](https://github.com/truvity/audit-v1) | The archived first audit. History only. |

## Licence

MIT. See [LICENSE](LICENSE).
