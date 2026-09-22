# Framework presets

A preset says what one framework requires of a profile: which record fields
must be present, which are forbidden, how identities are treated, how long
copies are kept, what integrity controls apply, and how often the trail is
reviewed. Each preset cites the clauses it reads and carries a disclaimer.

A preset also names the core fields a copy under it must, may and may never
carry, and the field classes of extension properties it keeps. A copy is
default-deny: what no preset names is dropped.

A deployment composes presets into **profiles**. Composition is a union:

- `field_classes`, `required_fields`, `optional_fields`, `required_categories`: union.
- `forbidden_fields`, `forbidden_pii`: union; forbidden beats optional.
- `identity`: the stricter treatment wins (`omit` > `pseudonym` > `scoped` > `clear`).
- `retention`: the longest wins, and `minimum_days` can only be raised.
- `integrity`: `required` wins over `recommended`; for `object_lock_mode`,
  `compliance` over `governance` over `none`. The mode is the least lock the
  store must run: `pci-dss`, `nen-7513`, `dora` and `evidence-etsi` demand
  `compliance`, `security`, `history` and `billing-nl` demand `none`, and
  each says why in a one-line `note`. The writer refuses to start on a store
  written in a weaker mode than a composed profile demands
  ([0014](../docs/decisions/0014-lock-modes-and-store-tiers.md)).
- `review`: the most frequent cadence wins.

The validator (`schemas/preset.schema.json` plus the composition rules in
[the presets reference](../docs/reference/presets.md)) refuses a profile whose presets both require and
forbid a field, directly or through an ancestor, and a deployment whose
catalogues do not emit a category a profile requires. `audit profile explain
<name>` prints what a profile keeps.

| preset | framework | retention default | identities: internal / external |
|---|---|---|---|
| `security` | NIS2 + ISO/IEC 27001:2022 + PCI DSS as the prescriptive floor | 365 days, 90 hot | clear / pseudonym |
| `billing-nl` | Dutch tax administration duty (AWR art. 52) | 7 years | omit / omit |
| `evidence-etsi` | ETSI EN 319 401 / 411 for trust service providers | 7 years after expiry | clear / pseudonym |
| `history` | product policy for tenant-facing activity | 365 days | **omit** / scoped |
| `pci-dss` | PCI DSS v4.0.1 Requirement 10 | 12 months, 3 hot | clear / pseudonym |
| `dora` | DORA RTS on ICT risk management, Art. 12 | entity-defined, 365 default | clear / pseudonym |
| `nen-7513` | NEN 7513 healthcare access logging | 5 years | clear / scoped |

`history` omits internal actors rather than pseudonymising them, which is
what lets a tenant-facing view of activity render with no key provider: a
staff actor is shown by kind and role, never by identity, which is what a
tenant's administrator should see anyway
([0013](../docs/decisions/0013-no-pseudonymisation-keys-by-default.md)).
Composed with `security`, which keeps staff in clear, the stricter reading
wins and the composed profile omits them.

A deployment that has declared its external identifiers opaque gets `clear`
where the table says `pseudonym`; `audit profile explain <name>` prints the
effective treatment.

Where a framework gives no number, and NIS2, ISO 27001 and DORA all say
"define it yourself", the preset carries a defended default and marks it
`configurable`.

Which of these an installation is expected to compose, and what each costs to
run, is [the presets policy](../docs/operations/presets-policy.md).
