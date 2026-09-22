# Why

Organisations end up with several audit logs that disagree. One product
writes an activity table in its database. Another writes lines to a log
pipeline that samples. A billing system counts requests in its own store.
An identity provider keeps events for thirty days. When an auditor asks who
did what, or a customer disputes an invoice, or a regulator asks for
evidence, each answer comes from a different place with a different shape,
a different retention and a different idea of who the actor was.

This component exists so that there is one answer.

## What it must be

- **Complete.** A record that was accepted is never lost. Privileged and
  billable actions do not complete until the record is durable, and an
  action that is not worth keeping does not belong in the catalogue.
- **Immutable and provable.** Copies live under S3 Object Lock in
  compliance mode where a framework demands it, and under the chain alone
  where none does. An hourly signed digest chains every object to the
  previous digest. A `verify` command lets an auditor check the chain
  without trusting the operator.
- **Purpose-bound.** The same event is kept once per purpose, each copy with
  the fields, identity treatment and retention that purpose justifies.
  Security keeps client addresses for a year. Billing keeps quantities for
  seven years and no people. Evidence keeps lifecycle facts for seven years
  after expiry. A tenant's history keeps sentences and diffs for as long as
  the product promises.
- **Owned by the application it records.** One installation belongs to one
  application, in its namespace, rendered by its chart
  ([0011](decisions/0011-one-installation-per-service-or-product.md)). No
  team waits on another team's audit service to ship, and no application
  holds the archive's credentials.
- **Extensible without forks.** Applications add their own data through
  predefined slots described in JSON Schema, register an action catalogue,
  and every generic component understands them at runtime.
- **Searchable.** Faceted search with typed predicates, cursor pagination,
  a tail cursor for pulls, exports, and a viewer.
- **Metered.** The same records feed usage-based billing, with idempotent
  projections and immutable monthly statements.

## What it is not

- **Not an application log pipeline.** Logs are for operators debugging a
  system; they may be sampled, rotated, and dropped under pressure. Audit
  records are facts with a legal life.
- **Not a SIEM.** It exports to one, in OCSF, ECS or OpenTelemetry form.
- **Not the wallet's transaction log.** A digital-identity wallet keeps its
  own log of presentations on the user's device, and the wallet provider
  must not be able to read it. This component never carries presentation
  contents, attribute values or identifiers that would let transactions be
  linked. It sees operational events, whose subjects are the identifiers the
  application itself minted, and nothing else.
- **Not a ledger database.** Object storage is the record; every database
  is a rebuildable projection.

## Who reads it

A security analyst during an incident. A tenant administrator wondering who
removed a colleague. A finance controller reconciling a month. A customer
disputing an invoice. An internal auditor collecting ISMS evidence. A
conformity assessment body under eIDAS. A tax inspector seven years later.
Each of them reads a different profile of the same records.
