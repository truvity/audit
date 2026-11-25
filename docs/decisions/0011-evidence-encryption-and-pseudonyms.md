# 0011. Evidence encryption, pseudonyms and crypto-shredding, through atoom's `seal` block

Status: accepted, 2026-11-21. Partly superseded by [0018](0018-the-pseudonym-domain-label-is-the-products.md): the domain label is per product, configured by the product; sluis keeps `sluis-pseudonym-v1`. Partly superseded by [0020](0020-the-writer-moves-here-first-and-reads-nats-jetstream.md) (proposed): the host of `seal` for audit alone is `audit-seal`, beside each writer instance

## Context

A log is written once and kept for years, and nothing in it can be deleted. Two things follow.

- **A person can lawfully ask to be forgotten,** and a tenant that leaves must leave nothing personal behind. A locked log cannot be edited, so what is personal must be something that can be made meaningless later: a pseudonym under a key, and a key that can be destroyed.
- **Evidence outlives personal data.** A bill or a security fact can be disputed after a tenant has left. The evidence must stay readable for its retention, and then go.

audit does not hold keys. It uses atoom's [`seal` block](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md): purposes, data keys per tenant, wraps, `Seal`, `Open`, `Mac` and signing by purpose. In the first plan the block was called `keyring`. It is `seal` now, so that the name `keyring` stays with the OpenPGP file of ritueel. The block is built from the key and store ports, and its host supplies the rules.

## Decision

### The purposes audit declares

The purposes a block serves are declared by its host, and the key port takes any purpose a host declares. audit declares four:

| Purpose | What it does | Who may use it |
|---|---|---|
| `log` | Signs the checkpoints of a writer's logs. One key per writer instance. | That writer instance, to sign |
| `witness` | Signs the cosignatures of a witness. | That witness, to sign |
| `evidence` | Seals every entry of a log, under the tenant's key. | The writer: Seal, never Open. The indexer and the reader: Open, under a grant ([0013](0013-access.md)). |
| `pseudonym` | Makes a person's pseudonym with `Mac`. | Emitters and the reader, `Mac` only. The key never leaves the block. |

