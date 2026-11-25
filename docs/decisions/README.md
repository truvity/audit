# Decision records

These records are the design of audit. Each one stands alone: Status and date, Context, Decision, Consequences, Roads not taken. They are written in reading order, one subject each.

## The rule: a clean rewrite once, then append-only

The records 0001 to 0016 are **the first set**. They were written as one clean rewrite, from the design that grew inside sluis and from the archived audit-v1. Nothing in them is an amendment of an earlier text, and none has a history in this repository, because this repository began with them.

**From this set on, records are append-only.** An accepted record is not rewritten. To change a decision, write a new record with the next number that supersedes the old one, and move the old record to `archive/` with a pointer to its successor. A typo or a broken link may be fixed in place. A record that a later one supersedes only in part stays where it is, and its Status line changes (see [Status values](#status-values)). The same rule is in [CONTRIBUTING.md](../../CONTRIBUTING.md) and [AGENTS.md](../../AGENTS.md).

## Status values

The Status line of a record is one of:

- `accepted, <date>`
- `accepted, <date>. Partly superseded by [NNNN](NNNN-slug.md): <what changed>`: a later record changed one part of it. The record stays in place and is otherwise unchanged. The successor says in its own text which words it overtakes.
- `superseded by [NNNN](NNNN-slug.md)`: the whole record is replaced. It moves to `archive/`.

The successor of a partly superseded record has a Status line that says `Partly supersedes [NNNN](NNNN-slug.md)`.

## The records

| No. | Record | Decides |
|---|---|---|
| [0001](0001-purpose-scope-and-neighbours.md) | Purpose, scope and neighbours | What audit is and is not, and how it relates to atoom, sluis and session |
| [0002](0002-pieces-and-holders.md) | The pieces, the transport and who holds what | Emitter, writer, witness, indexer, reader. One audit per scope, a project per emitting unit. The transport and its drivers. |
| [0003](0003-the-neutral-model.md) | The neutral model | Scope, project, log kind, log, tenant, product, component |
| [0004](0004-the-record-format.md) | The record format | OCSF, RFC 8785, integer bounds, the product object, the catalogue |
| [0005](0005-the-security-class.md) | The security class | Three ways to emit, counted loss, no blocking it does not need |
| [0006](0006-the-usage-class.md) | The usage class | Never dropped, idempotency key, CloudEvents, one call two logs |
| [0007](0007-meters.md) | Meters | Name, unit, kind, outcomes, immutability, the time of an event |
| [0008](0008-the-log.md) | The log | tlog-tiles, the fence, checkpoints, sealing, the writer's state, duplicates |
| [0009](0009-witnesses-and-verification.md) | Witnesses and verification | Cosignatures, what a verifier checks, what a chain proves |
| [0010](0010-lock-modes-retention-and-compliance-floors.md) | Lock modes, retention and compliance floors | Locks, retention, legal holds, tiers, profiles |
| [0011](0011-evidence-encryption-and-pseudonyms.md) | Evidence encryption and pseudonyms | Through atoom's `seal` block; crypto-shredding |
| [0012](0012-search-and-the-reader-api.md) | Search and the reader API | The index, its drivers, the reader's calls |
| [0013](0013-access.md) | Access | Cedar as a library with audit's own schema, two checks |
| [0014](0014-the-front-the-console-page-and-audit-verify.md) | The front, the console page and `audit verify` | What audit owns at the edge |
| [0015](0015-configuration-kinds.md) | Configuration kinds | AuditLog, AuditWitnesses, AuditCatalogue, Meter, through atoom's `state` registry |
| [0016](0016-distribution-and-the-lambda-shape.md) | Distribution and the Lambda shape | Forms per piece, every function behind the Web Adapter |
| [0017](0017-the-organisation-of-a-usage-record-is-optional.md) | The organisation of a usage record is optional | A usage record carries the customer and the product, and the organisation when the emitter knows it. Partly supersedes 0006. |
| [0018](0018-the-pseudonym-domain-label-is-the-products.md) | The pseudonym domain label is the product's | The label is product configuration with no default; sluis keeps `sluis-pseudonym-v1`; revisit when a second product names people. Partly supersedes 0011. |
| [0019](0019-floors-are-a-declared-switch.md) | The floors are a declared switch | Each log declares `floors: enforce \| report`, `enforce` when absent; no scope or installation name is a switch. Partly supersedes 0010 and 0006. |

## Records changed by a later record

| Record | Changed by | What changed |
|---|---|---|
| [0006](0006-the-usage-class.md) The usage class | [0017](0017-the-organisation-of-a-usage-record-is-optional.md) | The organisation is optional in a usage record. The customer and the product stay required. |
| [0006](0006-the-usage-class.md) The usage class | [0019](0019-floors-are-a-declared-switch.md) | A log's retention has no default anywhere ("in production" is gone). |
| [0010](0010-lock-modes-retention-and-compliance-floors.md) Lock modes, retention and compliance floors | [0019](0019-floors-are-a-declared-switch.md) | The floors apply by a log's declared `floors`, `enforce` (refuse) or `report` (warn), `enforce` when absent; "in a production scope" is gone. |
| [0011](0011-evidence-encryption-and-pseudonyms.md) Evidence encryption and pseudonyms | [0018](0018-the-pseudonym-domain-label-is-the-products.md) | The open point on the domain label is decided: per product, configured by the product, no default. |

## What each record replaces

There is no history to archive here. This table says where each decision came from, so that a reader of the older records finds its successor. A record of sluis that moved here keeps its number in sluis, with a "moved to" note.

### From sluis

| sluis record | Subject | Now |
|---|---|---|
| 0028 Audit pieces | Four pieces, transport, search, access, the front | [0002](0002-pieces-and-holders.md) (pieces, transport), [0012](0012-search-and-the-reader-api.md) (search, index, reader), [0013](0013-access.md) (access, audit alone), [0014](0014-the-front-the-console-page-and-audit-verify.md) (front) |
| 0029 The audit log | Projects, tiled log, sealing, counted loss, fence, witnesses, locks, encryption, pseudonyms, retention, tiers, profiles | [0003](0003-the-neutral-model.md), [0005](0005-the-security-class.md) (counted loss), [0008](0008-the-log.md), [0009](0009-witnesses-and-verification.md), [0010](0010-lock-modes-retention-and-compliance-floors.md), [0011](0011-evidence-encryption-and-pseudonyms.md) |
| 0030 Audit records and the usage stream | OCSF record, `unmapped.sluis`, catalogue, class mapping, export, usage stream | [0004](0004-the-record-format.md), [0006](0006-the-usage-class.md), [0012](0012-search-and-the-reader-api.md) (export); the class mapping of sluis's actions stays in sluis as its action table |
| 0040 Meters | Meters, outcomes, immutability, the period | [0007](0007-meters.md) |
| 0046 Usage records | The usage class, and the limit check | The usage class is [0006](0006-the-usage-class.md). The limit check and its counters stay in sluis. |
| 0039 Quota allowance | The allowance engine | Superseded in sluis, not carried here. audit holds no balance. |

### From audit-v1 (archived)

audit-v1's records are history, in the [archived repository](https://github.com/truvity/audit-v1/tree/master/docs/decisions). What is still valid was carried over, and the rest was left.

| audit-v1 record | Carried over as | Left because |
|---|---|---|
| 0001 Record schema, Protobuf with JSON Schema slots | The no-float rule, the bounds and the bounded free text ([0004](0004-the-record-format.md)) | The record format is OCSF now |
| 0002 Profiles and framework presets | The clause-cited presets as floors ([0010](0010-lock-modes-retention-and-compliance-floors.md), [compliance profiles](../reference/compliance-profiles.md)) | A profile no longer makes a copy of the records |
| 0003 S3 Object Lock as the record | Lock modes ([0010](0010-lock-modes-retention-and-compliance-floors.md)) | The tiled log is the record |
| 0004 Sink interface and transports | The transport ([0002](0002-pieces-and-holders.md)) | The sink is atoom's transport port |
| 0005 Identity tiers and pseudonymisation | Pseudonyms ([0011](0011-evidence-encryption-and-pseudonyms.md)) | Tiers and the key directory are not carried over |
| 0006 Search contract | The filter grammar, cursors, facets, get, export and the tail ([0012](0012-search-and-the-reader-api.md), [reader API](../reference/reader-api.md)) | |
| 0007 Authentication and authorization plug points | Access ([0013](0013-access.md)) | Plug points are Cedar and the token source now |
| 0008 Digest chain and verification | "What a chain proves depends on who holds the key" ([0009](0009-witnesses-and-verification.md)) | The digest chain is replaced by signed checkpoints |
| 0009 Versioning policy | | Replaced by the release rules of [0016](0016-distribution-and-the-lambda-shape.md) |
| 0010 Key providers | | The `seal` block and the key port |
| 0011 One installation per service or product | | One audit per scope ([0002](0002-pieces-and-holders.md)) |
| 0012 Two deliveries and a durable ack | The durable acknowledgement ([0002](0002-pieces-and-holders.md), [0008](0008-the-log.md)) | |
| 0013 No pseudonymisation keys by default | | A person is always a pseudonym here |
| 0014 Lock modes and store tiers | Lock modes and tiers ([0010](0010-lock-modes-retention-and-compliance-floors.md)) | |
| 0015 Schema ids on GitHub Pages | | Schemas are published with each release ([0004](0004-the-record-format.md)) |
| 0016 Three parts installed independently | | The pieces ([0002](0002-pieces-and-holders.md)) |
| 0017 Sink durability and transports | The transport rules ([0002](0002-pieces-and-holders.md)) | |
| 0018 v1 bucket layout | | The log layout is `tlog-tiles` ([reference/log-layout.md](../reference/log-layout.md)) |
| 0019 Seals | | Replaced by checkpoints and witnesses ([0008](0008-the-log.md), [0009](0009-witnesses-and-verification.md)) |
| 0020 Observe follows the bucket | | The indexer follows checkpoints ([0012](0012-search-and-the-reader-api.md)) |
| 0021 One validated configuration file | | Configuration kinds ([0015](0015-configuration-kinds.md)) |
| 0022 Contracts, proto and Connect | | Ports are atoom's ConnectRPC contracts |
| 0023 Archive retention and lifecycle | Retention and tiers ([0010](0010-lock-modes-retention-and-compliance-floors.md)) | |
| 0024 Indexer and query are separate processes | The split ([0002](0002-pieces-and-holders.md), [0012](0012-search-and-the-reader-api.md)) | |
| 0025 Configuration is immutable per instance | | Signed configuration that reloads ([0015](0015-configuration-kinds.md)) |

Not carried over at all: audit-v1's bucket layout, its digest chain and seals, one installation per application, key tiers, the Pulumi library and the chart.

## Archive

`archive/` holds records that a later record superseded. It is empty: the first set has not been superseded yet.
