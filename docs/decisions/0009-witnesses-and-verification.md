# 0009. Witnesses and verification

Status: accepted, 2026-11-21

## Context

A signed log proves that the signer signed it. It does not prove that every reader saw the same log. A compromised writer can ask for a signature over a history it changed, and show different histories to different readers. This is a **split view**. Only a party that cosigned the original checkpoint can expose it.

What a chain proves also depends on who holds the signing key. audit-v1 wrote this down, and it is still true. With a key that the writer holds itself, a chain proves that objects have not changed since they were signed, by a party who could also have chosen what to sign. With a key that never leaves its custodian (a cloud key service, a card), the writer cannot sign at will, and the chain proves what an auditor wants: the operator did not choose what to sign.

## Decision

### Witnesses

- **A witness is optional everywhere.** The configuration check warns when a log has none. The risk is written down: with no witness, a compromised writer can get a rewritten checkpoint signed. A compliance lock still prevents overwriting stored objects, but it does not prevent a split view.
- **Witness drivers are `none`, `peer` and `external`.**
  - `none` is a single signer.
  - `peer` lets the nodes of a cluster witness one another. On a cluster of three, the other two nodes are each log's peer witnesses, two of three.
  - `external` is a party outside the installation. It is a choice of party, not an integration of audit.
- **A witness speaks `tlog-witness` and signs `tlog-cosignature`** with an Ed25519 or ML-DSA-44 key. It signs through the `seal` block under its own purpose, so it holds nothing the writer holds and cannot write to the log.
- **`AuditWitnesses` declares the witnesses of each log and the cosignatures it requires** ([0015](0015-configuration-kinds.md)).
- **The writer asks every witness to cosign each new checkpoint of each log,** at most one request per witness and log every 5 seconds, so the window in which only the writer vouches for the log is about one seal. When a log is idle, the writer asks again for the latest checkpoint every 5 minutes. The cosignature's timestamp lets a reader tell a quiet log from a writer that stopped.
- **A restore re-registers the node with its peers and never restores a witness to an older state.**
- **Readers merge the logs of a node cluster by recorded time,** keep each log's own order, and break ties by log id. A reader shows each log's clock offset and claims no order across logs closer than it.

### What a verifier checks

Verification needs no key of audit and no running piece. It reads the log from the store and trusts neither the search index nor the writer's state. A verifier:

1. reads the checkpoint history, `checkpoints/<size>`, and refuses a name that is not a size, a file over the limit, a bad signature, a bad text, a size or origin that disagrees, two checkpoints of one size with different roots, and a checkpoint whose root is not the tree hash of its first entries ([reference/log-layout.md](../reference/log-layout.md));
2. checks each checkpoint's signature against the published log key, and each cosignature against a pinned witness key;
3. recomputes the tree from the tiles and checks consistency between checkpoints, so that a history only grew;
4. checks that an inclusion proof of an entry holds against a cosigned checkpoint.

- **A verifier pins the keys it trusts.** An auditor believes a key because they pinned it, out of band, and not because it is in the bucket. Nothing in the bucket can add a key to the verifier.
- **A verifier accepts more than one log key for a log, each with the checkpoint size from which it applies,** so that a key that rotated on compromise does not break the history.
- **The decryption of a record is a separate step** and needs the `seal` block ([0011](0011-evidence-encryption-and-pseudonyms.md)). A log verifies after its records stop opening.
- **The command is `audit verify`** ([0014](0014-the-front-the-console-page-and-audit-verify.md), [reference/audit-verify.md](../reference/audit-verify.md)).

### What the log proves, by who holds what

| Holder | What the log proves |
|---|---|
| The writer holds the log key itself | Objects have not changed since signing. The writer could also have chosen what to sign. |
| The `seal` block holds the log key, and nothing cosigns | The writer cannot sign at will. A split view is possible if the writer is compromised. |
| The `seal` block holds the key, and a witness cosigns | A split view is exposed by a party that cosigned the original. |
| The lock is `compliance` | No stored object can be overwritten or deleted before its retention ends, by anyone. |

### Built and planned

Built: the checkpoint history check and the verification of one log key over a directory (in sluis's CLI). Planned: the `tlog-witness` exchange and the cosignature check, several keys for one log, the check over a bucket, and the `peer` and `external` drivers.

## Consequences

- Witnesses are an addition and not a prerequisite. A single-node installation runs with none, and a warning.
- A verification run reads the whole history. Its memory grows with the number of checkpoints and nothing else.
- A witness adds a dependency for each log and a state to keep. The restore rule keeps it from going back.

## Roads not taken

- **A public transparency log as the only witness.** It needs a service with its own storage, and ties audit to its availability. `external` allows it as a choice.
- **Time-stamp anchoring (RFC 3161) as the main protection.** It proves when a hash existed, not that every reader saw the same history.
- **A required witness everywhere.** A single-node installation would have no way to run. The lock rule ([0010](0010-lock-modes-retention-and-compliance-floors.md)) makes a witness required where the lock is weak.
- **Hash-chained rows in a mutable database.** It detects edits but lives in a store that its administrator can change.
