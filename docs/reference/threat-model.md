# Threat model

What audit is designed against. Each threat names the asset, the attacker's position, the control that stops it, what is left after the control, and the requirement rows that prove the control. The page describes. The [records](../decisions/README.md) decide, and each control links the record that holds it.

The scope is one audit of one scope: its pieces, its logs and its index. The identity product, the key host and the blob store are neighbours, and their own threats are theirs.

## Assets

| Asset | Where it lives |
|---|---|
| The logs: the evidence | Blob store, one bucket for each project, written once ([0008](../decisions/0008-the-log.md)) |
| The log keys and witness keys | Behind the `seal` block, never exported ([0011](../decisions/0011-evidence-encryption-and-pseudonyms.md)) |
| The tenants' `evidence` and `pseudonym` keys | Wrapped, in the `seal` block |
| The search index | A database, derived and rebuildable ([0012](../decisions/0012-search-and-the-reader-api.md)) |
| The usage records | The usage log, never dropped ([0006](../decisions/0006-the-usage-class.md)) |
| The configuration | Signed documents ([0015](../decisions/0015-configuration-kinds.md)) |

## Attacker positions

| Position | Who |
|---|---|
| **P1** Outsider | Anyone who reaches the front |
| **P2** Compromised emitter | Code execution in an emitting component or product |
| **P3** Compromised piece | Code execution in the writer, the witness, the indexer or the reader |
| **P4** Compromised store | The blob store, the index database or their accounts |
| **P5** Reader with a grant | A person or tenant that may read some records |
| **P6** Operator | The person who runs the installation |

## Threats

