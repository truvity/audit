# Presets reference

Format: [preset.schema.json](../../schemas/preset.schema.json). Files:
[presets/](../../presets/). This page is the mechanism — what a preset says
and what composing two of them produces. Which presets an installation is
expected to compose, and what each costs to run, is
[the presets policy](../operations/presets-policy.md).

## Composition into a profile

A profile is declared in the installation's configuration, and the profiles
are that installation's own: the application names them in its catalogue,
and nothing else composes them.

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
| `integrity` | required > recommended; compliance > governance > none for `object_lock_mode`, which is the least lock the store must run ([0014](../decisions/0014-lock-modes-and-store-tiers.md)); daily > none. The `note` kept is the one behind the lock mode that won |
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
a reviewer checks the deployment against. It reports the **effective**
treatment per identity category, including the override below, so what is
actually written is inspectable rather than inferred from the preset files.

## What a deployment can relax

Composition only ever tightens, with one exception. A deployment declares
`external_identifiers_are_opaque` when the identifiers it receives for
external people are ones it minted itself and that mean nothing outside its
own database. When it does, a composed profile's `external: pseudonym` is
treated as `clear`, and the writer refuses a record whose external actor,
subject or person target carries something that looks like a direct
identifier — an address, or whitespace — naming the field and why.

This is what lets `keys.provider: none` be the default: a profile that asks
for a pseudonym does not force a key provider on a deployment that has
nothing to pseudonymise
([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)). When the
declaration is false and the provider is `none`, the writer refuses to start
if the registered catalogue declares an external actor kind or a person
target type: a deployment chooses, and may not arrive at clear-text
addresses in a locked archive by omission.

`external_identifiers_are_opaque` and `keys.provider: none` are not built
yet: they arrive with the rewrite. Nothing relaxes `internal`, `scoped` or
`omit`.

The `history` preset was reworked for the same reason. It treated internal
actors as `pseudonym`, which made a tenant-facing view need a key provider
before it would render. It now omits them, so a staff actor is shown by kind
and role and never by identity, and `external: scoped` stays.

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

## Changing a profile

A profile's rules decide what every record written under it means, so a change
is itself recorded. At start-up the writer fingerprints each composed profile
and compares it with the last composition in the archive, under
`schema/profile/<name>/`:

- the first composition a deployment records is kept there and is not a change;
- a different composition is kept beside the old one (both stay readable, so
  "what did this profile keep in April" has an answer) and emits
  `audit.profile.changed` with both fingerprints;
- a different preset version emits `audit.preset.changed`, even when the rules
  it composes to are the same — a library upgrade changes meaning nobody typed.

Both actions are declared `block`: a writer that cannot record the change does
not start. The events are recorded before the new composition is written, so a
writer stopped in between records the change again next time rather than never.
