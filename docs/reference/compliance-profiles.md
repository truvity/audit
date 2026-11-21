# Compliance profiles

The decision is in [0010](../decisions/0010-lock-modes-retention-and-compliance-floors.md). A profile is a floor, and not a copy of the records. It is a data file in [`profiles/`](../../profiles/), one for each standard. Each value carries the clause it comes from and the date it was last checked.

**These files are an engineering reading of the cited clauses, and not legal advice.** A deployer confirms applicability, numbers and scope with its own assessor, auditor or counsel. They were carried over from the presets of audit-v1, version 2026.09, and **the clauses are not rechecked in this rewrite.** They are rechecked before the first release.

## What a profile states

For each log kind it covers (`audit` or `usage`):

| Key | Meaning |
|---|---|
| `retention_days_min` | The least retention, in days. Some profiles give a rule that starts at an event (`retention_rule: after_expiry`) with a fallback. |
| `hot_days_min` | The least time the records stay in the hot storage class |
| `lock_min` | The least lock mode: `none`, `removable` or `compliance` |
| `witness_may_replace_lock` | Whether a `peer` or `external` witness may stand in place of the lock. In all five profiles, no. |
| `witness_required` | Whether a witness is required |
| `identity` | The least identity treatment: `pseudonym`, or `none` where a record names no person |
| `log_access_audited` | Whether reads of the log must be audited |
| `clock_sync_check` | How often the clock is checked against a reference |
| `record_classes` | The classes of record that must be emitted. The reader reports a class with no records. |
| `review_cadence` | How often the log should be reviewed |
| `clauses` | The clause that each value comes from |
| `checked` | The month the clauses were last read |

**Open: the record classes.** The names are those of audit-v1's categories. Their mapping to action groups and OCSF classes is made with the first product tables.

## How the floors apply

- A project's logs name the profiles they keep in `AuditLog`. The strictest value of each floor applies: the longest retention, the strongest lock, and a witness if any requires it.
- The configuration check refuses, in a production scope, a retention, lock or identity treatment below the floor, and names the profile and the clause.
- The check cannot say that a standard is met. It says that the installation is not below the floor the file states.

## The five profiles

| Profile | Standard | Log | Retention floor | Lock floor | Review |
|---|---|---|---|---|---|
| [`dora`](../../profiles/dora.yaml) | DORA and Commission Delegated Regulation (EU) 2024/1774 Art. 12 | `audit` | 180 days (365 suggested) | `compliance` | monthly |
| [`nen-7513`](../../profiles/nen-7513.yaml) | NEN 7513:2018, and the five-year rule for access logs of medical records | `audit` | 1,826 days | `compliance` | monthly |
| [`pci-dss`](../../profiles/pci-dss.yaml) | PCI DSS v4.0.1 Requirement 10 | `audit` | 365 days, 90 of them hot | `compliance` | daily |
| [`billing-nl`](../../profiles/billing-nl.yaml) | The Dutch tax administration's duty to keep records (AWR art. 52) | `usage` | 2,557 days (7 years) | `none` | monthly |
| [`evidence-etsi`](../../profiles/evidence-etsi.yaml) | ETSI EN 319 401, 319 411-1 and 411-2, TS 119 461, and eIDAS Art. 24(2)(h) | `audit` | 7 years after the last credential that relies on the record ceases to be valid, with a fallback of 3,653 days | `compliance` | quarterly |

- **`billing-nl` is for the usage log.** It names no person: a usage event carries none ([0006](../decisions/0006-the-usage-class.md)).
- **`evidence-etsi` recommends time-stamp anchoring** of checkpoints. That is planned and not built.
- **A weaker lock is not allowed for four of the five,** whatever the witness. audit-v1 read the clauses as asking for a store nobody can delete from.

## What changed from audit-v1

| audit-v1 | Here |
|---|---|
| A preset made a purpose-bound copy of the records in a store of its own | A profile is a floor. There are no copies. |
| A framework profile composed into a deployment's own profiles | A project's logs name the profiles that apply |
| `forbidden_pii` and `required_fields` of v1's record | The record format is OCSF, and the product's table says which fields exist |
| `retention.policy: fixed` and `hot_days` | `retention_days_min` and `hot_days_min` |
| Presets `security` and `history` | Not carried over. They were v1's own defaults, and not a standard's floor. |
