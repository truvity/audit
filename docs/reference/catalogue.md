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
  version, message per locale, meter with quantity path and the outcomes
  that count (success only unless the action says otherwise).

## Categories

Presets require categories, never actions. A source in scope of a preset
must have at least one action per required category:

`authentication`, `privileged_access`, `account_lifecycle`,
`authorization_decision`, `configuration_change`, `data_access`,
`data_change`, `key_lifecycle`, `credential_lifecycle`, `log_access`,
`logging_control`, `clock`, `billing`.

## Message templates

ICU MessageFormat per locale, one per declared locale, all required. The
validator reads enough of the grammar to tell an argument from a plural or
select submessage, and refuses a template that names anything a record of the
action does not carry. The viewer renders in the browser; exports render a
constrained subset server-side.

An argument is named by a **path** into the record: `{targets.0.id}`,
`{data.items}`. ICU itself forbids dots in argument names, so a template is not
handed to an ICU parser as written. `@truvity/audit` finds the arguments with
the same scanner as the validator and renames each to a placeholder first. Both
scanners are held to one fixture, `testdata/messages.json`. Any other renderer
must do the same.

Arguments a template may name:

| argument | value |
|---|---|
| `id`, `source`, `action`, `operation`, `tenant`, `profile` | the core fields |
| `occurred_at`, `recorded_at` | timestamps |
| `actor`, `actor.id`, `actor.kind` | the actor; `actor` alone renders as the viewer resolves it |
| `subject`, `subject.id`, `subject.kind` | the subject |
| `outcome`, `outcome.result`, `outcome.reason`, `outcome.code` | how it ended |
| `observer.id`, `observer.instance` | who reported it |
| `targets.N.id`, `targets.N.name`, `targets.N.type` for N in 0..3 | the first four targets |
| `data.<property>` | any property the action's data schema declares, nested with dots |
| `meter.name`, `meter.quantity`, `meter.unit` | when the action is metered |

## Validation in CI

```
audit validate ./catalogue.yaml
audit check-emitters ./ --catalogue ./catalogue.yaml
```

`validate` loads the document and every `.json` schema beside it. With
`--deployment <file>` it also composes the deployment's profiles and reports
any category a profile requires that nothing emits. `check-emitters` reads the
string literals in a source tree and fails on an action the catalogue does not
declare; it says plainly that it cannot see a name assembled at run time.
