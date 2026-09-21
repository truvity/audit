# Billing

Usage billing is a projection of records the installation already keeps. A
billable action is an audit record that happens to carry a quantity, so the
same completeness, the same deduplication and the same lock that make the
trail evidence make the invoice defensible.

Nothing here is in the request path.

## The five slots

| # | slot | what the application adds | state |
|---|---|---|---|
| 1 | **catalogue** | on each billable action: `meter: {name, quantity_path, outcomes: [success]}` and `profiles: [security, billing]` | built |
| 2 | **emit** | nothing — the record carries `meter{name, quantity, unit}` | built |
| 3 | **writer** | nothing — it splits a `billing` copy (tenant and meter, no actor, the metering profile's retention), indexes the meter fields, and the dedupe table makes it exactly-once | built |
| 4 | **rollups and statement** | a rollup row per tenant, meter and hour, filled at index time; a monthly CronJob writes an immutable statement object naming the digest it was computed from | rollups partly built; the statement not built |
| 5 | **export** | push the statement's totals to a billing system, or invoice from the statement object | the application's |

## The rules that make it defensible

- **A billable action is `block`.** If the record cannot be kept, the
  operation does not happen. An invoice line that exists without a record,
  or a record that exists without the operation, is the failure mode worth
  paying a round trip to avoid.
- **The meter counts only the outcomes it declares.** A refused call is not
  billable, and that is a property of the catalogue, not of a query someone
  wrote later.
- **Exactly once.** The dedupe table absorbs a redelivery, so a writer
  restart or a stream redelivery cannot double a customer's bill.
- **The statement is immutable and self-describing.** It names the period,
  the rollups it summed and the digest of the archive it was computed from,
  and it is written into the archive under the metering profile's lock. A
  dispute six years later is answered by re-reading it, and by verifying the
  chain it names.
- **The billing copy holds no people.** The metering profile omits actor and
  subject: quantities per tenant and meter, which is what finance, a
  customer in a dispute and a tax inspector are entitled to see. Who did it
  is in the security copy, under the security profile's retention.

## Turning it on

Compose a metering profile and enable the extension:

```yaml
audit:
  profiles:
    security:
      presets: [security]
    billing:
      presets: [billing-nl]
  extensions:
    billing:
      enabled: true               # not built yet: renders the statement job
```

The chart refuses `extensions.billing.enabled` when no profile composes a
metering preset, because the copy the statement is computed from would not
exist.

Then, in the application's catalogue:

```yaml
actions:
  app.credential.verified:
    summary: A credential was verified for a tenant.
    operation: execute
    categories: [data_access]
    profiles: [security, billing]
    delivery: block
    meter:
      name: verifications
      outcomes: [success]
```

## What to watch

- **The rollup lag**: rollups are written at index time, so a writer that
  cannot index is also a writer that is not counting. The runbook's
  `audit.writer.index.deferred` counter is the one to alert on.
- **The monthly close**: a statement is written after the period ends and
  after the last hour of it is sealed by the digest job. Running it earlier
  produces a statement that names a digest that does not cover the period.
- **A meter renamed** is a new meter. The old name keeps its history; the
  rollups do not migrate.

Retention for the metering copy is the metering preset's — seven years under
`billing-nl`, for the Dutch tax administration's retention duty. See
[which presets a deployment composes](../../operations/presets-policy.md).
