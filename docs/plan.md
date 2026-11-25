# The plan

What is built and what comes next, in order. The decisions are the [records](decisions/README.md), and the testable rules are the [requirement rows](requirements/audit.yaml). The ports and blocks that audit is built on follow [atoom's plan](https://github.com/truvity/atoom/blob/master/docs/plan.md); its step names (B7, B9, B12, ...) are used here as they are there.

## Built

- **The emitters, security part.** The Go module `github.com/truvity/audit/emitter` and the Rust crate `audit-emitter`, moved from sluis with the same bytes and held to the same shared vectors ([emitter/](../emitter/README.md), [emitters](reference/emitters.md)). They build OCSF security records with the three calls, the counted loss and pseudonyms through a `Mac` the host supplies.
- **The release flow and three releases:** 0.17.0 (the first, with the emitters), 0.18.0 and 0.19.0 (atoom 0.4.0 and 0.5.0). See [CHANGELOG.md](../CHANGELOG.md) and [releasing](release.md).

Not built here: the usage part of the emitters, and every process. The writer, the first indexer and reader, and `audit verify` run inside [sluis](https://github.com/truvity/sluis) today (the writer moves here in step 1); the blob, transport and index ports and their first drivers are atoom's.

## The steps

In order. Each step is one pull request or a few. A step that needs an atoom step starts when that step is released. The owner's decision D90 A (2026-11-25) put the writer's move first and usage second ([0020](decisions/0020-the-writer-moves-here-first-and-reads-nats-jetstream.md)): session's development site sends its records to NATS JetStream, and nothing reads them until the writer runs outside sluis.

| Step | What | Needs |
| --- | --- | --- |
| 1 | **The writer moves here** from sluis: a Go service in its own image that reads NATS JetStream, with audit's own `seal` host, the documents through the source port, and mimic's action table | atoom 0.9.0 (B12, with `blob/s3`) to deploy; besides it, released atoom only: `transport/nats`, `source/file`, the signed document verifier, the `seal` block, `store/dynamodb`. Not B7 |
| 2 | **Usage**: the usage part of the emitters, a writer path that never drops a usage record and stores it once, and the redelivery test of each at-least-once transport | Step 1; the transports as built (spool, `transport/nats`, `transport/sqs`) |
| 3 | **The indexer and the reader move here** from sluis | B12; B15 for more index backends |
| 4 | **`audit verify` moves here** from sluis's CLI | step 1's log layout; `blob/s3` (atoom 0.9.0) for the check over a bucket |
| 5 | **The witness**, new | step 1 |

### 1. The writer

The decision and the owner's choices are in [0020](decisions/0020-the-writer-moves-here-first-and-reads-nats-jetstream.md).

- **The code.** sluis's `actors/writer` moves as the module `github.com/truvity/audit/writer`, with its state, segments, quarantine and the fence ([0008](decisions/0008-the-log.md)) unchanged, and its checks over the emitter module. Only the side facing sluis changes: its store, key and policy clients.
- **The transport.** atoom's Go `transport/nats` in the process (`--transport-nats`), or the receive socket of a driver beside it (`--transport-socket`, the spool on a board); exactly one. It speaks B12's generic message, whose wire bytes are today's.
- **The configuration.** `AuditLog` and `AuditCatalogue`, defined here as audit's kinds and signed as [0015](decisions/0015-configuration-kinds.md) asks, read through the source port (`source/file`, or `source/appconfig`) and verified with atoom's signed document verifier. B7 adds a third path; the documents do not change.
- **The keys.** `audit-seal`, audit's host of atoom's `seal` block, beside each writer instance: the writer signs under `log` and seals under `evidence`, and never opens.
- **The blob.** atoom's Go `blob/s3` in the process from the start, with S3's conditional writes; it is built in atoom's B12 and released in 0.9.0, and the writer deploys on that release (the owner's D107 B and D112 A).
- **The shape.** On Kubernetes, a Deployment of two replicas with one active: the active one holds a lease in the store port, renews it well before it expires and stops writing when it cannot; the standby takes over after the expiry; the lease's epoch is in the state, the checkpoints and the seal entries (D112 A).
- **The image.** `audit-writer` and `audit-seal` in the forms of [0016](decisions/0016-distribution-and-the-lambda-shape.md) except the Lambda app zip, which needs `blob/s3`, `transport/sqs` and the SQS pass-through test.
- **The first deployment.** One writer of two replicas per project on a shared Kubernetes cluster of a development environment: session's development site, and mimic. Its shape is the deployment repository's, in a pull request of its own. session publishes its catalogue for its project.
- **mimic's action table** (the owner's D91 A): `mimic.identity.create`, `lease`, `use` and `revoke`, the actor the real subject and the target the test identity ([0020](decisions/0020-the-writer-moves-here-first-and-reads-nats-jetstream.md#mimics-action-table)).
- **sluis's switch.** sluis keeps its writer, frozen, until its key service hosts the `seal` block and its documents reach the writer; it then runs audit's writer and continues its logs.
- **Done when:** the writer runs from this repository's image on the development cluster, reads session's stream, seals session's records into a log that `sluis audit verify` checks, and refuses nothing that session's catalogue declares; and the writer's tests that moved from sluis pass here.

### 2. Usage

The usage class is decided ([0006](decisions/0006-the-usage-class.md), [0007](decisions/0007-meters.md), [0017](decisions/0017-the-organisation-of-a-usage-record-is-optional.md)) and not built. It comes right after the writer's move ([0020](decisions/0020-the-writer-moves-here-first-and-reads-nats-jetstream.md), the owner's D90 A), so that the usage path is built once, in audit's writer.

- **The emitters, Go and Rust.** One call builds the security record and its usage events, with one message id; each event is a CloudEvents 1.0 event in RFC 8785 form, with the id `<message id>/<n>`, the customer and the product, the organisation only when the product knows it, and no personal data. The usage part always uses the required call: the operation fails when the transport does not accept the message durably. Meters come from the action table ([0007](decisions/0007-meters.md)). Both languages pass the same new vectors.
- **The writer path.** A usage record is never dropped, never buffered in memory and never counted as lost. The writer refuses a message whose usage part is malformed ([0017](decisions/0017-the-organisation-of-a-usage-record-is-optional.md): no customer, no product, an empty organisation, an undeclared meter), stores a repeat of a message id once, and writes the usage log beside the audit log in the same conditional write of its state ([0008](decisions/0008-the-log.md)). This half lands in audit's writer of step 1. sluis's writer, frozen until sluis switches, takes no usage path, so sluis emits no usage until it runs audit's writer.
- **The transports.** Delivery is at least once. For each at-least-once transport, a test redelivers a message that carries usage, after the writer stored it and before the transport saw the acknowledgement, and finds exactly one usage record in the usage log. The spool is tested first, then atoom's Go `transport/nats` and `transport/sqs`, both released in atoom 0.8.0 with the transport kit's redelivery case.
- **atoom B12** makes the transport message generic: the `usage` field goes, and the payload is opaque. The usage part rides the `usage` field until then, and moves into the payload in the change that adopts B12. The records do not change.
- **Not in this step, and not in audit:** the counters made from usage facts and the limit check. They are sluis's ([sluis record 0046](https://github.com/truvity/sluis/blob/main/docs/decisions/0046-limits-on-the-request-path-and-usage-in-audit.md)) and are built in the Quota milestone of [sluis's roadmap](https://github.com/truvity/sluis/blob/main/docs/roadmap.md#customer-identity-and-quota), after atoom's `state` block (B7). audit supplies the records the counters are made from, and nothing else.
- **Done when:** both emitters build a usage part from one call and pass the shared vectors; an operation whose usage part the transport does not accept fails; a redelivered message gives one usage record, on the spool, on NATS JetStream and on SQS; and the writer refuses and accepts the cases of [0017](decisions/0017-the-organisation-of-a-usage-record-is-optional.md).

The requirement rows of this step (the YAML has no field for a step, so they are listed here):

| Part | Rows |
| --- | --- |
| The emitters | REQ-AUDIT-095, 108, 110, 132, 189, 190 |
| The writer path and the usage log | REQ-AUDIT-093, 109, 126, 166, 167, 168, 191, 193, 194, 195 |
| The transports | REQ-AUDIT-014 (built for the audit log; the redelivery test adds usage) |

The other usage rows belong to later steps: the witness's cosignature of the usage log (REQ-AUDIT-096) to step 5, the period rule of readers (REQ-AUDIT-157, 158) to step 3, and the bucket rows (REQ-AUDIT-142, 143) to steps 1 and 3.

### 3. The indexer and the reader

- Both move from sluis ([0012](decisions/0012-search-and-the-reader-api.md)), with the access check over atoom's Cedar loader, which is built ([0013](decisions/0013-access.md)).
- They follow the generic index contract of step B12. `index/rqlite` and `index/postgres` are step B15.
- The embedding of the console page stays sluis's ([0014](decisions/0014-the-front-the-console-page-and-audit-verify.md)).

### 4. `audit verify`

- The command moves from sluis's CLI (`sluis audit verify`) and takes its own name ([0014](decisions/0014-the-front-the-console-page-and-audit-verify.md)).
- The check over a bucket needs `blob/s3` (atoom 0.9.0). The check of cosignatures comes with step 5.

### 5. The witness

- New: nothing of it runs in sluis. It cosigns the checkpoints of both log kinds through the `seal` block, which is built ([0009](decisions/0009-witnesses-and-verification.md)).

## Open

- **Open:** the names of the identifier attributes of a usage event ([0006](decisions/0006-the-usage-class.md), [0017](decisions/0017-the-organisation-of-a-usage-record-is-optional.md)). They are fixed in step 2.
