# Usage event

The decisions are in [0006](../decisions/0006-the-usage-class.md), [0017](../decisions/0017-the-organisation-of-a-usage-record-is-optional.md) and [0007](../decisions/0007-meters.md). A usage event is a [CloudEvents](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md) 1.0 event (1.0.2) in structured mode, as JSON in RFC 8785 form. Its shape follows OpenMeter's usage events: the `type` selects a meter, the `subject` is the aggregation key, and `data` holds the unit, an amount and the request id.

## Attributes

| Attribute | Value |
|---|---|
| `specversion` | `1.0` |
| `id` | `<message id>/<n>`: the security record's `metadata.uid` (UUIDv7) and the event's position in the usage part, from 0 |
| `source` | The emitter: the component's name |
| `type` | The meter, named by its `Meter` document |
| `subject` | The aggregation key: the tenant, or a key of the product's own within the tenant. Never personal data. |
| `time` | The time of use |
| `datacontenttype` | `application/json` |
| `data` | See below |

### `data`

| Member | Holds |
|---|---|
| `unit` | The meter's unit, a lower-case word |
| `amount` | A signed 64-bit integer. A count adds up. A gauge is an absolute sample. |
| `request_id` | The request id, where there is one |
| The identifier of the customer | **Required.** An opaque id, never personal data. |
| The identifier of the product | **Required.** An opaque id, never personal data. |
| The identifier of the organisation | **Optional.** An opaque id, never personal data. Carried when the emitting product knows the organisation, and left out otherwise. |

**Open: the member names of the three identifiers.** They are fixed with the first emitter.

A usage event has no pseudonym, subject, e-mail address or free text.

### The identifiers

| Case | The writer |
|---|---|
| The customer or the product is missing | Refuses the event, and its whole message with it |
| The organisation is missing | Accepts the event |
| The organisation is present and empty | Refuses the event |

An emitter that has no organisation leaves the member out. It sends no empty value and no placeholder. sluis has none to send ([0017](../decisions/0017-the-organisation-of-a-usage-record-is-optional.md)).

**A consumer expects the customer and the product on every event, and the organisation on some.** A report by organisation covers the events that carry one. It does not count the others as an organisation of their own, and a total per customer and product still includes them.

## The idempotency key

The key is `source` plus `id`, as CloudEvents and OpenMeter deduplicate. A bare message id would give two meters of one message one `id`. The writer removes duplicate messages by `metadata.uid`, so a usage fact is written exactly as often as its security record. The write fails if the record cannot be stored.

## What the writer checks

- the id is the record's `metadata.uid`, a slash and the position;
- the meter is declared by the action's catalogue entry, and its unit and kind match;
- the amount is within bounds;
- the customer and the product are present, and the organisation, if present, is not empty;
- no field holds personal data.

A message that fails any of these fails whole, security part included.

## The entry in the usage log

The usage log is a log of its own, with the same layout as the security log ([log layout](log-layout.md)). Its entries have a clear envelope with the id, the time, the tenant and the event `type`, and the event sealed under the tenant's `evidence` key. It has no index.

## The time of a fact

A usage fact carries two times: the time of use (`time`), and the time the writer recorded it (the writer's stamp, never the emitter's).

| Case | The fact counts at |
|---|---|
| The time of use is not earlier than the recorded time by more than 72 hours, and not later by more than 5 minutes | The time of use |
| Otherwise | The recorded time. The reader marks it `time_adjusted`. |

audit closes no period. A reader that assigns facts to periods uses its own calendar.

## Meters

| Property | Rule |
|---|---|
| Name, unit, kind | Fixed from the first release. A change is a new meter. |
| Kind `count` | Amounts add up over any span |
| Kind `gauge` | An absolute sample. The meter declares how samples combine: `avg`, `max` or `last`. |
| Outcomes | The meter declares the outcomes it records. The default is `success`. |
| Retired | A retired meter stays readable, and no release emits it. |

A reader never merges two meters.

## Built and planned

Planned. The writer's state and the log it would write to are built. The usage part of the emitter, the writer's registration of both kinds in one write, and the readers' period rule are not.