- **The seal host's rules say who may use which purpose.** Where audit runs beside sluis, sluis's key service hosts the block. Where it runs alone, audit needs a host of its own. **Open: the host of `seal` for audit alone.** Whether audit ships its own host process, and how it is packaged, is not decided.
- **A log key rotates on compromise only.** A tenant's `evidence` key rotates by generations, with wraps kept until the evidence retention ends.
- **Planned work in atoom:** the purposes move from the key port to the block ([atoom's plan](https://github.com/truvity/atoom/blob/master/docs/plan.md#11-after-020-the-blocks-phase)). Until then, the port's closed list applies and sluis's key service serves audit.

### Evidence encryption

- **Each entry is an envelope in clear plus the record,** sealed under the tenant's `evidence` key. The seal is bound as authenticated data to the scope, the tenant, the project, the log and the record id, so a record copied to another log or tenant does not open.
- **The writer may Seal under `evidence` and never Open,** so it holds nothing that decrypts what it wrote. Only the indexer and the reader may Open.
- **A record that concerns no tenant** belongs to the scope's own tenant and is sealed under its `evidence` key. Records are routed to a tenant at write time.
- **The envelope uses OCSF field names:** `metadata.uid`, `time`, `metadata.tenant_uid`, `class_uid` and `activity_id`, `status_id`, `actor.user.type_id`, plus the writer's stamps. It leaves out `metadata.event_code`, which says more than the class does. Apart from the stamps it is a projection of the record that the writer computes before sealing, so a reader that opens the record checks that the two agree and catches a writer that lies in the envelope. A usage entry has the same form: the id, the time, the tenant and the event `type` in clear, the event sealed.
- **The tree's leaf is the hash of the whole entry, ciphertext included,** and the record's own RFC 8785 hash is inside the ciphertext, so a log verifies after its records stop opening.
- **Evidence and personal data never share a key.** Entries name people only by pseudonym and carry no name, address or token. The personal data that would make them readable about a person lives under the product's keys for the tenant, outside audit.
- **The operator's own view of a tenant's records is the envelope:** the class, the outcome and rotating pseudonyms.

### Pseudonyms

- **A person or an external actor is recorded under a keyed pseudonym,** never by name, e-mail address or token. A secret appears as a fingerprint and a revision.
- **A pseudonym is `p1.<epoch>.<value>`.** `<epoch>` is the UTC month as `YYYYMM`. `<value>` is the first 128 bits, base64url without padding, of the keyed hash that `Mac` makes with HMAC-SHA384 under the tenant's `pseudonym` key of that epoch.
- **The input is domain-separated:** a label, then the scope, the tenant, the kind of identity and its stable identifier, each part prefixed with its length as a 4-byte big-endian integer. The identifier is the subject for a person; the upstream's issuer and subject for an identity with no assigned subject; the workload id or client id for a workload or client; a product's own stable subject id for a person of a product; and the vendor and the vendor's account id for an **external account**, a person of another tenant known to this tenant only as a member of a container it owns.
- **Open: the domain label.** sluis's emitter binds `sluis-pseudonym-v1` today. A neutral label changes every pseudonym that sluis's alpha wrote. The choice is made when the emitter moves here.
- **An epoch is a generation of the tenant's `pseudonym` key,** made at the start of every UTC month. Within a month the records of one person link to each other. Across months they do not, without reversal. A suspected compromise of the key starts a new epoch at once, and past records keep their pseudonyms.
- **The pseudonym is made where the record is made.** The emitter asks the block for it before the record leaves the process, so a raw subject never reaches a transport, a spool or the writer. A product's emitter is a declared caller of the block for `Mac` under the `pseudonym` purpose and nothing else, known by its platform identity.
- **Only the tenant's own audit role reverses a pseudonym.** The reader answers two questions for a person whose grant carries the tenant's declared reversal permission: the pseudonyms of a subject the person names, for a range of epochs; and whether a given pseudonym is that of a subject the person names, in its epoch. Each reversal is audited under the asking person's name. The operator of the installation cannot reverse a tenant's pseudonyms.
- **The mapping from a pseudonym to a person lives outside the log, and erasure of a person deletes it.** The pseudonym leads to the subject, and the subject leads to a person only through the person's records, which live outside the log. Once those are erased, the person's pseudonyms name no one. There are no per-subject keys: the tenant's `pseudonym` key is the only key that unlinks the log.
- **Each epoch's generation is kept for the longest retention of the logs that hold pseudonyms it made,** and is deleted then. Every generation goes at the tenant's exit.

### A tenant's exit

- **The exit deletes the tenant's `pseudonym` wrap, and every personal-data wrap its product keeps, at once.** Its pseudonyms can no longer be made or reversed, and its people's records are gone, so its records name no one. The records stay in the logs, sealed and verifiable, and readable as evidence.
- **Its `evidence` wrap is deleted when the evidence retention ends,** the latest end of the declared retention of any log of the scope that holds the tenant's entries ([0010](0010-lock-modes-retention-and-compliance-floors.md)). After that nobody can read the tenant's records again, and the logs still verify.
- **The search index purges the tenant's rows at the exit,** and every later rebuild skips them ([0012](0012-search-and-the-reader-api.md)).

### Built and planned

Built: evidence sealing, pseudonyms and the epoch rule, in sluis through its key service. Planned: the `seal` block in atoom and the move of purposes, audit's host for the block, the reversal calls of the reader, the rule of an external account across tenants.

## Consequences

- An auditor verifies a log after the tenant has left and after its records stopped opening. Nobody can read a departed tenant's records after the evidence retention.
- The operator sees a person's activity linked within a month and not beyond. An investigation across months needs the tenant's reversal role, and every reversal is visible in the log.
- A leaked pseudonym key lets its holder confirm whether a subject they already know appears in that tenant's month. It reveals no subject and forges no record.
- Records of one person stay linkable to each other within each month after the mapping is deleted. A contract that demands per-subject shredding cannot be met.
- The envelope leaks metadata by design: who did what kind of thing, when, and with what outcome.
- Sealing a record is a call per record, batched, and served from the block's cache of opened data keys.

## Roads not taken

- **Records sealed by HPKE to a public key per tenant, so the writer encrypts without the block.** A second sealing path beside the block's, and a private key per tenant to hold somewhere. The block's Seal-only rule for the writer gives the same property.
- **Records under the tenant's data key, with usage under a key of its own.** A tenant's exit would destroy the security evidence at once, and a dispute after the exit could not be answered from the log.
- **Delay the shred of personal data until the evidence retention ends.** Personal data would be kept for years after an exit, against any erasure promise.
- **A key per (tenant, subject).** Millions of keys and a key lookup per record.
- **A plain hash of the subject.** Anyone with a list of subjects could reverse it without any key.
- **One pseudonym key for the life of the log.** One person could be followed through every record for years by whoever reads the envelopes.
- **A stored mapping table from pseudonym to person.** A second store of personal data to protect, back up and erase, where the keyed function already gives the mapping on demand.
- **Pseudonymising in the writer.** Every transport and spool would then carry raw subjects.
- **Format-preserving encryption of the subject.** Reversible by whoever holds the key, without the person's record, so erasure could not unlink it.
- **No keys by default, as audit-v1 had.** audit-v1 made pseudonymisation optional because its identifiers were often opaque. Here a person is always a pseudonym, and the cost is one `Mac` call per person per record.
