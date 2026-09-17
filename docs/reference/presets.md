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
| `field_classes` | union |
| `required_fields`, `optional_fields`, `required_categories` | union |
| `forbidden_fields`, `forbidden_pii` | union; forbidden beats optional |
| `identity.<category>` | strictest: omit > pseudonym > scoped > clear |
| `retention` | longest; `minimum_days` may only be raised; `after_expiry` beats `fixed` |
| `integrity` | required > recommended; compliance > governance; daily > none |
| `review.cadence` | most frequent |
| `pipeline` | longest windows; shortest `close_after_hours` |

## What a copy carries

A preset names fields as JSON pointers in three lists, and **anything it does
not name is dropped**. A copy carries what its purpose justifies and nothing
more, so adding a field to the record does not quietly widen every copy of it.

- `required_fields` must be present, and are kept.
- `optional_fields` are kept when the record has them.
- `forbidden_fields` are never kept. Forbidding a field forbids everything
  beneath it, so `/actor` and `/actor/id` are one rule.

A listed child keeps the parent that has to carry it: a copy cannot hold
`/outcome/result` without `/outcome`.

Extension properties are not named field by field. They are kept when their
`x-audit-class` is in the profile's `field_classes` and their `x-audit-pii` is
not in `forbidden_pii`. The free-form bags (`attributes`, `unmapped`) count as
class `audit`.

`audit profile explain <name>` prints the result for a deployment, which is what
a reviewer checks the deployment against.

## Validation

The validator refuses:

- a profile whose composed allow-list drops a required field;
- a profile whose required and forbidden sets intersect;
- a deployment whose registered catalogues lack a required category for a
  profile they emit into;
- a preset without citations or the disclaimer;
- a profile whose presets both require and forbid a field, directly or through
  an ancestor. Composing the security and billing presets into one copy is
  refused for exactly this reason: security must know who acted and billing must
  not, which is why they are separate copies rather than one with a compromise.

## Changing a preset in use

Emits `audit.preset.changed` with block delivery. Retention already set on
written objects is unaffected; only new objects take the new value.
