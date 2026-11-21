# 0002. The pieces, the transport that joins them, and who holds what

Status: accepted, 2026-11-21

## Context

Many parties emit: a product's own components, and the infrastructure it runs on. Many parties read: the team of an emitter, an auditor, a finance reader who needs usage and not security records, and each tenant for its own records. Who emits and who reads must be kept apart, without a separate audit for each pair.

The pieces that touch the log hold credentials, and a flaw in one must not become a flaw in another. A cluster has several nodes, and a log with two writers cannot keep one chain. People read the log through something that faces them, and the piece that holds the index credential must not parse internet input.

## Decision

### Five pieces

| Piece | Language | Holds | Never holds |
|---|---|---|---|
| **Emitter** | Go and Rust | Nothing. It is a library inside the emitting process, configured by that process. | A credential of audit |
| **Writer** | Go | The write credential of its project's bucket. It may Seal under `evidence` and ask for a signature by its log purpose. | The log key; Open under `evidence` |
| **Witness** | Go | Its cosignature key, through the `seal` block. | Anything the writer holds; a write right on the log |
| **Indexer** | Rust | The write credential of the search index. It may Open under `evidence`. | A write right on the log |
| **Reader** | Rust | A read-only credential on the index and on the log. It may Open under `evidence`, under the call's grant. | A write right on the index or the log |

- **The emitter** builds one message from one call: a security record and, for an action that uses something, usage events ([0004](0004-the-record-format.md), [0006](0006-the-usage-class.md)). It is not a process and not a credential holder.
- **The writer, the witness, the indexer and the reader are processes** with separate credentials. The indexer and the reader are separate so that a flaw in query parsing is not a flaw in a writer of the index.
- **There is no notary.** The writer asks the `seal` block to sign each checkpoint and the witness asks it to sign each cosignature. The block lets each use only its own purpose ([0011](0011-evidence-encryption-and-pseudonyms.md)).
- **The search index is derived data.** It is rebuilt from the log and is never evidence ([0012](0012-search-and-the-reader-api.md)).

### One audit per scope, a project per emitting unit

- **Each scope has one audit:** one set of pieces, one search index and one front serve every project that emits in the scope. No audit spans two scopes. A scope is the unit of keys and policy, so the evidence of one scope is never sealed, indexed or read with another's keys ([0003](0003-the-neutral-model.md)).
- **Each emitting unit is a project.** A project has its own bucket, its own writer, its own log keys, its own stream in the transport, and its own team and auditor bindings. A project's writer writes only its own bucket and signs only with its own log key, so a flaw in one project's writer cannot write or sign for another.
- **The scope's auditor reads every project of the scope, read-only.** No other binding reaches more than one project.

### The transport

The transport joins the emitter to the writer. Its port is atoom's [transport port](https://github.com/truvity/atoom/blob/master/transport/docs/transport-port.md). A message is a unique id, the record as opaque bytes and the usage events of the same call as opaque bytes.

- **Two modes.** In **direct** mode the emitter streams to the writer over a Unix socket on the same host or over mutual TLS. In **queue** mode the emitter puts the message into a queue and the writer receives it. Queue drivers are `spool` (built), `sqs` (planned) and `nats-jetstream` (planned).
- **Each project has its own queue or stream.** Only the project's emitters may publish to it and only the project's writer consumes it, so a product cannot write into another project's log.
- **The default follows the shape of the installation:** the spool on a host with a disk, SQS on Lambda, NATS JetStream on Kubernetes. Each stays possible in the other shapes where it fits. Direct has no default.
- **In direct mode the emitter falls back to a local spool** when the writer is down, so direct mode needs a disk and does not run on Lambda.
- **The transport authenticates every emitter by its platform identity:** the peer uid of a Unix socket, an IAM role, NATS credentials, or a service-account token. A product's emitter uses the product's own platform identity, so it needs no credential from an identity product.
- **A receive socket belongs to the writer alone.** The emit socket is open to the group of the emitters. Planned: separate groups for the two, so that the receive socket is closed to every emitter at the socket and not only by the guard.
- **A transport answers an emit only when the message is durable.** This is a required property of every transport. The [security class](0005-the-security-class.md) and the [usage class](0006-the-usage-class.md) rely on it.
- **Every blob request has a bounded duration and a bounded number of attempts,** so that the worst case of an operation is known. When the store refuses a request with 401 or 403, the driver reads its credential again without a cache and retries once. It decides that the credential changed by comparing the whole credential. Concurrent refusals share one read, and the log never holds the credential.
- **The writer logs each phase of its start-up with its duration,** so that a start-up on a platform with an initialisation limit completes or fails inside it.

### Built and planned

Built: the emitters in Go and Rust, the writer, the spool, and the `fs` blob drivers (in sluis and atoom). Planned: the witness, the `sqs` and `nats-jetstream` drivers, the separate receive group, and the move of the code to this repository.

## Consequences

- A project is a bucket, a writer, a log key per writer instance, a stream and two bindings. Adding a product to a scope costs that set and no new audit.
- A team or an auditor of one project cannot read another project, through the reader or through the bucket. The reader's grant filter, row-level security, the seal host's rules and the bucket's own access control each have to fail before that changes, and each needs tests with the others switched off.
- The emitter has no process to run, but the emitting process owns its transport configuration. The spool as the direct-mode fallback keeps messages when the writer is down, at the price of a disk on every emitting node and a spool to drain in order.
- Four transport drivers and three provisioning modes ([0012](0012-search-and-the-reader-api.md)) need tests and documents.

## Roads not taken

- **One audit per product, spanning the scopes the product runs in.** The evidence of several scopes would sit under one set of keys and one policy.
- **One audit for a whole installation.** The same, for every scope at once.
- **All projects in one bucket with one writer and one log key.** One credential and one key would write and sign for everyone.
- **A separate audit installation per project.** A front, an index and a reader for each project, and no place where the scope's auditor reads everything.
- **A separate notary actor.** A second holder of signing rights beside the `seal` block.
- **Fail the operation, or buffer in memory, when the writer is down in direct mode.** Failing stops every audited operation. Buffering in memory loses records on a restart, and an audit trail that drops records under failure is not one.
- **Cloudflare Queues as a transport driver.** Another driver to write and test that adds nothing over spool, SQS, NATS JetStream and direct.
- **The writer indexes as it writes, or one process for indexing and queries.** The writer would hold the index credential and stall when the index fails, and one process would parse callers' queries while holding the write credential.
