# Integrity

How the archive shows that nothing was removed, nothing was changed, and the operator did not choose what
was signed. The decisions are [0003](../decisions/0003-s3-object-lock-as-the-record.md) (the lock),
[0014](../decisions/0014-lock-modes-and-store-tiers.md) (the tiers) and [0019](../decisions/0019-seals.md)
(seals); the byte-level contract is the [bucket contract](../reference/bucket-contract.md#seals), and the
check is [`audit verify`](../reference/verify-command.md). The v0 digest chain that preceded seals was removed
with the v0 layout ([0008](../decisions/0008-digest-chain-and-verification.md)).

## Three layers

1. **Per-object and per-record hashes.** Every object names its `sha256` and each record line carries its
   `hash`; `audit verify` recomputes both from the archive alone. This shows each object is the object that
   was written. It cannot show that an object was removed or added beside the others.
2. **The lock.** On the record tier the store is written in Object Lock compliance mode, so nobody, the
   account's root included, can overwrite or delete an object before its retention ends.
3. **Seals.** The notary writes one signed seal per profile, tenant and hour, for empty hours too, chained
   through `prev` and carrying a Merkle root over the hour's record hashes. A seal that disagrees with the
   bucket shows an object added, removed or changed after it was sealed.

## Why a job, and not a signature on each record

What the seals must prove is that nothing was **removed**, nothing was **changed**, and the operator did
not **choose** what was signed. A signature per record, made by the emitter or by the writer, proves only the
second:

- **Removal leaves nothing behind.** A signed record that is deleted takes its signature with it. Only a signed
  list of everything written in an hour, including an empty list for a quiet hour, makes a missing object
  visible.
- **An emitter's signature says "the application said so",** which the trail already knows: the writer verifies
  each caller's workload token and stamps it as the record's observer, and the `origin_hash` covers the record as
  accepted. A compromised application would sign its own false records as readily, and keys per emitter would
  have to be issued, rotated and revoked in the least trusted place in the system.
- **The writer must not hold the signing key.** It already has write rights on the archive; with the key as well,
  one compromised process could both write and vouch for what it wrote. The notary runs as its own identity, may
  use the key, and may write only under `seals/` and `keys/`.
- **Quiet hours need a seal too,** and nothing wakes the writer when nothing happens. A scheduled job seals every
  hour, and one that missed its runs seals the hours it missed.

The cost is that the current hour is not sealed yet (a seal is due after the settle window and a grace). Until
then its objects are protected by the lock and not yet by the seal.

## Two tiers

The **record** tier is a store with the Object Lock API, written in compliance mode. The **attested** tier is the
same archive and the same seals on a store with no lock: one without the Object Lock API, such as an
S3-compatible service from another provider, or a bucket a deployment chooses not to lock. On it the seals are the
whole of the integrity story, and the gap the lock alone covered (the unsealed hour, and the operator's own
ability to delete) is covered by compensating controls: a managed signing key, which is required there rather than
recommended; no delete permission on any component; and an administrative no-delete rule where the store offers one.

Which tier a deployment may run on is the profiles' decision. Every framework profile says the least lock its
framework demands (`compliance` for `pci-dss`, `nen-7513`, `dora` and `evidence-etsi`; `none` for `security`,
`history` and `billing-nl`), and the writer refuses to start when the deployment's
`archive.lockMode` is weaker than any composed profile demands.

## Who holds the key

With a signing key the writer holds itself, the seals would prove objects have not changed since signing, by a
party who could also have chosen what to sign. A managed key, in KMS or a transit engine, never leaves its
provider, so the seals also prove the operator did not choose what to sign. A deployment that must answer an
assessor uses a managed key; a key file is for tests and for a deployment that accepts the weaker claim knowingly.
An auditor believes a key because they **pinned its thumbprint**, not because it is in the bucket
([key custody](key-custody.md#signing-key)).

## Time

Emitters and writers run on synchronised clocks. `audit clock-sync` checks the
offset against UTC daily and records `audit.clock.synchronised`, which the
evidence and PCI framework profiles require. It measures and records; it never sets the
clock, because a component that both set the time and recorded the times of
things would be marking its own paper.

The recorded `offset_ms` is the correction this clock needs, as RFC 5905 §8 has
it: positive means the clock is behind. Given several references it believes
the quickest to answer, since the error in an offset is bounded by half the
round trip that carried it. A clock outside the deployment's tolerance fails the
run and is recorded anyway — that hour is exactly the one an auditor wants the
measurement from. Nothing is recorded when no reference answered: the clock was
not checked, and saying it was would be worse than a failed job.

## Anchoring

Framework profiles may require or recommend anchoring the head of the seal chain with an RFC 3161 or ETSI
time-stamp. **Not built.** Time-stamp anchoring is not part of the seal ([0019](../decisions/0019-seals.md)).
