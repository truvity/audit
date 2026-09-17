# Catalogue reference

Format: [catalogue.schema.json](../../schemas/catalogue.schema.json).
Example: [common.yaml](../../catalogue/common.yaml).

## Where it lives

Next to the code that emits, under version control with it, validated in
that repository's CI against this repository's validator, embedded in the
binary or mounted by the chart, and registered with `RegistryService` at
start-up. The registry copies every version into the archive on first use.

## Fields

- `source`, `version`, `locales`.
- `actor_kinds`: name → category (internal, external, machine),
  attributes schema.
- `target_types`: name → is_person, attributes schema.
- `context_areas`: name → schema.
- `meters`: name → kind (count, gauge), unit, dimensions schema.
- `actions`: `source.resource.verb` → summary, operation, categories,
  profiles, capture level, delivery, target types, data schema and
  version, message per locale, meter with quantity path.

## Categories

Presets require categories, never actions. A source in scope of a preset
must have at least one action per required category:

`authentication`, `privileged_access`, `account_lifecycle`,
`authorization_decision`, `configuration_change`, `data_access`,
`data_change`, `key_lifecycle`, `credential_lifecycle`, `log_access`,
`logging_control`, `clock`, `billing`.

## Message templates

ICU MessageFormat per locale. Arguments are core fields (`actor`,
`targets.0.id`, `outcome.reason`, `observer.instance`) and properties
declared in the action's data schema (`data.records`). The validator
refuses a template that references anything else. The viewer renders in
the browser; exports render a constrained subset server-side.

## Validation in CI

```
audit catalogue validate ./catalogue.yaml --schemas ./schemas/
audit catalogue check-emitters ./... --catalogue ./catalogue.yaml
```

The second command scans Go or TypeScript sources for emitted action names
and fails on any not in the catalogue.
