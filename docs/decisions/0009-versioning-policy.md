# 0009. Versioning: package per major, major.minor on the record, decoders forever

- Status: accepted
- Date: 2026-09-17

## Context

Records are locked for up to seven years or longer. Whatever reads them in
year seven must understand year one. The schema must still be allowed to
change, including incompatibly.

## Decision

- The proto package carries the major version: `audit.v1`, then
  `audit.v2`. A breaking change is a new package.
- Every record carries `schema_version` as `major.minor`. The compatibility
  contract is equal on major, greater-or-equal on minor: a reader built for
  1.3 reads 1.0 through 1.x and refuses 2.0.
- `buf breaking` runs in CI against the last tag, so additive changes stay
  additive by force.
- Readers keep a decoder for every major that was ever written under lock.
  Removing one is a decision that must show no object of that major
  remains.
- Catalogues carry their own version on the record (`catalogue_version`);
  per-action payloads version inside the catalogue. Presets, profiles and
  the digest format version independently.
- Every schema and catalogue version is copied into the archive on first
  use, so the archive is self-describing without this repository.

## Consequences

- Majors are rare. Additive changes plus the `unmapped` bag are the norm.
- The archive copy of schemas is part of the retention, under the same
  lock as the records that use it.

## Alternatives considered

- **Semantic versioning on the package name only.** Loses the per-record
  minor that lets a reader refuse what it cannot understand.
- **Schema evolution by migration.** Locked objects cannot be rewritten;
  migration is impossible by design.
