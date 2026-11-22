# 0017. The organisation of a usage record is optional

Status: accepted, 2026-11-22. Partly supersedes [0006](0006-the-usage-class.md).

## Context

[0006](0006-the-usage-class.md) says that a usage event carries the identifiers of the customer, the organisation and the product that the use is aggregated by. That makes all three required.

An emitter does not always know an organisation. sluis never uses or looks one up: a customer's identity token carries none, and the organisations of a customer are owned by the products, which still send them. sluis's limit counter is keyed by the customer and the product ([sluis record 0053](https://github.com/truvity/sluis/blob/main/docs/decisions/0053-the-limit-counter-is-keyed-by-customer-and-product-and-sluis-never-uses-the-organisation.md)), so the usage records from which its counters are made name the customer and the product only. A rule that requires an organisation would make sluis invent one, or make the writer refuse every usage record of sluis.

## Decision

### The identifiers of a usage record

- **A usage record MUST carry the customer and the product.** They are the aggregation key that every consumer can rely on.
- **A usage record carries the organisation when the emitting product knows it.** The organisation is optional. An emitter that has none leaves it out. It does not send an empty value, a placeholder or a guess.
- **The three identifiers are opaque ids and never personal data,** as before ([0006](0006-the-usage-class.md)).

### What the writer checks

- A usage record **without the customer or without the product is refused,** and its whole message is refused with it ([0006](0006-the-usage-class.md)).
- A usage record **without an organisation is valid.**
- A usage record whose organisation is present but empty is refused. Absent is valid. Empty is not.
- The idempotency key, the meters and the time of a fact are unchanged. The organisation is not part of the key.

### What a consumer of the usage stream expects

- **The customer and the product are always present,** so a total per customer, per product, or per customer and product counts every record.
- **The organisation is sometimes absent.** A record from sluis never carries one. A record from a product carries one when that product knows it.
- **A consumer that groups by organisation covers only the records that carry one.** It does not treat the others as an organisation called "unknown" or "none", and it does not drop them from the totals per customer and product. It reports the records without an organisation apart, or leaves them out of the grouping and says so.
- **The organisation does not come from sluis.** A consumer that needs usage per organisation takes it from the products' records, and a limit per organisation is the product's own ([sluis record 0053](https://github.com/truvity/sluis/blob/main/docs/decisions/0053-the-limit-counter-is-keyed-by-customer-and-product-and-sluis-never-uses-the-organisation.md)).

### What this supersedes

[0006](0006-the-usage-class.md), in part: the words that make the organisation one of the three identifiers every usage event carries, and the same words about the counters of the limit check, which are keyed by the customer and the product. Everything else in 0006 stands, including its open question: **Open: the names of the identifier attributes.** The names of the members are fixed with the first emitter, and this record says only which of them may be absent.

### Built and planned

Planned, as the rest of the usage class. The usage part of the emitter and the writer's check of it are not built. The requirement rows `REQ-AUDIT-193` to `REQ-AUDIT-195` carry the rule, and the [usage event reference](../reference/usage-event.md) shows the shape.

## Consequences

- sluis writes usage records that are valid, without an organisation and without a placeholder.
- A product that sells or limits by organisation keeps sending it, and its records are groupable by organisation.
- The usage stream is not uniform. A consumer must handle a missing organisation, and a report by organisation is partial for a customer that uses a mix of emitters.
- The writer's check has one more case to test: a record without the customer or the product is refused, and one without an organisation is stored.

## Roads not taken

- **Keep the organisation required, and let sluis send a placeholder.** A made-up value cannot be told from a real organisation, so it would pollute every grouping by organisation.
- **Make sluis look the organisation up.** It would put a call to another service on the path of a request. sluis decided against it in its record 0053, and the relationship between a customer and its organisations is the products'.
- **Drop the organisation from the usage class.** The products that know it need it for their own caps and bills by organisation.
- **Require the organisation per emitter or per meter.** The writer's check would then depend on a configuration of each emitter, and the optional attribute already says what each emitter knows.
