# Keys: what they are for, how many, where they live

**Pseudonymisation keys are off by default.** `keys.provider: none` is what a
deployment runs unless it says otherwise: no key directory, no login to a
secret manager, no `identity/` prefix, and resolve refused as unimplemented.
(`keys.provider: none` and `external_identifiers_are_opaque` are not built
yet: they arrive with the rewrite this documentation specifies, and the
chart's default today is `local`.)
Most installations need none of it, because the identifiers they record are
already opaque and the staff they record are meant to be readable
([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)). Such a
deployment declares `external_identifiers_are_opaque` instead, and skips
everything on this page about pseudonymisation.

Everything below about pseudonymisation keys applies to **a deployment that
must be able to crypto-shred**: one whose contract demands erasure of an
identifier from a locked archive, or whose records unavoidably carry direct
identifiers. That is a deliberate choice, made once and recorded by the
deployer, and it cannot be undone or migrated afterwards.

The **signing key is different**: every installation has one, whatever it
does about pseudonyms, because without a signed digest chain the archive
proves nothing to anybody outside. [Signing key](#signing-key) is that half
of the page.

| kind | what it does | how many | who holds it | ends by |
|---|---|---|---|---|
| **pseudonymisation** (symmetric), off by default | turns an identifier into a stable pseudonym; seals the identity behind it | one per **tenant × purpose**, created on first use | the writer (make), resolve (open) | destruction — that is erasure |
| **signing** (asymmetric), always | signs the hourly digest chain | one per installation | the digest job only | replacement, with the old public half kept |

Neither is ever rotated, and that is deliberate.

## Why keys at all

The archive is write-once under Object Lock, for years. Two things follow.

- A person can lawfully ask to be forgotten, and some profiles must not hold
  a directly identifying name in the first place. Nothing in the archive can
  be deleted or edited, so where a record does carry such a name, the only
  way to honour that is to write something that **can be made meaningless
  later**: a pseudonym under a key, and destroy the key. Where the
  identifier was opaque to begin with there is nothing to make meaningless,
  which is why the default is no keys at all.
- Object Lock stops deletion. It does not prove the trail is complete and
  genuine to anyone outside: a record can be added beside the real ones, a
  quiet hour cannot be told from an emptied one, and a copy (an export, a
  replica, a restored backup) carries no lock at all. A **signed chain of
  digests** proves all three, to anyone with the public key.

## Pseudonymisation keys

### The requirement

- The **same person, same tenant, same purpose** always gets the same
  pseudonym, for the whole retention: that is what lets an investigation
  follow one actor through years of records.
- The **same person under two purposes** gets two unrelated pseudonyms. The
  security copy and the billing copy of one event cannot be joined on a
  person, which digital-identity regulation requires of a wallet provider.
- Nobody without the key can compute, check or reverse a pseudonym. A plain
  hash fails this: hash every e-mail you know and compare.
- Destroying one tenant's key for one purpose erases exactly that tenant's
  people in that purpose, and nothing else.
- The way back (resolve) exists for the cases the law requires, is available
  to almost nobody, is itself recorded, and dies with the key.

So the key model is **one key per (tenant, purpose)**, where a purpose is a
profile's name. A deployment with two profiles and a thousand tenants has two
thousand keys. They are created on first use — the first record for a tenant
in a profile — never in advance.

### How a key is used

For a record whose profile says an actor kind is pseudonymised:

    pseudonym = "ps_" + base64url( HMAC-SHA256( key[tenant, purpose], identifier ) )

The identifier is what the emitter sent (`alice@example.com`, a user id).
The writer replaces it with the pseudonym in that profile's copy. Other
profiles that keep the kind in clear keep the identifier.

For resolve, the writer also **seals** the identifier under the same key
(AES-GCM, with a sealing key derived from the data key so one key never
serves two algorithms) and stores the sealed form in the archive beside the
records, at `identity/tenant=<t>/purpose=<p>/<pseudonym>`. Opening it needs
the same key. A deployment that wants no way back turns this off
(`ForgetIdentities` / `--keep-identities=false`).

### Never rotated

Rotating a pseudonymisation key would give every person a second, unrelated
pseudonym from the rotation on, and break the one property the security copy
is kept for. The threat rotation answers (a leaked key) is answered here by
where the key lives (below), not by changing it.

### Destroyed, and the tombstone

`audit key destroy --tenant <t> --purpose <p> --by <who> --reason <why>`
is erasure. It refuses while a legal hold covers the tenant, and refuses
without a writer to record `audit.key.destroyed` through: an erasure the
trail does not mention is one nobody can prove was lawful.

After it, the pseudonyms in the archive stay, and nothing can ever compute
or match them again, and every sealed identity is unreadable. The provider
leaves a **tombstone** — a marker that the key existed and is gone — so that
a later record for the same tenant does not quietly mint a fresh key and hand
the same person a new identity. The marker is checked before any key is
created.

### Where the keys live: three providers

**`local`.** Each data key is 32 random bytes, generated by the writer,
wrapped (AES-GCM) under a 32-byte **root** the deployment supplies in a
Secret, and stored as a file in a directory. The root never leaves the
Secret; the directory holds only wrapped keys and tombstones. Two writers
sharing one directory agree (create-once via link, so a race mints one key,
not two); two writers with separate directories do **not**, and the writer
refuses that. Losing the directory is losing every tenant's pseudonyms
silently, so the chart insists on persistence. It is right for tests and
for a single instance; a production deployment should want one of the
other two.

**`transit` (OpenBAO or Vault).** There is no root and no directory. Each
(tenant, purpose) is a **key inside the transit engine**, named
`<prefix>.<purpose>.<tenant>`, created by the writer on first use through
the encrypt endpoint. The key material never leaves the engine: a pseudonym
is the engine's HMAC of the identifier under that key, a seal is the
engine's encryption, both pinned to the key's first version. What OpenBAO
adds, concretely:

- **No shared state between replicas.** Every writer asks the same engine,
  so any number of replicas agree without a shared volume.
- **Nothing to back up or lose.** The keys are in the engine's storage,
  covered by its own snapshots and DR.
- **Scope by policy, per role.** The engine's policy decides who may do what
  with which purposes: the writer may `hmac` and `encrypt` on its purposes
  and touch nothing under `transit/keys`; the query service's resolve role
  may `decrypt` and nothing else; a metering role gets its purpose only;
  only a human eraser role reaches `rotate`, `config` and `trim`. A leaked
  writer credential can make pseudonyms and cannot open or erase any.
- **No stored credential.** The writer signs in with the pod's projected
  service-account token on a JWT auth mount, so there is no long-lived
  token to leak or rotate.
- **Erasure as a tombstone the engine keeps.** Destroy rotates the key
  once, raises the minimum usable version past the first, and trims the
  first version away. The key remains, unusable for anything written under
  it, and cannot be re-created; a tenant destroyed before it was ever seen
  gets a key made and destroyed at once.

The full engine set-up and the policies per role are in
[OpenBAO keys](openbao-keys.md).

**`kms` (AWS KMS envelope) — designed, not built.** One customer-managed
KMS key per deployment is the root. Each (tenant, purpose) data key is
minted by `GenerateDataKey` with an encryption context naming the tenant and
purpose, and the wrapped copy is stored in a key table (the index database)
beside a tombstone column. The writer unwraps into memory on first use; a
processor's IAM role is scoped by the encryption context to the purposes it
may unwrap. It is the same model as `local` with KMS as the root and a
database as the directory, and it is the choice for a deployment with no
OpenBAO. Not a single KMS key with derived children: KMS cannot HMAC per
tenant without a key per tenant, and per-tenant KMS keys would cost and
count in the thousands.

### Which one

- Anything that does not have to crypto-shred: `none`, the default.
- Tests, a laptop, a single-instance trial that does: `local`.
- Anything with more than one replica, or where secrets already live in
  OpenBAO: `transit`.
- A deployment on AWS with no OpenBAO: `kms`, once built.

The choice is permanent for a deployment. Moving keys between providers
would mean either re-keying every tenant (a new identity for every person)
or exporting key material, which two of the three providers exist to make
impossible.

## Signing key

### The requirement

Every hour the digest job writes, per profile, one small object listing
every archive object written in that hour with its SHA-256, the previous
digest's hash and signature, and its own signature. `audit verify` walks
the chain with the **public** half and reports any object changed, any
object nobody accounted for, and any hour missing — with nothing that has to
be trusted.

The signing key must be one the **writer cannot use**. Whoever can write the
archive and also sign digests for it can choose what to sign. So the digest
job has its own identity and its own credentials, and the writer's have no
path to the key.

### Where it lives

- **A key file** (ed25519 PEM in a Secret): fine for a trial; the private
  half is then in the cluster.
- **AWS KMS** (`ECC_NIST_P256`, sign/verify): the private half never leaves
  KMS; the digest job's role may `kms:Sign`, the writer's may not.
- **OpenBAO transit** (ed25519): the same separation for a deployment whose
  secrets live in OpenBAO; the job's policy may `transit/sign/<key>`.

`audit key public` prints the public half for the verify job and for any
auditor.

### Replaced, not rotated

The chain can survive a new signing key: each digest names the key version
it was signed with (`KeyID`), and verification with the right public half
per version still walks the whole chain. What must never happen is losing a
public half: keep every one that ever signed, beside the archive, for as
long as the archive lives.

## What a deployment supplies

| provider | Secret or engine | rights |
|---|---|---|
| `none` (default) | nothing | — |
| `local` | a 32-byte root in a Secret; a persistent, shared directory | — |
| `transit` | an OpenBAO transit mount in the environment's namespace; a JWT role per component | writer: `hmac`, `encrypt` on its purposes; resolve: `decrypt`; eraser (human): `read`, `update` on `transit/keys/<prefix>.*` |
| signing, KMS | one asymmetric key | digest job: `kms:Sign`, `kms:GetPublicKey`; verify job: `kms:GetPublicKey` |
| signing, transit | one ed25519 transit key | digest job: `transit/sign/<key>`, `read` on the key |
