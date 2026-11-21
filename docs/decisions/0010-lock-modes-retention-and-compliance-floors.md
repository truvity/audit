# 0010. Lock modes, retention, legal holds, lifecycle tiers and compliance floors

Status: accepted, 2026-11-21

## Context

- **Blob stores lock differently.** AWS S3 has Object Lock in compliance mode: nobody, not even the account's root, can shorten an object's retention or delete it before then. Other stores have a bucket lock rule that an account administrator can remove. A local file system has none.
- **Evidence and personal data live for different times.** A bill or a security fact can be disputed after a tenant has left. A tenant's exit must still destroy everything personal.
- **A lock or a retention period is never shortened.** A wrong period locked in compliance mode cannot be shortened.
- **The numbers a standard sets are facts with a source and a date,** and they change faster than a record does.
- **Old evidence costs money,** and storage has colder classes. A tier must never weaken a lock.

audit-v1 had framework presets that each made a purpose-bound copy of the records in a store of their own. That put several retentions and several stores of the same evidence outside the tenant's keys. It is not carried over. What is carried over is the clause-cited data of the presets.

## Decision

### Lock modes

- **A project's bucket is a blob with an explicit lock mode,** `none`, `removable` or `compliance`. The backend sets it, and the configuration check reports it.

  | Backend | Lock mode | What it means for the log |
  |---|---|---|
  | AWS S3 | `compliance` | Object Lock in compliance mode |
  | S3-compatible store with a bucket lock rule | `removable` | A rule that an account administrator can remove |
  | Local file system | `none` | Nothing stops a local administrator. Tamper evidence comes from the witnesses. |

- **A production log needs a `compliance` lock, or a weaker lock plus a `peer` or `external` witness.** The configuration check refuses anything else in a production scope and warns elsewhere.
- **A bucket that audit does not own needs a guard in its own policy.** The settings of a blob destination include `bucket_guard` (`report`, the default, or `require`) and `breakglass_roles`, the roles that may still bypass governance retention, delete a version or change the lock. The guard is a policy statement that denies `s3:BypassGovernanceRetention`, `s3:DeleteObjectVersion` and `s3:PutBucketObjectLockConfiguration` to every principal except `breakglass_roles`. For an S3 destination the check reads the bucket policy and reports a bucket without the guard, reports a policy it cannot read as unchecked, and under `require` refuses a production scope whose bucket lacks it.

### Retention

- **Each log declares its retention in `AuditLog`, and a production log must** ([0015](0015-configuration-kinds.md)). The security log and the usage log declare theirs separately, so billing evidence can be kept as long as finance needs without keeping security records as long. There is no built-in default, so the period is chosen consciously and shows in the reviewed document.
- **A tenant's evidence retention is the latest end of the retention of any log of the scope that holds its entries,** in any project. The `seal` block deletes its `evidence` wrap then ([0011](0011-evidence-encryption-and-pseudonyms.md)).
- **Each backend enforces retention its own way:** per-object retention on a compliance store, set when the object is written and only ever extended; the bucket lock rule where there is one; a lifecycle rule or an expiry sweeper elsewhere. A sweeper holds a delete right on expired objects only. A trial in governance mode before compliance is recommended in the documentation and not enforced.
- **The log is not part of an ordinary backup.** It is protected by its lock, its witnesses and its store.

### Legal holds

- **Legal holds stay outside audit.** A hold is placed and lifted with the store's own means, under cloud IAM or a break-glass role. The person who does it records it as a security record under their own name. audit places no hold and lifts none.
- **Before the `seal` block deletes a tenant's `evidence` wrap,** the deletion checks for a standing hold on the logs that hold the tenant's entries. On a store with no hold flag, the operator suspends expiry for the held range by hand ([runbook](../runbooks/legal-hold.md)).

### Lifecycle tiers

