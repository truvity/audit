# Metering

One stream, two projections, two locked prefixes.

This projection reads only what the write path already produces: the billing
profile's copies, the outcome rule on metered actions, and pseudonyms that
differ per purpose. It is what validates the design's central claim, that two
projections of one stream cannot be joined on a person, and so it is built
before the first adopter rather than after.

## Fields

Billable actions carry `meter` with `name`, `quantity` (decimal string),
`unit`, `kind` (count or gauge) and flat `dimensions`. The billing profile
keeps `id`, `occurred_at`, `recorded_at`, `source`, `action`, `tenant_id`,
`meter` and `origin_hash`, and nothing about people.

## Which records count

The billing copy carries the outcome, because the first thing a customer
disputing an invoice asks is whether the call succeeded. The catalogue says
which outcomes a metered action counts, and the default is success only: a
refused call is not a billable one. An action that bills attempts, say a
verification that is charged whether or not it passes, lists the outcomes it
counts. The rollup reads that from the catalogue and never guesses.

## Counts and gauges

Counts are events. Stored bytes and similar levels are **absolute samples**
emitted on a schedule (hourly) and billed as the average over the period,
the way object storage and CI providers meter storage. Never deltas.

## Projection

- `usage_dedup(id, source, seen_at)` with the preset's dedupe window.
- `usage_hourly(tenant_id, meter, hour, quantity, event_count, last_event_id)`
  upserted idempotently.
- `usage_statement(tenant_id, meter, period, quantity, event_id_range,
  digest_key, computed_at)` written once at period close and never
  updated. The statement plus the locked billing copies are the seven-year
  administration.

## Period close

The period closes `close_after_hours` after its end (default 72). Later
events are flagged, not billed; corrections are credit notes, never
rewrites. Reprocessing replays the period's billing copies into a fresh
rollup and diffs against the statement; the diff is kept as the
reconciliation.

## Rating

The rating engine (a payment provider's meters, or an in-house one) receives
rollups, not raw events, in pre-aggregated mode with an idempotency key per
tenant, meter and hour. It is never the evidence store.
