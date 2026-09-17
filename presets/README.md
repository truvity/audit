# Framework presets

A preset says what one framework requires of a profile: which record fields
must be present, which are forbidden, how identities are treated, how long
copies are kept, what integrity controls apply, and how often the trail is
reviewed. Each preset cites the clauses it reads and carries a disclaimer.

A deployment composes presets into **profiles**. Composition is a union:

- `required_fields`, `required_categories`: union.
- `forbidden_fields`, `forbidden_pii`: union.
- `identity`: the stricter treatment wins (`omit` > `pseudonym` > `scoped` > `clear`).
- `retention`: the longest wins, and `minimum_days` can only be raised.
- `integrity`: `required` wins over `recommended`, `compliance` over `governance`.
- `review`: the most frequent cadence wins.

The validator (`schemas/preset.schema.json` plus the composition rules in
`docs/reference/presets.md`) refuses a profile that would drop a field a
preset requires, and refuses a catalogue whose sources lack a required
category.

| preset | framework | retention default |
|---|---|---|
| `security` | NIS2 + ISO/IEC 27001:2022 + PCI DSS as the prescriptive floor | 365 days, 90 hot |
| `billing-nl` | Dutch tax administration duty (AWR art. 52) | 7 years |
| `evidence-etsi` | ETSI EN 319 401 / 411 for trust service providers | 7 years after expiry |
| `history` | product policy for tenant-facing activity | 365 days |
| `pci-dss` | PCI DSS v4.0.1 Requirement 10 | 12 months, 3 hot |
| `dora` | DORA RTS on ICT risk management, Art. 12 | entity-defined, 365 default |
| `nen-7513` | NEN 7513 healthcare access logging | 5 years |

Where a framework gives no number, and NIS2, ISO 27001 and DORA all say
"define it yourself", the preset carries a defended default and marks it
`configurable`.