- **Only cold, immutable objects move.** The entry bundles and the complete level-0 hash tiles may move to a colder storage class after a declared age. Partial tiles, hash tiles of higher levels, the checkpoints and the pointer stay in the hot class, so a proof and the newest entries never wait for a retrieval.
- **A tier never touches retention.** A transition leaves the object's lock and retention as they are, and expiry at the end of retention deletes the object from any tier. The conformance suite of each blob driver proves that a transitioned object keeps its lock.
- **Each blob driver declares the tiers it supports,** and the check refuses a tier the driver lacks. The ages are declared per log in `AuditLog`.
- **audit renders the lifecycle rule of each bucket and does not apply it.** The rule is a neutral artefact derived from the declaration, applied by the operator's infrastructure code. The writer, whose credential has no delete right, would otherwise need rights over bucket configuration.
- **A read of a cold object is explicit.** The reader returns a `pending` state with a retry time, and asks for the retrieval when the grant allows it. It never blocks a request on one. The search index holds the searchable fields, so a query does not touch cold objects. Only showing a full record and verifying do.

### Compliance floors

- **A profile is a floor and not a copy.** It is a data file, `profiles/<id>.yaml`, one per standard. For each log kind it states the least retention, the least lock mode (or whether a witness may stand in its place), whether a witness is required, the least identity treatment, the record classes that must be emitted and the review cadence. Each value carries the clause it comes from and the date it was last checked.
- **Each project's logs name the profiles they keep, in `AuditLog`.** The strictest value of each floor applies.
- **The configuration check enforces the floors.** It refuses, in a production scope, a declared retention, lock or identity treatment below the floor of a declared profile, naming the profile and the clause. It cannot check that a standard is met, only that the installation is not below the floor the profile file states.
- **The record classes are reported and not refused.** The reader reports, per period and project, a required class with no records, so that an application that stopped emitting is seen.
- **There are no purpose-bound copies.** One log holds the evidence. A consumer that needs another view reads the log through the reader, or takes the usage log, which has its own retention.
- **The release ships five profiles:** `dora`, `nen-7513`, `pci-dss`, `billing-nl` and `evidence-etsi` ([reference/compliance-profiles.md](../reference/compliance-profiles.md)). They are carried over from audit-v1 and are an engineering reading of the cited clauses, not legal advice.
- **Open: recheck of the clauses.** The profile files were carried over from the version of September 2026 and are not rechecked in this rewrite. They are rechecked before the first release.

### Built and planned

Built: the lock mode of the `fs` and S3-style backends as a declared property, the one-rule check of lock and witness (in sluis's configuration check). Planned: the retention rules, the bucket guard, the tiers, the rendered lifecycle rule, the profile files and the floor check, the holds check before an evidence wrap is deleted.

## Consequences

- A production scope on a store with a removable lock needs a witness, and a profile may require a compliance lock regardless.
- A legal hold works only where the store has one and as far as the operator's procedure goes.
- The cost of old evidence falls without a change to what can be verified. The price is a delay of minutes to hours when a cold range is read, and a runbook for a verifier that needs one.
- A profile file is data. A standard's change is a reviewed change to one file, dated and sourced, with no record to rewrite. The maintenance cost is the review cadence of five files.
- A tenant's `evidence` key outlives its personal data by the difference between the longest log retention and the exit.

## Roads not taken

- **Require a compliance lock everywhere, or only warn.** Only some backends offer compliance, so a production log on the others could never exist. Only warning leaves a production log one warning away from no protection.
- **A built-in retention, or keep forever.** A wrong default locked in compliance mode cannot be shortened. Keeping forever grows without bound and keeps every departed tenant's evidence.
- **Legal holds as a kind that the writer enforces.** The writer would gain the right to set holds, and the sweeper and the evidence deletion would have to check them.
- **A copy of the log per compliance profile,** as audit-v1 did. Several retentions and several stores of the same evidence, each outside the tenant's keys, and a copier for each.
- **Tier by moving the object to another bucket, or tier every object.** A new key means a new address, and the checkpoints and tiles name each other by path. Tiering higher-level tiles makes every proof wait for a retrieval, for a tiny saving.
- **audit applies the lifecycle rule itself.**
- **Compliance numbers in the record, or a profile that turns on features.** The values are the standards' and change when the standards do. A floor can be checked, while a feature switch would hide which control meets which clause.
