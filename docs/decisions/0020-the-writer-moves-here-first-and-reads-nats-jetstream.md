# 0020. The writer moves here first, as a Go service in its own image that reads NATS JetStream, before the usage step

Status: proposed, 2026-11-25 (owner decisions D90 A and D91 A). Partly supersedes [0015](0015-configuration-kinds.md) and [0011](0011-evidence-encryption-and-pseudonyms.md).

## Context

- **The owner's decision D90 A (2026-11-25):** the next step of audit is to move the writer out of sluis into this repository, so that it reads NATS JetStream, before the usage step. The [plan](../plan.md) had usage first and the writer second.
- **Why now.** session's development site sends its security records to a JetStream stream through atoom's in-process `transport/nats` ([session record 0032](https://github.com/truvity/session/blob/master/docs/decisions/0032-atoom-0.8.0-transports-and-the-appconfig-source.md), `--audit-nats`). Nothing consumes that stream yet. Until a writer reads it, the records are kept by the stream's limits and are evidence of nothing.
- **The owner's decision D91 A (2026-11-25):** mimic, sluis's test identity provider for development ([sluis record 0054](https://github.com/truvity/sluis/blob/main/docs/decisions/0054-mimic-is-a-test-identity-provider-of-fixed-identities-on-development-only.md)), gets its own action table in the catalogue: create, lease, use and revoke a test identity. The actor is the real sluis subject behind the identity, and the target is the test identity.

### The writer as it runs in sluis today

The writer is sluis's actor `actors/writer` on the `main` branch (sluis v2): about 8,500 lines of Go with its tests, in the package `writerd`. It is built as described in [0008](0008-the-log.md) and the [writer state](../reference/writer-state.md): the tiled log, the fence, sealing in batches of 256 or 5 seconds, the state with segments, quarantine and the clock rules. It writes the `audit` log only: a message with a usage part is refused with the reason `usage`, because no catalogue declares a meter.

It is a client of five sockets and serves none:

| It calls | For | Contract |
|---|---|---|
| sluis `store@` | the documents `AuditLog`, `AuditCatalogue` and sluis's `Boundary` (the boundary's name and its default tenant), at the start | sluis's store and config kinds |
| sluis `key@` | `PublicKey` and `Sign` of the purpose `audit-log` (ES256) for checkpoints; `Seal` under `evidence` for each record and each quarantined message | sluis's key service |
| sluis `policy` | a grant for `audit.seal` per tenant, presented to `key@` with each seal and kept for its life | sluis's policy service |
| the blob driver | `Create`, `Put`, `Get`, `List` in the project's bucket | atoom's blob port (the `fs` driver beside it) |
| the transport driver | `Receive` and `Ack` | atoom's transport port (the `spool` beside it) |

It also uses sluis's `kits/go/audit` (canonical JSON, the schema compiler and its policy, the release check), which moved to this repository as the emitter module, and the Merkle vectors of `kits/conformance/audit`. On a board it runs as a systemd unit per boundary with the spool and fs drivers as units beside it.

sluis's `master` branch (v1) carries the older audit module of the archived audit-v1 design (a notary, an observer, a query service and a Lambda writer). It is not the writer that moves, and this record does not touch it.

## Decision

### What moves

- **The code.** `writerd` and the writer's `main`, with every test, move into this repository as the Go module `github.com/truvity/audit/writer`, with the same bytes where nothing below changes them. The checks against the catalogue use the emitter module (`Canonical`, `ParseSchemas`, `CheckPolicy`, `ValidRelease`), not a second copy. The Merkle vectors move into `emitter/vectors/` beside the others, as [the catalogue page](../reference/action-table-and-catalogue.md#conformance-vectors) planned. The interoperability test with sluis's Rust indexer (the `go-writer` fixture) stays an interoperability test: the fixture is made here and copied to sluis by a test that fails when they differ.
- **What changes in it** is only the side facing sluis: the clients of `store@`, `key@` and `policy` are replaced (below). The log, the state, the segments, the quarantine and the clock rules do not change, so a log written by sluis's writer and one written by audit's are the same objects, and a verifier or an indexer cannot tell them apart.
- **The image.** The writer is the binary `audit-writer` (`serve`, `check`, `version`), released in the forms of [0016](0016-distribution-and-the-lambda-shape.md) for the writer except the Lambda app zip, which needs `blob/s3`, `transport/sqs` and the pass-through test of 0016. The OCI image is distroless, runs as a user that is not root, and is built for `linux/amd64` and `linux/arm64`. The release flow ([releasing](../release.md)) gains the packages and images of atoom's flow.

### How it runs

```text
emitters ──publish──> JetStream stream of the project ──durable consumer──> audit-writer ──blob port──> blob-fs sidecar ──> the project's volume
                                                                                │
                                                                                └──seal port──> audit-seal sidecar ──key port──> key driver
                                                                                                                  └──store port──> the wraps
```

- **A Go service, one per project.** One writer instance per project and environment, as [0002](0002-pieces-and-holders.md) has it: its own stream, its own blob root or bucket, its own log key. One image serves every project; the project is a start-up flag and a document.
- **The transport is a switch, exactly one.** `--transport-nats URL` links atoom's Go `transport/nats` (0.8.0) in the process: the host's connection, the stream and the durable consumer the deployment made (`--nats-stream`, `--nats-consumer`, `Create` off), the credentials as a credentials file or a token file read at every connect, and a CA file for TLS. `--transport-socket PATH` keeps the receive socket of a driver beside the process, the spool on a board. Neither or both are refused at the start. The JetStream consumer's ack wait must be longer than the writer's longest bounded batch (0002); the writer refuses to start when the consumer's ack wait is shorter than that bound.
- **The blob is a socket.** The `fs` driver is Rust, so it runs beside the writer (`blob-fs serve --root DIR`) on a volume with an atomic create-if-absent ([0008](0008-the-log.md#the-write-once-fence)). `blob/s3` (atoom's B9, not built) is linked in the process when it is released, and the writer then takes the switch `--blob-s3`. A log moves from a volume to a bucket by copying its objects, because every object but the two pointers is written once and the pointers are copied last.
- **Keys go through atoom's `seal` block, behind audit's own host.** See [below](#the-seal-host).
- **The start-up settings are flags; the documents come through the source port** ([0016](0016-distribution-and-the-lambda-shape.md)). Flags: the project, the instance, the scope, the transport switch, the sockets, the source switch, and the file of publisher pins. No environment variable carries configuration.
- **On Kubernetes** it runs in a shared cluster as one StatefulSet per project with one replica: the instance name is the pod's ordinal and stays the same across restarts, and the volume follows it. More than one replica per project would need a blob root per instance, because the writer's state is one object per bucket ([log layout](../reference/log-layout.md)); one replica is the shape until a project needs more.

### The configuration before B7

- **The writer may start before atoom's `state` block (B7).** It reads its documents through atoom's `source` port: `--config-dir DIR` with the Go `source/file` (built since atoom 0.3.0), or `--source-appconfig APP/ENV` with the Go `source/appconfig` (0.8.0), exactly one.
- **The documents are signed exactly as 0015 asks.** Each is the signed config format that sluis's pipeline signs and atoom's policy loader verifies ([atoom's policy loader](https://github.com/truvity/atoom/blob/master/policy/docs/policy-loader.md)): the writer calls atoom's `policy.VerifyDocument` with the kind and the publisher pins of the installation, refuses a document that does not verify or whose sequence went back, and keeps the last good set when a reload fails. A writer starts only with a valid set.
- **The writer reads two kinds in this step:** `AuditLog` (`audit-log.jws`) and `AuditCatalogue` (`audit-catalogue.jws`). `AuditWitnesses` comes with the witness (plan step 5) and `Meter` with usage (step 2).
- **audit owns the schemas of its kinds now.** The four kinds are defined in this repository as `audit.config.v1alpha1` (protobuf, with the JSON form the signed payload carries), from the fields of [config kinds](../reference/config-kinds.md). `AuditLog` gains what sluis's `Boundary` gave the writer: the scope's name, which the writer checks against its flag, and nothing for the default tenant, because a record that names no tenant is sealed under the scope's own evidence key (the seal scope that is empty, [0011](0011-evidence-encryption-and-pseudonyms.md)). The same schemas are what audit declares to the registry when B7 is released.
- **When B7 is released,** the writer gains a third source switch that reads the same documents through the `state` block, and the file and AppConfig paths stay for hosts that run no `state`. The documents do not change.
- **What this record overtakes in 0015:** "The audit pieces read their documents through the block" now reads "through the block where a host runs one, and otherwise through the source port with the same signature checks". Everything else in 0015 stands: the four kinds, signed and reviewed, written by none of the pieces.

### The seal host

- **audit ships its own host of the `seal` block,** the binary `audit-seal`, a small Go process that links atoom's `blocks/seal` and its key and store drivers in process, and serves the seal port on a socket. It closes the open point of [0011](0011-evidence-encryption-and-pseudonyms.md) on "the host of `seal` for audit alone". It is a host of an atoom block, not a sixth piece: it holds keys and writes no log.
- **Its rules are audit's,** by the caller's uid on the socket: the writer may `Sign` and `PublicKey` under `log`, and `Seal` under `evidence` for any scope, making a missing key (`MayCreate`); it may never `Open`. The indexer and the reader, when they move, are admitted to `Open` under `evidence` by the grant of [0013](0013-access.md); emitters may `Mac` under `pseudonym`. Every other call is refused.
- **It runs beside each writer instance,** in the same pod as a separate container with its own uid, so the writer's process never holds a key that opens what it wrote (0002, 0011). The log key is the instance's: one key under `log` per seal host and instance.
- **It needs a key driver and a strict store for the wraps.** The data keys are kept only wrapped, in the store port (atoom's `seal` block). A lossy store (`store/memory`) is refused at start, because a lost wrap is a record that never opens again. Which drivers is the installation's choice ([C5](#c5-keys-and-wraps-of-the-first-deployment)).
- **The grants of sluis's policy go.** Outside sluis no policy service gives the writer a grant per tenant: the seal host's rules admit the writer by its caller identity. Where sluis hosts the block, sluis's rules may still want its grant: that is sluis's change.
- **What this record overtakes in 0011:** the line "Open: the host of `seal` for audit alone" is decided as above. The purposes of 0011 (`log`, `witness`, `evidence`, `pseudonym`) and its rules stand. sluis's writer signs under the purpose `audit-log`; audit's writer signs under `log`, and the switch of [sluis](#sluiss-switch-and-the-dual-period) maps one to the other.

### What atoom must give, and what it need not

| Need | atoom today | For this step |
|---|---|---|
| `transport/nats` in Go | Released in 0.8.0 | Linked by the writer |
| `source/file`, `source/appconfig` in Go | Released (0.3.0, 0.8.0) | Linked by the writer |
| The signed document verifier | Released in `policy` (0.5.0) | Used by the writer for its kinds |
| The `seal` block in Go | Released in 0.4.0 | Linked by `audit-seal` |
| Key and store drivers | `key/awskms`, `key/software` and `store/dynamodb` released in Go; `store/rqlite` released in Rust | Linked by `audit-seal`; `store/rqlite` as a socket beside it |
| `blob/fs` | Released, Rust | A sidecar |
| `blob/s3` (B9) | Not built | Not needed to start; needed for a bucket and for the Lambda shape |
| The `state` block (B7) | Not built | Not needed to start (above) |
| The generic transport message (B12) | Not built | Not needed: the writer reads today's message (`uid`, `record`, `usage`) and adopts B12 in the change that follows its release, as the emitter does |

Nothing new is asked of atoom for this step.

### sluis's switch and the dual period

- **audit's writer runs first where nothing runs:** the JetStream stream of session's development site, and mimic's (below). sluis's boards keep sluis's writer.
- **During the dual period sluis's writer is frozen.** A change to the writer is made here. A security fix is made here and carried to sluis's copy in the same week, until sluis has switched. The people who maintain sluis v2 have accepted the freeze (2026-11-25).
- **sluis switches when three things hold:** sluis `key@` hosts atoom's `seal` block and serves the seal port (sluis's own step); sluis's signed `AuditLog` and `AuditCatalogue` reach the writer through the source port or B7; and audit's writer, run beside sluis's on a board against a copy of a real log, continues it and passes sluis's tests. Then sluis's units run `audit-writer --transport-socket` with the spool, and `actors/writer` is deleted with a "moved to" note.
- **sluis continues its logs; it does not start new ones** ([C7](#c7-sluiss-logs-at-the-switch)). The log format, the state format and the seal addresses are the same, so audit's writer opens sluis's state and goes on. The log key stays the same key: sluis's host maps the purpose `log` to the key it holds as `audit-log`.
- **The usage path is built in audit's writer** (plan step 2), not in sluis's. sluis emits no usage until it has switched.

### The first deployment

On a shared Kubernetes cluster of a development environment, for session's development site and for mimic. The shape is written in the deployment's own repository, in a later pull request of its own; this record names only what the writer needs from it:

- for each project: the JetStream stream (storage, replicas, duplicate window of at least two minutes, a retention longer than the writer's longest outage), the writer's durable consumer with an explicit ack and an ack wait above the writer's bound, and NATS credentials that may consume that consumer and nothing else; the emitters' credentials may publish to the subject and nothing else;
- a StatefulSet of one replica with three containers (`audit-writer`, `audit-seal`, `blob-fs`), each its own uid, sharing a socket directory, the volume mounted by `blob-fs` alone;
- a volume with an atomic create-if-absent (a block device with a local file system; not a network file system whose `link()` is not atomic);
- the signed documents of the project, mounted read-only for `--config-dir`, and the publisher pins;
- the key driver's and the store's credentials for `audit-seal` alone, through the cluster's workload identity;
- the floors of the project's logs declared `report` or `enforce` in its `AuditLog` ([0019](0019-floors-are-a-declared-switch.md)): the writer reads the field and the deployment chooses it.

The project's catalogue must exist before the writer can take a record: a record whose release has no catalogue is quarantined ([0008](0008-the-log.md)). session's table and schemas are code in session; session publishes them as its signed `AuditCatalogue` for its project. That is session's follow-up ([session record 0023](https://github.com/truvity/session/blob/master/docs/decisions/0023-audit-emitter.md)), not audit's, and it must be done before the first records are written rather than quarantined.

### mimic's action table

mimic is an emitting unit of its own, so it is a project of its own on the development writer, with its own stream and catalogue (0002). Its table follows the [table format](../reference/action-table-and-catalogue.md#the-table) with the product `mimic`:

| Action | When | OCSF class and activity | Outcomes | `emit` |
|---|---|---|---|---|
| `mimic.identity.create` | A person or a job takes a test identity for the first time: mimic derives the identity's subject for that pair | 3004 Entity Management, 1 Create; `entity` is the test identity | `success` | required |
| `mimic.identity.lease` | A person or a job signs in at mimic and is bound to a test identity (by `login_hint` or the chooser), after the act-as group is checked | 3002 Authentication, 7 Account Switch | `success`, `failure` | required |
| `mimic.identity.use` | mimic mints an ID token or an access token under a lease for a relying party | 3002 Authentication, 99 "Token Grant" | `success`, `failure` | required |
| `mimic.identity.revoke` | A lease ends: sign-out, the cookie's expiry seen, the act-as group no longer held, or an operator | 3002 Authentication, 2 Logoff | `success` | best effort |

- **The actor is the real subject.** `actor.user` is the person or the job behind the identity: the stable sluis's subject that mimic puts in `act.sub`, recorded as a pseudonym ([0011](0011-evidence-encryption-and-pseudonyms.md)), with `type_id` saying a person or a workload.
- **The target is the test identity.** `user` is the test identity: `uid` is mimic's keyed subject, `name` the identity's username. The e-mail address of a test identity is never recorded (the emitter refuses an e-mail shape in any string), and its groups are not either.
- **The tenant is mimic's tenant** (`metadata.tenant_uid`, such as `example-a`), so each test tenant's records are sealed under its own evidence key.
- **The product object holds** `tenant`, `profile` (`entra` or `google`), `identity` (the username), `client` (the relying party's client id, for `use`), `lease` (a digest of the lease, so that `lease`, `use` and `revoke` of one lease join), and `reason` (a closed word: for a `failure`, `act_as`, `identity`, `client`; for `revoke`, `logout`, `expiry`, `act_as`, `operator`).
- **No meters.** mimic is not billed.
- **A refused sign-in is a `lease` with `failure`,** so a person who tried an identity without the act-as group is on the record.
- **The pseudonym label is mimic's,** `mimic-pseudonym-v1` ([0018](0018-the-pseudonym-domain-label-is-the-products.md)). **Open:** where mimic's `Mac` comes from: the `audit-seal` host of mimic's project, or a seal block mimic hosts in its own process. It is decided in mimic's emitter change.
- **The classes are checked against OCSF 1.9.0** when the rows are written, as for every table ([0004](0004-the-record-format.md)); a class that the check refuses changes in that pull request and not here.
- **The table file** lives with mimic, in sluis's `tools/mimic` ([C8](#c8-where-mimics-table-lives), decided), and its catalogue is published as mimic's signed `AuditCatalogue` for its project.

## The owner's choices

Each has options, their pros and cons, and a recommendation. On 2026-11-25 the owner took the recommendation of C1 to C5, C7 and C8 (D107 to D111, D113 and D114). C6 is still being discussed, and the record stays proposed until it is settled.

### C1. Log storage of the first deployment

| Option | Pros | Cons |
|---|---|---|
| **A. `blob/fs` on a volume now; copy to a bucket when `blob/s3` is released** | Starts with what is released; the fence is proven on `fs`; nothing waits for atoom | A sidecar; a volume tied to one zone; a later copy and a switch of the writer's blob flag |
| B. Wait for `blob/s3` (B9) | The bucket from the first record; object lock and lifecycle at once | session's records stay unread until atoom builds and releases `blob/s3` |

Decided: **A** (the owner's D107 A, 2026-11-25). The copy is safe because every object but the pointers is written once.

### C2. Whether the writer starts before B7

| Option | Pros | Cons |
|---|---|---|
| **A. Start before B7, documents through the source port, verified as signed config** | No wait; the same signatures and review as 0015 asks; the documents carry over to B7 unchanged | A second read path until B7; partly supersedes 0015 |
| B. Wait for B7 | One read path, as 0015 says | B7 comes after B12 in atoom's order; the stream stays unread for that long |
| C. An unsigned file or environment variables until B7 | Least code | 0015 refused it: no signature, no review |

Decided: **A** (the owner's D108 A, 2026-11-25).

### C3. The source of the documents in the first deployment

| Option | Pros | Cons |
|---|---|---|
| **A. `source/file` over a read-only mount** | The cluster's usual way; nothing outside the cluster | A reload needs the mount updated |
| B. `source/appconfig` | The same path as a Lambda function; polling built in | A cloud service for a cluster workload |

Decided: **A** (the owner's D109 A, 2026-11-25). B stays a switch for the Lambda shape.

### C4. One writer per project, per environment, or per vendor

| Option | Pros | Cons |
|---|---|---|
| **A. One per project (0002)** | One credential and one key write and sign for one emitting unit; a flaw stays in it | One StatefulSet per project |
| B. One per environment for every project | One workload | One credential and one key for every project: refused by 0002 |
| C. One per vendor | Fewer workloads than A | Still several projects under one key; a vendor is not a unit of audit's model |

Decided: **A** (the owner's D110 A, 2026-11-25). On the first deployment that is two writers, session's and mimic's.

### C5. Keys and wraps of the first deployment

| Option | Pros | Cons |
|---|---|---|
| **A. `key/awskms` and `store/dynamodb` in `audit-seal`** | No key material in the cluster; strict wraps; both drivers are Go and released | A cloud key service and a table for each project's seal host |
| B. `key/software` from a secret, and `store/rqlite` | All inside the cluster | A root key in a secret; an rqlite cluster to run and back up, and its Rust driver as one more sidecar |
| C. The block in the writer's own process | One container fewer | The writer's process holds the evidence data keys and could open what it wrote: against 0002 and 0011 |

Decided: **A** (the owner's D111 A, 2026-11-25). In the separate `audit-seal` container.

### C6. The workload shape

| Option | Pros | Cons |
|---|---|---|
| **A. A StatefulSet of one replica** | A stable instance name and log; the volume follows the pod; never two pods of one ordinal | A stuck node holds the pod until it is evicted |
| B. A Deployment with the Recreate strategy | Simpler | A new pod name per rollout, so a new log id unless the name is pinned by a flag |

Recommendation: **A.** The fence holds in either.

### C7. sluis's logs at the switch

| Option | Pros | Cons |
|---|---|---|
| **A. Continue each node's log** | One chain per node for a verifier; no closed logs to keep track of | The switch must prove it opens sluis's state (a test on a copy) |
| B. Close sluis's logs and start new ones | No dependence on the old state | Twice the logs for every node, and a final checkpoint to sign at the switch |

Decided: **A** (the owner's D113 A, 2026-11-25).

### C8. Where mimic's table lives

| Option | Pros | Cons |
|---|---|---|
| **A. With mimic, in sluis (`tools/mimic`), product `mimic`** | The rule of the catalogue page: a product's table stays with the product, with its emitter's code | Its catalogue is published from sluis's pipeline |
| B. In this repository | One place for the tables of the group's test tools | audit would hold a product's table and follow its changes |
| C. Rows in sluis's own table, product `sluis` | No new product | mimic is not part of sluis's product (0054); its records would mix with sluis's |

Decided: **A** (the owner's D114 A, 2026-11-25).

## Built and planned

Nothing of this record is built. Planned, in order: the writer and its tests here with the replaced clients, the kinds' schemas, `audit-seal`, the image and packages in the release flow, the first deployment, session's catalogue, mimic's emitter and table, and sluis's switch. The requirement rows of the move are written when the record is accepted.

## Consequences

- session's records on JetStream are read, sealed and kept as soon as the writer and session's catalogue are deployed.
- audit runs without sluis for the first time: a product's records need no sluis store, key service or policy.
- The usage path is built once, in audit's writer.
- Two read paths for the documents exist until B7; both verify the same signatures.
- audit ships two binaries more (`audit-writer`, `audit-seal`) and their images, so the release flow grows packages and images.
- A writer pod is three containers; each holds only its own credential.
- sluis's writer is frozen and carried for security fixes only until sluis switches.

## Roads not taken

- **Usage first, as the plan said.** It would land the usage path in sluis's writer and move it again, while session's stream stays unread.
- **A NATS transport driver beside the writer as a process.** atoom's Go driver links in the process, as session's does, and one sidecar fewer.
- **Run sluis's writer on the cluster for session.** It needs sluis's store, key service and policy, which the cluster does not run.
- **Keep the policy grant outside sluis.** No policy service gives it there; the seal host's rules admit the writer by its identity.
- **Move the writer, the indexer and the reader together.** The reader and the indexer need B12's index contract; the writer needs nothing new.
