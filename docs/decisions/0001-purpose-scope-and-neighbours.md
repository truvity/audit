# 0001. Purpose, scope and neighbours

Status: accepted, 2026-11-21

## Context

Several products need an audit trail: an identity product, a session service, a certificate tool, and any product that sells something by use. Each of them could write its own log. Then each would have to solve the same hard parts again: a format that others can read, a log that cannot be altered without notice, a way to destroy personal data and keep evidence, and a way to count usage without losing a record.

The first audit product, [audit-v1](https://github.com/truvity/audit-v1), solved this for one application at a time. A second design grew inside [sluis](https://github.com/truvity/sluis), as a part of the identity product. Then the shared parts moved to [atoom](https://github.com/truvity/atoom), and audit became a product of its own. This repository is that product.

## Decision

- **audit is a product that other products use.** It takes records from emitters, seals them into logs that auditors can verify, and lets authorised readers search and read them.
- **audit keeps two classes of record in two kinds of log.** The [security class](0005-the-security-class.md) is for what happened, and loss is counted and never blocks. The [usage class](0006-the-usage-class.md) is for what was used, and it is never dropped.
- **audit is built on atoom.** The store of blobs, the transport, the index, the keys, the configuration source and the policy loader are atoom ports and blocks. audit owns the model: the record format, the logs, the classes, the pieces and the rules that make them an audit.
- **audit works without sluis.** Any product can emit records, and any issuer can sign in the readers. Where audit runs beside sluis, sluis maps its boundary to an audit scope ([0003](0003-the-neutral-model.md)).

### What audit is not

- **Not a SIEM.** It exports OCSF events ([0004](0004-the-record-format.md)) to one.
- **Not a log pipeline.** Telemetry stays in OpenTelemetry. A record points at a trace by id and never carries one.
- **Not billing.** audit holds no price, plan, balance or invoice. The product builds its billing from the usage log ([0006](0006-the-usage-class.md)).
- **Not a limit check.** A check that compares a counter with a limit on a request path belongs to the product that does it. sluis runs one on its own counters, and it stays in sluis.
- **Not an identity product, and not a key store.** audit trusts a token source that its configuration names, and it uses keys through atoom's `seal` block.

### Neighbours

| Neighbour | Relation |
|---|---|
| atoom | audit is a consumer. The ports are the [blob](https://github.com/truvity/atoom/blob/master/blob/docs/blob-port.md), [transport](https://github.com/truvity/atoom/blob/master/transport/docs/transport-port.md) and [index](https://github.com/truvity/atoom/blob/master/index/docs/index-port.md) ports, the key, source and store ports, and the blocks `seal` and `state`. The records of atoom that matter here are [0008 blocks](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md), [0009 in-process ConnectRPC](https://github.com/truvity/atoom/blob/master/docs/decisions/0009-connectrpc-in-process.md), [0010 distribution](https://github.com/truvity/atoom/blob/master/docs/decisions/0010-distribution-per-driver-and-lambda.md), [0011 Cedar](https://github.com/truvity/atoom/blob/master/docs/decisions/0011-cedar-as-a-library.md) and [0012 durability](https://github.com/truvity/atoom/blob/master/docs/decisions/0012-store-durability.md). |
| sluis | An emitter, and the host of the console that embeds audit's page ([0014](0014-the-front-the-console-page-and-audit-verify.md)). It keeps the limit check and the grant vocabulary. |
| session | An emitter of security records. It never emits usage records. Its records go to the audit writer of its environment. |
| Other products | Emitters, in Go or Rust, and optionally readers. |
| The group's architecture | Decides where audit runs and who runs it. This repository does not. |

### Status of the work

The code that this design describes is built, in part, in sluis (emitters, writer, first indexer and reader, `audit verify`) and in atoom (the blob, transport and index ports, and their `fs`, `fsreadonly`, `spool` and `sqlite` drivers). It moves here in steps. Each record says what is built and what is planned.

## Consequences

- A product adopts audit by linking an emitter, declaring its actions in a table, and running or joining a writer. It does not write a log format.
- audit's size stays small, because ports and blocks are not rewritten here.
- Moving code from sluis is work, and until it is done the design here runs ahead of the repository. The requirement rows say how far.

## Roads not taken

- **Keep audit as a part of sluis.** Every product that wants an audit trail would depend on an identity product.
- **Keep audit-v1's design.** One installation per application, a bucket layout of its own, hourly seals built by listing the bucket, and key tiers. The tiled log with signed checkpoints and witnesses is standard and verifiable by generic tools.
- **Make audit also a billing product.** A price and a balance are decisions of the product that sells; audit would grow a second domain.
- **Make audit an atoom block.** A block moves into atoom at its second consumer, and audit is a product with pieces that run as processes. atoom's [0013](https://github.com/truvity/atoom/blob/master/docs/decisions/0013-what-moves-from-sluis.md) records that audit is not a block.
