# 0001. Record schema in Protocol Buffers with JSON Schema extension slots

- Status: accepted
- Date: 2026-09-17

## Context

The record has two audiences. Compiled components (emitter libraries, the
writer, the query service, the viewer) want generated types, breaking-change
detection and a binary encoding on the queue. Generic components (the split
writer's field routing, the indexer's facet columns, the viewer's detail
panel, the exporters) must understand an application's own data at runtime
without being recompiled. Existing schema standards were surveyed
([research/standards.md](../research/standards.md)): OCSF is the right
export target but too heavy to author in; ECS is frozen since its donation
to OpenTelemetry; OpenTelemetry has no audit convention; CloudEvents is an
envelope, not a record; CADF is dormant but its initiator, action, target,
outcome and observer model is the cleanest.

## Decision

The core record is described in Protocol Buffers
(`proto/audit/v1/record.proto`) and is the schema of record. Its shape is
the vendor audit-log spine three products converged on: action in
`resource.verb` form plus a coarse operation, typed actor, targets, outcome,
context, bounded attributes, schema version. A JSON Schema of the core is
generated from the proto and published.

Predefined **extension slots** (`data`, `targets[].attributes`,
`actor.attributes`, `context.areas.<area>`, `meter.dimensions`) are
described in JSON Schema by the registering source. Extension schemas must
validate against `schemas/extension.schema.json`: closed objects, bounded
depth, every property annotated with `x-audit-class` and `x-audit-pii`,
optional facet, filter, sensitivity and export-path annotations.

The composed schema per action, core plus extensions, is what emitters
validate against and what is copied into the archive next to the records.

JSON encoding uses proto field names (snake_case) on output and accepts both
spellings on input. Timestamps are RFC 3339 in UTC.

## Consequences

- Generic components read annotations, never application code.
- Two description languages coexist, with a test corpus that must parse as
  proto and validate as JSON Schema in the same test.
- A property that recurs across sources is promoted to the core in the
  next minor. Extensions are where fields prove themselves.
- The annotation vocabulary is tiny and versioned; adding to it is a
  decision.
- OCSF, ECS and OpenTelemetry are exporters with per-property mappings.

## Alternatives considered

- **OCSF as the record.** Best vocabulary and the SIEM target, but eighty
  attributes on the authentication class alone and enum churn per release.
  Kept as an exporter.
- **ECS as the record.** Flat dotted keys index well, but the design is
  frozen and it has no target object beyond `user.target`. Its outcome and
  category vocabulary is borrowed.
- **JSON Schema for the core too.** One language, but no generated types
  for the compiled components, no equivalent of `buf breaking`, and a
  larger message on the queue.
- **One free-form `data` blob.** Simplest, and exactly how a wide record
  becomes a dumping ground for content and secrets. The slot constraints
  are the guardrail.
