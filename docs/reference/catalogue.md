# Catalogue reference

Format: [catalogue.schema.json](../../schemas/catalogue.schema.json).
Example: [common.yaml](../../catalogue/common.yaml).

## Where it lives

Next to the code that emits, under version control with it, validated in
that repository's CI against this repository's validator, embedded in the
binary or mounted by the chart, and registered at start-up over
`RegistryService`, which the **receiver** serves — there is no registry
service of its own
([0011](../decisions/0011-one-installation-per-service-or-product.md)). The
receiver copies every version into the archive on first use.

One catalogue belongs to one application, and an installation admits one
application. What is shared between applications is this format and the
[presets](../../presets/README.md) that profiles are composed from, never a
catalogue.

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

## Delivery

An action declares one of two deliveries, and the choice is the action's,
not the deployment's: the same catalogue behaves the same way in direct and
in stream mode
([0012](../decisions/0012-two-deliveries-and-a-durable-ack.md)).

| `delivery` | the application's call returns | if the receiver is down | for |
|---|---|---|---|
| `block` | when the receiver has acknowledged durability | the action **fails** | a privileged sign-in, a key destruction, a billable operation |
| `async` (the default) | at once | the record waits in a bounded in-memory queue and is retried with backoff | everything else |

An acknowledgement always means durable, in either shape. An `async` record
is dropped only if the queue overflows, and then it is counted
(`audit.emit.records.dropped`) and logged.

`outbox` and `best_effort` are retired. **The loader refuses either, naming
the replacement**: `outbox` is `block` where the action may not go
unrecorded, and `async` otherwise; `best_effort` is `async`. There is no
file outbox and no volume on the emitting pod.

[catalogue.schema.json](../../schemas/catalogue.schema.json) lists the two
and defaults to `async`. A document declaring one of the retired spellings is
refused where it is loaded, before any emitter sees it.

## Categories

Presets require categories, never actions. A profile's required categories
must be covered by the installation as a whole — the application's catalogue
and the component's own together — not by each source: an application that
signs people in need not also read logs. `audit validate --deployment` fails
on a gap in the application's CI, and the receiver logs one after each
registration; neither refuses an application for a category it has no reason
to emit.
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
