# 0006. The usage class: what was used, never dropped

Status: accepted, 2026-11-21. Partly superseded by [0017](0017-the-organisation-of-a-usage-record-is-optional.md): the organisation of a usage record is optional.

## Context

Billing needs exactly-once counting. A lost record under-bills, and a duplicate over-bills. The counted loss that is acceptable for security facts ([0005](0005-the-security-class.md)) is not acceptable for usage.

Billing wants a different record from security: a customer, a meter and an amount, with its own readers, its own retention and its own standard. OCSF has no usage class. Money, plans and balances belong to the product that sells, so audit must hold the evidence of use and nothing of price.

## Decision

### Usage is a class of its own

- **A usage record is never dropped, never buffered in memory and never counted as lost.** The operation that produces it uses the required call: it waits until the record is durable in the transport, and **it fails when the record is not accepted.** The operation did not happen.
- **Every usage record carries an idempotency key,** `source` plus `id`. The writer stores a repeat once, so a retry after an uncertain answer neither loses nor doubles a record.
- **The write fails if the record cannot be stored.** A full spool, an absent transport and a writer that refuses are errors of the operation, and not notes on the next record.
- **The security class keeps its counted loss.** The guarantee differs by class. The pipeline does not split ([below](#one-call-one-message-two-logs)).

### A usage event is a CloudEvents event

A usage fact is a [CloudEvents](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md) 1.0 event in structured mode, as JSON in RFC 8785 form. The attributes are in [reference/usage-event.md](../reference/usage-event.md). In short:

- `id` is `<message id>/<n>`: the security record's `metadata.uid` and the event's position in the usage part, from 0.
- `source` is the emitter's name. `type` is the meter, named by the catalogue.
- `subject` is the aggregation key, never personal data. `time` is the time of use.
- `data` holds the unit, a signed integer amount, the request id where there is one, and the identifiers of the **customer, organisation and product** the use is aggregated by.

**A usage event carries no personal data:** no pseudonym, subject, e-mail address or free text. Caps and counts are per customer, organisation and product, so no consumer needs a person in it.

**Open: the names of the identifier attributes.** The aggregation is per customer, organisation and product. Where in the event each identifier sits is fixed with the first emitter.

### One call, one message, two logs

- **The emitter builds both parts of a message from one call.** The application describes the action once, with its meters. The emitter gives the message one id, used as `metadata.uid` and as the prefix of each usage `id`, and sends one message. No call emits a usage part alone or a security part twice.
- **The catalogue entry declares the meters an action may carry,** with units. The writer refuses an undeclared meter, an amount out of bounds, or an id that is not the record's ([0007](0007-meters.md)).
- **Every usage fact has a security record,** so the two cannot disagree about what happened. A periodic gauge is an audited action of its own.
- **The writer splits the message and registers both logs' sealed batches in one conditional write of its state.** A crash leaves both or neither ([0008](0008-the-log.md)). A writer outage stops both together, and a malformed usage part fails its whole message, security part included.
- **Only the emitter library may build a usage part.** There is no usage API that emits alone.

### The usage log

- **Usage is a separate log kind,** written by the project's writer, with its own prefix, checkpoints, witnesses, lock mode, readers and retention. It has no key of its own: the writer's one log key signs both kinds, and each signed checkpoint names its log.
- **Entries are sealed under the tenant's `evidence` key,** like security entries ([0011](0011-evidence-encryption-and-pseudonyms.md)). Usage outlives the tenant's personal data, so a bill can be disputed after the tenant has left, and the exit still destroys everything personal.
- **The usage log has no search index.** It is read through the reader's get and export by those whose binding grants `usage` ([0012](0012-search-and-the-reader-api.md), [0013](0013-access.md)).
- **Each log declares its own retention,** with no default in production, so neither period binds the other ([0010](0010-lock-modes-retention-and-compliance-floors.md)).
- **A product builds its billing log from the usage log, outside audit.** It keeps its own calendar, grace period and close job in the `billing/` prefix of its bucket or elsewhere. No piece of audit writes that prefix.

### What stays outside

- **audit holds no plan, price, money or balance.** The product keeps them.
- **The limit check stays with the product that runs it.** sluis compares a counter of usage per customer, organisation and product with a limit that the product writes, and refuses at the limit on the request path. The counters, their period and the check are sluis's ([sluis records](https://github.com/truvity/sluis/blob/main/docs/decisions/README.md), the usage and limit record 0046). audit supplies the usage records that the counters are made from, and nothing else.

### Built and planned

Planned: the usage part of the emitter, the usage log and its tests. The records of the security class, the writer and its state are built; the writer's registration of two kinds in one write is part of the same plan. See the requirement rows with status `planned` for the usage rules.

## Consequences

- A full spool now stops some operations that it used to let through: the ones that produce usage. This is the price of exactly-once counting. The operator sees an error and not a silent gap.
- The emitter's interface separates the two classes, and a test shows that a security fact is lost and counted while a usage record is refused.
- The writer already removes repeats by record id. The idempotency key is the same promise made for usage.
- Usage needs a second set of checkpoints to witness and a second retention to declare.
- Billing and limits read a stream made for them and never parse security records.

## Roads not taken

- **Counted loss for usage too.** A bill would be wrong by an amount nobody can reconstruct.
- **A buffer in memory for usage.** A restart would lose it.
- **Usage in the security log, or derived from it by the writer.** One log, but billing in a security schema, one retention for both, and a writer parsing records for amounts that are often not there.
- **A separate usage writer, or a usage API that emits alone.** A dual write, and a second path that can emit twice or without a security record.
- **A pseudonym in the usage fact.** No consumer needs it, and it would put personal data in the evidence.
- **The balance in audit.** Money-adjacent state in an audit product, and billing correctness would still depend on the product's own log.
- **No limit check, usage only.** A limit would be enforced after the fact, and a burst would pass. The check is a product's, not audit's.
