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

Presets require categories, never actions. A profile's required categories
must be covered by the deployment as a whole — every registered catalogue and
the component's own together — not by each source: an application that signs
people in need not also read logs. `audit validate --deployment` fails on a
gap in the deployment's CI, and the registry logs one after each
registration; neither refuses an application for what another should emit.
The categories:

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

An argument names a field of the record, with an underscore for each step:
`{targets_0_id}`, `{data_items}`, `{data_address_city}`. ICU forbids dots in
argument names, so these templates render as written in any ICU
implementation — the viewer uses FormatJS. The validator refuses a dotted name
and says what the underscore spelling is, and refuses a data schema where two
properties would answer to the same name (`/a_b` and `/a/b` are both
`data_a_b`). An argument a record does not carry renders as a gap.

Arguments a template may name:

| argument | value |
|---|---|
| `id`, `source`, `action`, `operation`, `tenant`, `profile` | the core fields |
| `occurred_at`, `recorded_at` | timestamps |
| `actor`, `actor_id`, `actor_kind` | the actor; `actor` alone is its id |
| `subject`, `subject_id`, `subject_kind` | the subject |
| `outcome`, `outcome_result`, `outcome_reason`, `outcome_code` | how it ended; `outcome` is the result word (`success`, `failure`, `denied`) |
| `observer_id`, `observer_instance` | who reported it |
| `targets_N_id`, `targets_N_name`, `targets_N_type` for N in 0..3 | the first four targets |
| `data_<property>` | any property the action's data schema declares; nested properties join with underscores (`/address/city` is `data_address_city`) |
| `meter_name`, `meter_quantity`, `meter_unit` | when the action is metered |

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
