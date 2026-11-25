# Configuration kinds

The decision is in [0015](../decisions/0015-configuration-kinds.md). audit declares four kinds to atoom's `state` registry. A document is signed, reviewed and verified before a piece uses it. The pieces write none of them.

The kinds exist today as kinds of sluis's store actor. Until a host runs atoom's `state` block, audit's writer reads the same signed documents through the source port ([0020](../decisions/0020-the-writer-moves-here-first-and-reads-nats-jetstream.md#the-configuration-before-b7)). The fields below are those that the records require. The schemas are written with the kinds in the registry. **Open: the exact field names and the schemas.**

## AuditLog

One item for each project.

| Field | Holds | Rule |
|---|---|---|
| `bucket` | The blob destination: its driver, bucket and settings | `bucket_guard` (`report` or `require`) and `breakglass_roles` for a bucket that audit does not own |
| `logs` | The logs of each kind, `audit` and `usage` | |
| `lock` | The lock mode of each log: `none`, `removable` or `compliance` | Set by the backend and checked against it |
| `retention` | The retention of each log, in days or as the profile's rule | No default. A log whose `floors` is `enforce` must declare one. |
| `floors` | How the configuration check applies the floors to each log: `enforce` or `report` | `enforce` refuses, `report` warns; `enforce` when absent. The deployment sets it; no scope or installation name is a switch ([0019](../decisions/0019-floors-are-a-declared-switch.md)) |
| `tiers` | For each log, a list of age and storage class | A tier the driver lacks is refused. A tier never touches retention. |
| `profiles` | The compliance profiles each log keeps | The strictest value of each floor applies |
| `products` | The product names the project's writer accepts in `metadata.product.name` | The writer refuses another |

## AuditWitnesses

One item for each log.

| Field | Holds |
|---|---|
| `witnesses` | Each witness: its driver (`none`, `peer`, `external`), its verifier key, and its address |
| `required` | The number of cosignatures a verifier requires |

## AuditCatalogue

One item for each project, named `default` (a singleton).

| Field | Holds |
|---|---|
| `release` | The newest release it covers |
| `policy` | The version of the schema policy ([action table and catalogue](action-table-and-catalogue.md#the-catalogue)) |
| `table` | The action table |
| `schemas` | The shared definitions and one schema for each action |

## Meter

One item for each meter.

| Field | Holds |
|---|---|
| `name` | The CloudEvents `type`. Fixed. |
| `unit` | A lower-case word. Fixed. |
| `kind` | `count` or `gauge`. Fixed. |
| `combine` | For a gauge: `avg`, `max` or `last` |
| `outcomes` | The outcomes it records. Default `success`. |
| `retired` | Whether it is retired |

A meter is a fixed document: a change is a new meter.
