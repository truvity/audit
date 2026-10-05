# Enable usage quotas

## Purpose

Switch on the usage extension (a second consumer counting a tenant's meter into a cache, an
hourly reconciler) in a stream-mode installation. How it works and why:
[usage quotas](../explanation/usage-quotas.md).

## Preconditions

- The installation runs in [stream mode](run-stream-mode.md): without a stream there is nothing
  for a second consumer to read.
- Billing is on, or at least a `meter:` is declared in the catalogue: what is capped is what is
  billed ([enable billing](enable-billing.md)).
- A cache (Valkey or compatible) the application already runs, and a decision point (the gateway's
  external authorisation, or middleware) that is the application's own.

## Before you start

- **The values toggle renders nothing yet.** `extensions.quotas.enabled` is accepted, and the chart
  refuses it without `mode: stream`, but the usage consumer and the reconciler are designed, not
  built. The key takes `enabled` only: a `cache:` or `period:` key under it fails the values schema.
- **This is not burst rate limiting.** "No more than fifty requests a second" has to be decided
  before the request is served and belongs in the gateway's rate limiter.
- **Fail open, and say so.** A cold cache means "this tenant has used nothing"; the decision point
  must serve the customer and record that it decided without a count. The reconciler corrects it
  within the hour.

## Steps

1. **Enable it** in a stream-mode installation.

   ```yaml
   audit:
     mode: stream
     extensions:
       quotas:
         enabled: true
   ```

   Verify: `helm template` renders; with `mode: direct` it refuses. Roll back: `enabled: false`.

2. **Record the denial.** Declare a refusal as an `async` action in the application's catalogue,
   so "you cut me off" has an answer.

## Afterwards

Watch the usage consumer's lag separately from the writer's, the reconciler's drift (a drift that
grows means the consumer is missing records) and denials (a sudden rise is an attack or a
misconfigured plan).
