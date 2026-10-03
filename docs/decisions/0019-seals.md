# 0019. Seals: JOSE ES384, chained, per profile, tenant and hour

- Status: accepted
- Date: 2026-10-02

Supersedes [0008](0008-digest-chain-and-verification.md). The reasoning
there about what a chain proves, and about who holds the key, stands and is
not repeated.

## Context

[0008](0008-digest-chain-and-verification.md) chose an hourly signed digest
per profile, listing every object with its hash, naming the previous digest,
signed by a pluggable signer. Three things in it do not carry into the three
parts of [0016](0016-three-parts-installed-independently.md).

The digest is a bespoke JSON document with a bespoke signature encoding, so
an auditor needs this component's tool to read one. It is per profile, so one
busy tenant sets the cost of the hour for every other. And it names its
signing key by an identifier and trusts the archive to hold the public half,
which means whoever can write the archive can substitute the key.

## Decision

A **seal** is a JWS (RFC 7515) in compact form, algorithm **ES384**, signed
with a P-384 key held in a KMS or another signer behind the `Signer`
interface. One seal is written **per profile, tenant and hour**, for empty
hours too, at
`seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws`. Seals are chained
through **`prev`**, the SHA-256 of the previous seal's bytes, so removal and
reordering are both visible.

A seal's payload is a proto message (`audit.v1.Seal`) in its JSON form, as
are the delegation and revocation messages below. The wire definitions are
the contract, so a verifier in any language generates its structures from the
same file. The fields are in
[the bucket contract](../reference/bucket-contract.md#seals).

**2026-10-03: the root is over per-record hashes**, so one record can be
proven with a short audit path. The root is a Merkle tree (RFC 6962) of the
record hashes from all objects in the hour, ordered by object key (ULID), then
line order within each object. The specification is in
[the bucket contract](../reference/bucket-contract.md#merkle-tree-construction).

**2026-10-03, built.** The notary (`audit-notary`) writes seals and `audit verify
--root` checks them; the delegation and revocation statements are checked by the
verifier and **not yet signed by anything**: a notary signs with the key it is
given, which is a root listed in `keys/roots.jwks`, and the daily delegated key
(decision L3) is a change to the notary and not to this decision. Three
details settled in the build: a verifier also takes the bucket's time of writing
a seal as a second witness to `sealed_at`, which a key signs for itself; an
hour whose objects do not match their own metadata is not sealed, nor is any
hour after it, so the chain stops where the notary cannot vouch; and the payload
writes every field (a count of zero is `"0"`, an empty `prev` is `""`), so a
reader that does not know the proto defaults reads what the seal says.

**Trust anchors are pinned.** `keys/roots.jwks` lists root public keys, and
**observe's configuration pins them by thumbprint** (RFC 7638). The file in
the bucket is a convenience for distribution; a root that is not pinned by
the verifier is not trusted, so writing to the bucket cannot add one.

**Delegation and revocation.** A root may sign a **delegation**: a statement
that another key may sign seals for a scope (profiles and tenants) between
`nbf` and `exp`, with `exp − nbf` at most 25 hours. A notary then runs with
a key that is short-lived and renewed each hour, while the root stays offline
or in a hardware module. A root may sign a **revocation** of a key, effective
from a time; a seal signed by a revoked key at or after that time fails
verification. A seal signed directly by a root needs no delegation.

`audit verify` walks the chain, checks each signature up to a pinned root,
each delegation's window and scope, each revocation, each object's metadata
hash against its listing, and each object's lock.

## Consequences

- **An auditor can read a seal with any JOSE library**, and recompute the
  Merkle root from the listing and the object hashes.
- **Per tenant, an hour is cheap**: one list, the objects' metadata, one
  signature. Empty hours are sealed with count zero, so a gap is a fault.
- **The notary's key can be a delegate that expires in a day**, so a leaked
  key is bounded by the delegation and ended by a revocation.
- **Only the pin in observe's configuration is the root of trust**, so the
  archive's writer, however compromised, cannot choose who signs.
- ES384 is fixed. A new algorithm is a new `typ` and a new decision.
- Time-stamp anchoring of the chain head, which 0008 offered as a preset
  option, is unchanged and not part of the seal.

## Alternatives considered

- **Keep the 0008 digest, change the object layout under it.** Leaves the
  bespoke format and the writable key.
- **A transparency log.** Still a service to run and witness; the reasons in
  0008 stand.
- **One key, no delegation.** Simplest, and a notary on a function platform
  would hold the root.
- **Ed25519 or RSA.** Ed25519 is not offered by the managed key services this
  has to run on; RSA is larger and slower to sign for no gain.
