# Enable usage billing

## Purpose

Keep a metering copy of each billable record, so that rollups and a monthly statement can be
computed from the same records that make the trail evidence. How it works and why:
[billing](../explanation/billing.md).

## Preconditions

- A metering framework profile composed into a profile of the installation
  (`billing-nl` ships; see [which profiles to compose](../explanation/which-profiles-to-compose.md)).
- The installation keeps seals (the notary runs), because the statement names the seal of the
  archive it was computed from.

## Before you start

- **The values toggle renders nothing yet.** `extensions.billing.enabled` is accepted and checked,
  but the statement job is designed, not built, and the rollups are partly built. The key takes
  `enabled` only: any other key under `extensions.billing` fails the values schema.
- **The chart refuses `extensions.billing.enabled` when no profile composes a metering profile**,
  because the copy the statement is computed from would not exist.
- **Retention cannot be shortened later.** The metering copy keeps seven years under `billing-nl`
  (the Dutch tax administration's retention duty); under Object Lock compliance an object written
  now is there that long.
- **A billable action must be `block`.** If the record cannot be kept, the operation does not
  happen.
- **A meter renamed is a new meter.** The old name keeps its history; the rollups do not migrate.

## Steps

1. **Compose a metering profile and enable the extension.**

   ```yaml
   audit:
     profiles:
       security:
         presets: [security]
       billing:
         presets: [billing-nl]
     extensions:
       billing:
         enabled: true
   ```

   Verify: `helm template` renders; without the `billing` profile it refuses. Roll back: set
   `enabled: false`; the profile's copies already written stay.

2. **Declare the meter in the application's catalogue** and deploy the writer before the emitter
   ([change what a source records](change-what-a-source-records.md)).

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

   Verify: a record of the action lands under `records/billing/<tenant>/` as well as
   `records/security/`.

## Afterwards

- Watch the rollup lag: rollups are written at index time by `audit-observe`, so an indexer that
  is behind is a count that is not counting (`audit.observe.index.deferred`,
  `audit.observe.index.lag`; [repair or rebuild the index](rebuild-the-index.md)).
- A statement is written after the period ends and after the last hour of it is sealed; running
  it earlier names a seal that does not cover the period.
