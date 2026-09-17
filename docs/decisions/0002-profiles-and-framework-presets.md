# 0002. Profiles composed from framework presets, one copy per profile

- Status: accepted
- Date: 2026-09-17

## Context

Several frameworks apply to the same event at once: security operations,
usage billing, tenant-facing history, regulatory evidence, and add-ons such
as PCI DSS, DORA or healthcare access logging. They disagree on which fields
may be kept, how identities are treated, and for how long. A single store
with a single retention over-retains for one purpose and under-retains for
another, and lets a reader with one purpose see fields justified only by
another ([research/regulation.md](../research/regulation.md)).

## Decision

A **preset** is a file under `presets/` describing what one framework
requires: required and forbidden fields, required event categories,
identity treatment per actor category, retention with a hot window,
integrity controls, review cadence, pipeline defaults, and the clauses it
cites, with a disclaimer. Where a framework gives no number the preset
carries a defended default marked configurable.

A **profile** is a deployment's composition of presets plus a destination.
Composition is a union: the strictest identity treatment, the longest
retention, `required` over `recommended`. The validator refuses a profile
that drops a required field and a catalogue whose sources lack a required
category.

The split writer produces **one copy per profile**. Scalar fields are
copied into every profile that keeps them. Large payloads are stored once
by content hash under a payload prefix, retained for the longest
referencing profile, and referenced from the copies. Every copy carries the
event id and the SHA-256 of the original wide record.

Presets reference core fields and classes only, never an application's
property, which keeps them framework-generic.

## Consequences

- Each prefix is one dataset with one purpose, one legal basis, one
  retention and one access role. An auditor or a data protection authority
  is pointed at exactly that prefix.
- After the security retention the actor, addresses and user agent are
  gone. The billing copy still holds tenant, time, action and quantity.
- Duplication multiplies a small core by at most the number of profiles.
  At the volumes surveyed that is tens of dollars a month.
- Enabling a preset per deployment is a versioned configuration change and
  emits `audit.profile.changed`.
- Applications never learn about profiles beyond tagging their actions.

## Alternatives considered

- **Store each field once, grouped by longest-retaining profile, join at
  read time.** No duplication, but joins that object storage cannot do,
  more complex exporters, and purpose bleed on shared groups. Revisit only
  at two orders of magnitude more volume.
- **Feature flags per field at the emitter.** Silent toggles defeat the
  requirement that starting, stopping or altering logging is itself
  logged, and a flag can drop a field a framework requires.
