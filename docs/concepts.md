# Concepts

## Record

One thing that happened, as seen by one source. Described in
[record.proto](../proto/audit/v1/record.proto). The spine is: `id`,
`occurred_at`, `recorded_at`, `source`, `action`, `operation`, `outcome`,
`tenant_id`, `actor`, `targets`, `context`. Around the spine: `subject`,
`capture`, `previous_attributes`, `data`, `meter`, `attributes`, `unmapped`.

Which fields a copy carries is decided by the profile it is written under.
For **core fields**, presets name what must, may and may never be kept, and a
field no preset names is dropped. For **extension properties**, the schema
annotates each with a **class** (shared, audit, metering, history or
evidence) and a PII level, and a profile keeps the classes its presets keep.
The record itself does not say which copies carry a core field;
`audit profile explain <name>` prints what a profile keeps.

The record carries **identifiers, never identity attributes**. Names and
e-mail addresses are resolved at read time by whoever may see them.

## Action and operation

`action` is fine-grained and namespaced by source: `wallet.credential.issued`,
`roster.session.revoked`. `operation` is one of seven coarse values:
create, access, modify, remove, authentication, transfer, restore. Both
appear on every record so a reader can filter broadly or precisely.

## Catalogue

Per source, the list of its actions and what each carries: operation,
framework categories, profiles, capture level, delivery mode, target types,
the schema of its `data`, a sentence template per locale, and an optional
meter. Also the source's actor kinds, target types, context areas and
meters. Authored next to the emitting code, validated in that code's CI,
registered at deploy, copied into the archive on first use. Format:
[catalogue.schema.json](../schemas/catalogue.schema.json).

## Extension slots

Fixed places in the record where a source attaches its own data, each keyed
by a discriminator that selects a registered JSON Schema:

| slot | keyed by |
|---|---|
| `data` | action |
| `targets[].attributes` | target type |
| `actor.attributes` | actor kind |
| `context.areas.<area>` | area |
| `meter.dimensions` | meter |

Extension schemas are closed objects with every property annotated with its
class and PII level. See [extension points](design/extension-points.md).

## Actor kinds and identity tiers

An actor kind is registered with a **category**: internal (staff),
external (end users, customers' people), machine (services, API keys, the
system). Presets decide the identity treatment per category: clear,
pseudonym, scoped or omit. Tenant identifiers are legal entities and stay
in clear everywhere. Internal actors stay in clear for the security
retention because accountability is a legal obligation. External actors and
subjects become a keyed pseudonym per tenant and purpose, so copies cannot
be joined on a person, and erasure is key destruction.

## Profiles and presets

A **preset** is what one framework requires: fields, categories, identity
treatment, retention, integrity, review, with citations. A **profile** is a
deployment's composition of presets plus a destination. The split writer
produces one copy per profile. See [presets](../presets/README.md).

## Prefixes

Each profile copy lands under its own S3 prefix, partitioned by tenant,
profile and day, with Object Lock retention set per copy at write time.
Large payloads are stored once under a payload prefix by content hash and
referenced from the copies. Schemas and catalogues used by the records are
copied under a schema prefix.

## Projections

Everything that is not a prefix copy: the facet index and counts table, the
metering rollups and statements, SIEM exports, analytics tables. All
idempotent, all rebuildable from the prefixes.

## Meta-events

Reads and exports of the trail, catalogue and profile changes, key
destruction, legal holds, digests, writer lifecycle and the daily clock
check are records too, in the [common catalogue](../catalogue/common.yaml).