| Id | Threat | Asset | Position | Control | What is left | Requirements |
|---|---|---|---|---|---|---|
| **A-1** | A piece writes into another project's bucket, or rewrites a written object | The logs | P3 | Every credential is scoped to its project's bucket and prefix. Every object is created only if absent, and no credential carries a delete right ([0008](../decisions/0008-the-log.md)). | Objects not yet written can be raced for. Witnesses detect a fork. | REQ-AUDIT-103, 141, 143, 050 |
| **A-2** | An emitter sends records as another emitter | The logs | P2 | The transport names the emitter from its platform identity and never from a field of the message ([0002](../decisions/0002-pieces-and-holders.md)). Each project has its own stream. | A sender may flood its own stream. Limits bound it. | REQ-AUDIT-148 |
| **A-3** | A compromised writer signs a rewritten history, or shows different readers different histories | The logs | P3 | The writer cannot sign at will: the `seal` block signs, and lets only that writer use its key. A compliance lock stops overwriting. A witness that cosigned the original exposes a split view ([0009](../decisions/0009-witnesses-and-verification.md)). | With no witness, a split view is possible. The configuration check warns, and a production log needs a compliance lock or a witness ([0010](../decisions/0010-lock-modes-retention-and-compliance-floors.md)). | REQ-AUDIT-040, 041, 010, 030, 058 |
| **A-4** | The `seal` host is compromised | The keys, and the records it can open | P3 | The host rate-limits and audits every signature. The keys stay in the card or key service. A log key rotates on compromise, and a verifier accepts several keys, each from a checkpoint size ([0009](../decisions/0009-witnesses-and-verification.md)). | It can sign and open within its rules until the log shows it. | REQ-AUDIT-041, 163 |
| **A-5** | The blob store or index database is compromised, or rolled back | The logs, the index | P4 | A sealed record is ciphertext bound to its log. The tree covers the ciphertext. A rollback of the whole store shows only against the witnesses. The index is never evidence and is rebuilt from the log. | Metadata: ids, sizes, times, access patterns. Deletion and denial of service. A rollback with no witness. | REQ-AUDIT-016, 056, 055 |
| **A-6** | A reader of one project or tenant reads another's records | The logs, the index | P5 | Access is checked twice: the reader's Cedar decision filters every query, and the stores below it scope every credential. Row-level security on the index ([0013](../decisions/0013-access.md)). | What its bindings allow. **Open:** the second check on Open ([0013](../decisions/0013-access.md)). | REQ-AUDIT-144, 145, 069, 143, 073 |
| **A-7** | A tenant that leaves keeps its data readable, or an erased person can be found | The records, the index | P6, P5 | The exit deletes the personal-data and `pseudonym` wraps at once. The `evidence` wrap goes at the end of the evidence retention. The index purges the tenant's rows ([0011](../decisions/0011-evidence-encryption-and-pseudonyms.md)). | Copies taken before the exit, and the months of linkable pseudonyms of a person whose mapping was deleted. | REQ-AUDIT-068, 057, 064 |
| **A-8** | A forged or replayed usage record changes a bill | The usage log | P2 | An emitter is authenticated by its platform identity. A usage record has an idempotency key, so a retry is stored once. The operation fails when the record cannot be stored ([0006](../decisions/0006-the-usage-class.md)). audit holds no balance. | A compromised product emits false usage for its own customers, visible in the log. | REQ-AUDIT-189, 190, 191, 109 |
| **A-9** | The trail becomes a store of personal data | The records | P2, P6 | The emitter refuses names, e-mail addresses, secrets and tokens. A person is a keyed pseudonym made before the record leaves the process. The writer validates ([0004](../decisions/0004-the-record-format.md)). | A pseudonym links one person's records within a month. | REQ-AUDIT-003, 023, 128, 046 |
| **A-10** | The operator reverses a tenant's pseudonyms | A person's privacy | P6 | Only the tenant's own audit role may reverse, and every reversal is audited under the asker's name ([0011](../decisions/0011-evidence-encryption-and-pseudonyms.md)). | A key leak lets its holder confirm a subject they already know. | REQ-AUDIT-130, 131 |
| **A-11** | A flaw in query parsing writes to the index, or the index poisons the reader | The index | P1, P3 | The reader has a read-only credential, and the indexer is a separate piece. The front parses internet input and holds no key ([0014](../decisions/0014-the-front-the-console-page-and-audit-verify.md)). | Denial of service of search. | REQ-AUDIT-073 |
| **A-12** | A hostile or buggy catalogue costs the writer without bound | The writer | P2, P6 | The catalogue is signed, and held to a policy with a bounded cost of validation and a version ([0004](../decisions/0004-the-record-format.md)). A validation that times out three times is refused, never blocks the log. | A catalogue within the policy that is expensive. The cost bound limits it. | REQ-AUDIT-113, 146, 147 |
| **A-13** | A wrong clock moves usage across periods, or defeats duplicate removal | The usage log, the writer | P2, P6 | The time of use counts only within 72 hours behind and 5 minutes ahead of the recorded time. The writer's time checks run only on a trusted clock, and the seal entry says whether they ran ([0007](../decisions/0007-meters.md), [0008](../decisions/0008-the-log.md)). | A clock that is wrong and reported synchronised from the start is taken as right, within at most 14 days of error. | REQ-AUDIT-157, 158, 017, 036 |
| **A-14** | Records are lost when the audit path fails | The logs | P2 | Security facts: a loss is counted and reported in the tenant's next record. Usage and credential-issuing facts fail their operation ([0005](../decisions/0005-the-security-class.md)). | A process that exits with an unreported total takes it with it. | REQ-AUDIT-186, 187, 015, 189 |
| **A-15** | A compromised front acts for a person | The reads | P3 | The front holds no key, secret or state and has no outbound network. The reader decides again with its own policy. A token for one scope or client is refused elsewhere ([0014](../decisions/0014-the-front-the-console-page-and-audit-verify.md)). | It can refuse service and alter the pages it serves. | REQ-AUDIT-082 |
| **A-16** | A mistyped tenant id creates a tenant | The tenants | P2 | A record for an unknown tenant is held in the transport and raises an alarm ([0013](../decisions/0013-access.md)). | **Open:** the registry ([0003](../decisions/0003-the-neutral-model.md)). | REQ-AUDIT-154 |
| **A-17** | A record is injected into a log's entry stream and kept twice, or after a long delay | The logs | P2 | Duplicate removal by record id for 14 days, quarantine of stale and future messages, a damaged segment stops the writer ([0008](../decisions/0008-the-log.md)). | A repeat after the window is written twice. | REQ-AUDIT-034, 052, 053, 178, 179 |

## Accepted risks

- **No witness.** A single-node installation can run without one, and the check warns.
- **A compromised operator who holds both the key host and the store.** The design does not claim to detect this. A witness outside the operator's control is the answer.
- **The envelope.** Who did what kind of thing, when, and with what outcome is in clear by design. It is what lets the operator run the system without reading a tenant's records.
