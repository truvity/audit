# 0019. The floors are a declared switch, `floors: enforce | report`; no installation name is a switch

Status: accepted, 2026-11-25 (owner decision on the pull request). Partly supersedes [0010](0010-lock-modes-retention-and-compliance-floors.md) and [0006](0006-the-usage-class.md).

## Context

[0010](0010-lock-modes-retention-and-compliance-floors.md) makes the configuration check refuse, "in a production scope", a lock without a witness, a log without a retention, a retention or lock below a compliance profile's floor, and a bucket without its guard under `require`; elsewhere it warns. [0006](0006-the-usage-class.md) says each log declares its retention "with no default in production". No record says what makes a scope a production one. The check would have to recognise a name, `production` or another, and every installation would have to call itself by the name the check knows.

audit is a set of libraries and pieces that several products and installations use. The owner's principle of 2026-11-25 (atoom record 0021, "Drivers and libraries know no deployment shapes; hosts decide") holds here as well: a library knows nothing about the installation it runs in, and the deployment passes an explicit, neutral switch where one is needed.

## Decision

- **Each log declares `floors` in `AuditLog`: `enforce` or `report`.** The deployment sets it. It is the only switch of the floors.
  - `enforce`: the configuration check refuses what 0010 refused "in a production scope". That covers a store whose lock is not `compliance` without a `peer` or `external` witness, a log with no retention, a retention, lock or identity treatment below the floor of a declared profile, and, under `bucket_guard: require`, a bucket without its guard.
  - `report`: the check reports each of them as a warning and refuses none of them, as 0010 did "elsewhere".
- **A log that declares no `floors` is `enforce`.** The check fails closed. A deployment that wants warnings only says so in the reviewed document.
- **No scope, installation or environment name is a switch.** The check recognises no name such as `production`, `staging` or `development`, and the floors never depend on a scope's name.
- **What this record overtakes.** In 0010, every "in a production scope", "a production log" and "warns elsewhere" now reads as "for a log whose `floors` is `enforce`" and "for a log whose `floors` is `report`". In 0006, "with no default in production" reads "with no default". Everything else in both records stands.

## Consequences

- The requirement rows that named a production scope, REQ-AUDIT-058, 059, 060, 136, 137 and 172, are withdrawn. They are replaced by REQ-AUDIT-196 to 201 with the same behaviour under `floors`, and by REQ-AUDIT-202 (no `floors` is `enforce`) and REQ-AUDIT-203 (no name is a switch).
- [config-kinds](../reference/config-kinds.md) gains the field, and [compliance-profiles](../reference/compliance-profiles.md) says the floors apply under `enforce`.
- A deployment that runs unprotected logs on purpose (a developer's machine, a test) sets `report` and sees the warnings. One that forgets the field gets the refusals.
- Nothing is built yet: the floor check is planned (0010, "Built and planned"), so no code changes.

## Roads not taken

- **Keep "production scope" and define it as a name.** The check would know one deployment's vocabulary, and an installation with another name, or more stages, would have to pretend.
- **A per-scope boolean such as `production: true`.** It is still an installation's name turned into a switch, and it says where the log runs rather than what the check does.
- **`report` as the default.** A forgotten field would let an unprotected log start silently, which is what the floors exist to stop.
