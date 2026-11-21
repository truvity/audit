# Emitters

The decisions are in [0005](../decisions/0005-the-security-class.md), [0006](../decisions/0006-the-usage-class.md) and [0004](../decisions/0004-the-record-format.md). The emitter is a library. There is one in Go and one in Rust. Both write the same bytes, and the shared vectors keep them so ([action table and catalogue](action-table-and-catalogue.md#conformance-vectors)).

## What one call does

A product describes an action once: the action, the outcome, the tenant, the fields and the meters. The emitter:

1. checks that the action is a row of the table, and refuses it if not;
2. fills the attributes that the class requires, from what it knows;
3. replaces each subject with its pseudonym, asking the `seal` block, before the record leaves the process;
4. adds the note of lost facts, when one is owed ([0005](../decisions/0005-the-security-class.md));
5. validates the record against the schema of its action, and refuses it if it does not validate;
6. builds the canonical bytes (RFC 8785);
7. sets one message id, a UUIDv7, used as `metadata.uid` and as the prefix of each usage event;
8. builds the usage events, only for an outcome that the meter lists ([0007](../decisions/0007-meters.md));
9. hands one message to the transport.

A browser emitter does not validate. The writer is authoritative.

## The three calls

| Call | Waits? | On failure |
|---|---|---|
| Best effort | No | The failure is counted. It is returned to the caller, who logs it. |
| Required | Until the message is durable in the transport | The call returns an error and the operation fails. This call is used for every usage part. |
| Wait, never fail | Asks for the wait | The answer says the fact was not audited. The caller carries on. |

An emit is answered only when the message is durable, whichever call is used.

## What it refuses

| Refused | Reason |
|---|---|
| An unknown action | Not a row of the table |
| A record that does not validate | The schema of its action |
| A meter that no `Meter` document declares, or an amount out of bounds | [0007](../decisions/0007-meters.md) |
| A string with the shape of an e-mail address | [0004](../decisions/0004-the-record-format.md) |
| A value of `src_endpoint.name` that is not a name the transport gave, a node, or a constant | An address must never reach a record |
| The `lost` field in an event from a caller | Only the emitter writes it |

## Bounds

| What | Bound |
|---|---|
| Record | 16 KiB |
| Free text | 1,024 characters |
| Pseudonym | `p1.<YYYYMM>.<22 characters of base64url>` |
| Tenants with a note of lost facts | 256 per emitter |
| Id time | The id's time is the later of the clock and the last id's time, so ids made by one process increase. After a clock spike the kit follows the clock again when the last id is more than a minute ahead of it. |
| Time sent | Within 14 days behind and one hour ahead of the writer's clock, or the writer quarantines it |

## What it exposes

- **The total of lost facts,** over all tenants, so that a component can export it as a metric ([telemetry](telemetry-and-alerts.md)).
- **A warning** when a record that carries a note is delivered: the process, the total and the time of the first, naming no tenant.

## The transport it uses

The emitter talks to atoom's [transport port](https://github.com/truvity/atoom/blob/master/transport/docs/transport-port.md) as the emitter side: it sends `Emit`, and the driver names the emitter from the platform identity and never from a field of the message.

## Built and planned

Built: the Go and Rust emitters with the three calls, the checks, the note and the vectors (in sluis). Planned: the usage part, the neutral name of the product object for products other than sluis, and the move here.
