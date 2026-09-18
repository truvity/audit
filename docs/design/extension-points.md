# Extension points

Proto for what is compiled, JSON Schema for what is loaded. The core record
is fixed; sources extend it through predefined slots.

| slot | keyed by | registered by |
|---|---|---|
| `data` | `action` | the emitting source |
| `targets[].attributes` | `targets[].type` | whoever owns the type |
| `actor.attributes` | `actor.kind` | the platform or the source |
| `context.areas.<area>` | area name | the platform |
| `meter.dimensions` | `meter.name` | the emitting source |

## Rules an extension schema must satisfy

Enforced by `schemas/extension.schema.json`:

- A closed object (`additionalProperties: false`) with a URI `$id` under
  the source's namespace.
- Every property annotated with `x-audit-class` (shared, audit, metering,
  history, evidence) and `x-audit-pii` (none or identifier; direct and
  content are refused).
- Facetable properties are primitives. Meter dimensions are primitives.
- Bounded depth and string length. No binary.
- Optional: `x-audit-filter`, `x-audit-sensitive` (hmac or redact),
  `x-ocsf-path`, `x-ecs-path`.
- Optional `x-audit-expiry: true`, on a `string` with `format: date-time`
  only: this property is when the credential or certificate the record is
  about expires. A profile retained `after_expiry` (the evidence profile) locks
  the record's object until that moment plus its years, or its fallback if that
  is later; an object holding several such records is locked for the latest.
  The writer reads it from the record as written, so it applies even to copies
  whose profile drops the data slot. A record that does not carry it gets the
  fallback.
- An action's `extends: /pointer` names the data property (an id, or a list
  of them) holding the earlier records it is an addendum to; its schema must
  mark an expiry. After the addendum is durable the writer finds each earlier
  record — through the index, or a scan within its budget and horizon when
  there is none — in every `after_expiry` profile the addendum is kept in,
  and lengthens the lock on the object holding it to the addendum's expiry
  plus the years. Compliance mode never shortens a lock, and neither does the
  writer: a shorter expiry changes nothing. An earlier record of another
  tenant is refused. Each extension, and each failure with its reason, is an
  `audit.retention.extended` record under the addendum's tenant; a failure
  never fails the batch, and is counted for an alert.

## Example

```json
{
  "$id": "https://schemas.example/wallet/credential-issued/v2.json",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "credential_format": { "type": "string", "enum": ["sd-jwt-vc", "mdoc"],
                           "x-audit-class": "shared", "x-audit-pii": "none",
                           "x-audit-facet": true },
    "credential_id":     { "type": "string", "x-audit-class": "evidence",
                           "x-audit-pii": "none", "x-audit-filter": true },
    "attestation_bytes": { "type": "integer", "x-audit-class": "metering",
                           "x-audit-pii": "none" }
  }
}
```

## How components use the annotations

- **Emitter**: validates against the composed schema before publishing.
- **Split writer**: routes each property to the profiles whose classes
  include it; applies the pseudonym treatment to `identifier` properties by
  the kind's category; applies `x-audit-sensitive`.
- **Indexer**: creates facet columns and typed filter predicates from
  `x-audit-facet` and `x-audit-filter`.
- **Viewer**: renders the detail panel, facets and filters from `title`,
  `description`, `enum` and `format`.
- **Exporters**: map by `x-ocsf-path` and `x-ecs-path`.

## Discipline

A property that appears in two sources' slots is promoted to the core in
the next minor. Extensions are where fields prove themselves before they
become standard. The annotation vocabulary is tiny and adding to it is a
decision record.
