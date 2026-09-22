# 0014. Lock modes and store tiers: the lock is demanded where a framework demands it

- Status: accepted
- Date: 2026-09-23

[0003](0003-s3-object-lock-as-the-record.md) stands for the `record` tier
it describes. This adds a second tier beside it and says which profiles may
run on which.

## Context

[0003](0003-s3-object-lock-as-the-record.md) made S3 Object Lock in
compliance mode the record and everything else a projection. It was written
for one deployer on one cloud, and it made two things mandatory that are
not the same thing: the Object Lock API, and the reading that every
framework demands WORM storage.

Neither survives contact with the deployments now being planned.

**Not every store has the Object Lock API.** The archive's calls are the S3
API's, and any store that speaks it — an S3-compatible service from another
provider, MinIO, Ceph — takes them, except for three: the lock headers on a
put, `PutObjectRetention` and `PutObjectLegalHold`. A store without them
refuses a put that names a lock mode outright, so a writer that always names
one cannot write to such a store at all, however good its digest chain is.

**Not every framework demands the lock.** The presets read seven frameworks,
and they divide. PCI DSS 10.3.2 and 10.3.3, NEN 7513, the DORA RTS on ICT
risk management (Art. 12) and ETSI EN 319 401 7.10 each require that logs or
evidence be protected against tampering and deletion in front of a named
assessor, and storage the operator cannot delete from is the reading a
qualified assessor expects. NIS2 and ISO/IEC 27001 are management-system
standards: the entity chooses the control that protects its logs from
alteration, and a signed digest chain under a managed key, on a bucket whose
policy grants nobody a delete, is a valid control. AWR art. 52 asks for the
authenticity and integrity of the administration over the retention period,
which the chain provides; it does not mandate WORM. `history` is a product
decision and cites no framework at all.

A single mandatory lock therefore forced a deployment whose profiles need
none onto the one kind of store that has it, and gave a deployment on any
other store no way to run at all.

## Decision

There are two store tiers, and a profile says which it needs.

**`record`** is [0003](0003-s3-object-lock-as-the-record.md) unchanged:
Object Lock in compliance mode, retention per object at write time,
deletes denied by policy, legal holds. It needs the Object Lock API:
`PutObject` with the lock headers, `PutObjectRetention`,
`PutObjectLegalHold`, beside `GetObject`, `HeadObject`, `ListObjectsV2`
and presigning.

**`attested`** is the same archive with no lock: the same keys, the same
objects, the same hourly signed digest chain, on a store that answers
`PutObject`, `GetObject`, `HeadObject`, `ListObjectsV2` and presigning and
nothing more. The chain proves that nothing was changed or removed since
it was sealed; what the lock alone covered — the objects of the hour not
yet sealed, and the operator's own ability to delete — is covered by the
compensating controls below, or not at all, and the profile says whether
that is acceptable.

Every preset's `object_lock_mode` is now the **least** lock its framework
demands: `compliance`, `governance` or `none`. Composition takes the
strictest, so a profile composed from any preset that demands compliance
demands it. `pci-dss`, `nen-7513`, `dora` and `evidence-etsi` demand
`compliance`; `security`, `history` and `billing-nl` demand `none`. Each
preset carries a one-line `note` saying why.

The deployment's lock mode (`--lock-mode`, the chart's `lockMode`) is what
the writer, the digest job and the verify job write with:
`compliance`, `governance` or `none`. **A component refuses to start when a
profile's composed mode is stricter than the deployment's**, naming the
profile and both modes: a compliance profile on a governance or unlocked
store, a governance profile on an unlocked store. A stricter store than a
profile asks is always fine.

On an unlocked store, `ExtendRetention` and `SetLegalHold` answer
`store.ErrNotLockable`. The writer treats a retention it could not extend as
it always did — the addendum is written, the failure is in the trail and
counted — and a legal hold placed by hand is refused with that error and
recorded as the attempt it was. `audit verify`, given the deployment,
reports an object with no lock as `unlocked` under a profile that demands
none and as `INVALID` under one that does.

## Compensating controls for the `attested` tier

These are what the deployment supplies in place of the lock, and the
[S3 guide](../operations/s3-guide.md) says how.

- **A managed signing key is required, not recommended.** On the record
  tier a local signer proved that objects had not changed since signing,
  and the lock proved nobody could have chosen what to sign. Without the
  lock only a key the operator cannot use to re-sign — KMS, a transit
  engine — proves the second, so on this tier it is the only signer that
  proves anything.
- **A shorter digest interval.** The unsealed window is the one gap the
  lock alone covered: an object of the current hour can be deleted before
  the chain names it. Running `audit digest` more often narrows it to
  minutes.
- **No delete permission, anywhere.** As on the record tier, no
  component's credentials carry `DeleteObject` or `DeleteObjectVersion`,
  and versioning is on where the store has it. This is the control ISO
  27001 A.8.15 accepts.
- **An out-of-band bucket rule where the store offers one.** Some stores
  can refuse deletes on a bucket or prefix by an administrative rule an
  administrator can also remove. That is governance-shaped protection —
  worth having, not worth mistaking for compliance mode — and a deployment
  that has it says so in its own records.

## Consequences

- A deployment that composes only `security`, `history` or `billing-nl`
  may run on any S3-compatible store, with `lockMode: none`, and the chart
  passes an endpoint, path-style addressing and static credentials to every
  component that touches the archive.
- A deployment that composes `pci-dss`, `nen-7513`, `dora` or
  `evidence-etsi` still needs a bucket with Object Lock, and the writer
  says so at start-up rather than at audit time.
- The exports bucket is unchanged: it always was an unlocked store, and it
  may now be on a store of its own.
- Retention on the attested tier is a lifecycle rule, not a lock: the store
  keeps objects until the rule expires them, and nothing stops an
  administrator shortening the rule. A profile that needs the retention
  to be unshortenable is a profile that demands the lock, and says so.
- The digest job on the attested tier writes its digests unlocked too. The
  chain is still self-certifying — each digest names the previous one's
  hash and signature — and a removed digest is a gap the verifier reports.

## Alternatives considered

- **Keep the lock mandatory and refuse every other store.** Simple, and it
  kept a deployment whose frameworks demand no lock off every store but
  one. The frameworks, not the store, should decide.
- **Make the lock optional everywhere, with a warning.** A compliance
  profile would then run unlocked on a mistake in one values line, and
  every copy written before the warning was read would be the evidence the
  framework asked for, deletable. The refusal is the point.
- **Emulate a lock on stores that lack one**, by refusing deletes in the
  writer's own code. The archive's guarantee has never rested on what this
  code refuses to do; it rests on what the store refuses to do for
  everyone. A lock the writer enforces protects against the writer.
- **Governance mode as the attested tier.** It needs the Object Lock API
  the attested tier exists to do without, and it is bypassable by anyone
  holding the permission — which makes it the wrong name for "no lock".
