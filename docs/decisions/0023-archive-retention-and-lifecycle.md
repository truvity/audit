# 0023. Archive retention: Object Lock compliance as the target, governance first

- Status: accepted
- Date: 2026-10-02

Extends [0003](0003-s3-object-lock-as-the-record.md) and
[0014](0014-lock-modes-and-store-tiers.md): the `record` tier stays Object
Lock in compliance mode, and this says how a deployment gets there and what
the storage costs once it has.

## Context

Compliance mode cannot be shortened by anyone, including the account's
owner. A retention that is wrong in the long direction is paid for until it
expires, and one set on the wrong bucket cannot be undone. The first lock on
an archive is therefore the riskiest write the component makes.

Cost is the other half. Years of retained objects in standard storage is the
most expensive way to keep them, and a retention that cannot be shortened
makes the storage class the only lever left.

## Decision

**Compliance mode is the target for every `record`-tier archive.** A
deployment reaches it through a **governance trial**: a separate bucket,
written by the same components with the same profiles and retention
computed the same way, in governance mode, run long enough to see the
retention values, the key layout and the lifecycle rules behave. Only when
the deployer has signed off the retentions does the archive move to a
compliance-mode bucket. Governance is a rehearsal, not a destination; for a
profile that demands no lock, [0014](0014-lock-modes-and-store-tiers.md)'s
`attested` tier is the answer.

**Lifecycle moves objects to colder storage by age**: to Glacier Instant
Retrieval at **30 days**, and to Deep Archive at **1 year**. Instant
Retrieval keeps the objects readable by observe's reindex and by `verify`
without a restore; Deep Archive is for objects nobody expects to read before
retention ends. Lifecycle rules are scoped by prefix, which is why the
profile is the first key component
([0018](0018-v1-bucket-layout.md)), and objects below the storage class's
minimum billable size stay where they are.

**Seals carry the lock of the records they cover**; `catalogue/` is written
once and locked; `keys/roots.jwks` is versioned and not locked, because the
pin in observe's configuration, not the file, is the trust.

## Consequences

- The first compliance write is made against retentions the deployer has
  already seen working.
- The trial doubles the storage for its length, on a bucket that is then
  emptied or kept until its own retention lapses.
- Reads of objects older than a year need a restore, and `verify` over such a
  range says so before it starts.
- The transition days are defaults the deployer may change before the
  compliance bucket exists; after it, a change applies to new objects only.

## Alternatives considered

- **Compliance mode from the first write.** Fewest steps; an error in a
  profile's retention is then permanent.
- **Governance as the destination.** Bypassable by anyone with the
  permission, which is why [0003](0003-s3-object-lock-as-the-record.md)
  refused it.
- **Everything in standard storage.** Simple, and the largest cost for the
  longest time.
- **Deep Archive from day one.** Cheapest per byte, and unreadable by the
  tools this component ships until a restore completes.
