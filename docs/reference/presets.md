# Presets reference

Format: [preset.schema.json](../../schemas/preset.schema.json). Files:
[presets/](../../presets/).

## Composition into a profile

A profile is declared in deployment configuration:

```yaml
profiles:
  security:
    presets: [security, pci-dss]
    prefix: profile=security
  billing:
    presets: [billing-nl]
    prefix: profile=billing
  history:
    presets: [history]
  evidence:
    presets: [evidence-etsi]
```

Composition rules:

| property | rule |
|---|---|
| `required_fields`, `required_categories` | union |
| `forbidden_fields`, `forbidden_pii` | union |
| `identity.<category>` | strictest: omit > pseudonym > scoped > clear |
| `retention` | longest; `minimum_days` may only be raised; `after_expiry` beats `fixed` |
| `integrity` | required > recommended; compliance > governance; daily > none |
| `review.cadence` | most frequent |
| `pipeline` | longest windows; shortest `close_after_hours` |

## Validation

The validator refuses:

- a profile whose composed allow-list drops a required field;
- a profile whose required and forbidden sets intersect;
- a deployment whose registered catalogues lack a required category for a
  profile they emit into;
- a preset without citations or the disclaimer.

## Changing a preset in use

Emits `audit.preset.changed` with block delivery. Retention already set on
written objects is unaffected; only new objects take the new value.
