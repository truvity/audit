# 0013. No pseudonymisation keys by default

- Status: accepted
- Date: 2026-09-22

Supersedes the preference in [0010](0010-key-providers.md) for a managed
provider wherever one is available. [0005](0005-identity-tiers-and-pseudonymisation.md)
still describes the model; this decides what a deployment turns on.

## Context

[0005](0005-identity-tiers-and-pseudonymisation.md) put every identity in
one of three tiers — kept in clear, replaced by a keyed pseudonym, or
dropped — and [0010](0010-key-providers.md) built three providers for the
keys. Both were written before any deployment existed. Now that the first
ones are being planned, the keys buy less than they cost.

Two kinds of people appear in these records.

**Staff**, in an internal service's trail: who signed in, who granted
access, who took a token. Pseudonymising them defeats the purpose. The
records exist so that a person can be held to what they did, and the people
allowed to read the trail are already the people allowed to know who works
here.

**End users**, in a product's trail. They do not arrive as names or
addresses. A product identifies them by an identifier it minted — an opaque
one, meaningless outside that product's own database. That identifier
already is a pseudonym in the sense the regulation means: it does not
identify anyone without a second dataset held by the product, and when the
product deletes the user, the link is gone. Encrypting it a second time adds
a key to manage, a provider to run and a way to lose the trail's
readability, and changes nothing about what a reader of the archive alone
can learn.

Against that, keys are not free: a provider per deployment, a permanent
choice (they cannot be migrated without re-keying every tenant), a login
path from the writer, an erasure procedure, and a resolve path that has to
be governed. The retention rules make the point sharper — an archive under
a compliance lock cannot be edited, so erasure has to be crypto-shredding,
which is precisely what a deployment that needs it should be made to
configure deliberately.

## Decision

**`keys.provider: none` is the default.** The writer runs with no key
provider: no key directory, no login to a secret manager, no `identity/`
prefix, and `resolve` refused as unimplemented.

**A deployment declares `external_identifiers_are_opaque`** in its profile
configuration. When it is true, a composed profile's `external: pseudonym`
is treated as `clear`, and the writer refuses a record whose external actor,
subject or person target carries something that looks like a direct
identifier — an address, or whitespace — naming the field and why.

When it is false and the provider is `none`, the writer refuses to start if
any composed profile still asks for external identifiers to be
pseudonymised, naming the profile. The check is on the profile and not on
the catalogues, because a catalogue can be registered after start-up and a
check on what is registered would be walked around by arriving late. The
deployment must choose: either the identifiers it receives are opaque, or it
configures a provider. It may not arrive at clear-text addresses in a locked
archive by omission. (Amended 2026-09-22: the first draft of this record
described a check on the registered catalogues.)

**The providers stay.** `local`, OpenBAO `transit` and the designed KMS
envelope remain, for a deployment that must be able to crypto-shred — a
contract that demands it, or a trail that unavoidably carries direct
identifiers. Choosing one stays a deployment decision, recorded by the
deployer.

**The `history` preset stops depending on keys.** It treated internal
actors as pseudonyms, so a tenant-facing view of activity needed a key
provider to render at all. It now omits internal actors: a staff actor is
shown by kind and role, never by identity. `external: scoped` stays, so a
tenant still sees its own people's identifiers.

## Consequences

- **A deployment's first installation needs no key infrastructure**: a
  bucket, a database, and the signing key for the chain.
- **Erasure of an end user is the product's**, in the product's database.
  The record keeps the opaque identifier, which GDPR Art. 17(3)(b) covers:
  the trail is kept to meet a legal obligation, and after the product's
  deletion the identifier resolves to nobody.
- **Resolve is off** where there are no keys, and the query service says so
  rather than returning nothing.
- **Turning keys on later is a re-key, not a switch.** Identifiers already
  written stay as they were written; a deployment that starts opaque and
  later needs pseudonyms gets them from that point on. This is stated
  because the archive cannot be rewritten.
- **The identity treatment a profile asks for is no longer what a copy
  necessarily gets.** `audit profile explain` reports the effective
  treatment per category, including the override, so what is actually
  written is inspectable.

## Alternatives considered

- **Pseudonymise external identifiers always.** The strongest default, and
  what 0005 assumed. It buys nothing when the identifier is already opaque,
  and the cost is a key provider in every deployment plus a permanent,
  unmigratable choice made before anyone knows whether they need it.
- **Pseudonymise staff too.** Would make an internal trail unreadable for
  the purpose it exists for, and the readers are the people who may know
  anyway.
- **Drop the providers.** Tempting while no deployment uses them, but a
  contract that demands crypto-shredding is a foreseeable ask, the code is
  built and tested, and it costs nothing to keep behind a default of
  `none`.
