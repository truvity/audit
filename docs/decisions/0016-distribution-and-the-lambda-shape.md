# 0016. Distribution, and the Lambda shape: every piece behind the Web Adapter

Status: accepted, 2026-11-21. The SQS path is to be tested.

## Context

atoom ships each driver in every form it can run in ([atoom 0010](https://github.com/truvity/atoom/blob/master/docs/decisions/0010-distribution-per-driver-and-lambda.md)): a library, a tarball, a deb and an rpm package, an OCI image, and a Lambda extension zip. audit's pieces are products and not drivers, but they follow the same habits. The group runs every Lambda function behind the Lambda Web Adapter, vendored and pinned, so that one HTTP application runs the same on a host, in a container and on Lambda.

The audit writer on Lambda is triggered by SQS and is bound to one concurrent instance. That is the one function that the Web Adapter might not carry cleanly, so it must be tested.

## Decision

### Distribution

| Piece | Language | Forms |
|---|---|---|
| Emitter | Go, Rust | A Go module and a Rust crate. Libraries only. |
| Writer, witness | Go | Tarball, deb, rpm, OCI image, Lambda app zip |
| Indexer, reader | Rust | Tarball, deb, rpm, OCI image, Lambda app zip |
| Front | Open ([0014](0014-the-front-the-console-page-and-audit-verify.md)) | The same forms |
| `audit` command | Go | Tarball, deb, rpm, OCI image |

- **One binary is one piece.** Drivers are atoom's, and each is one driver of one port. A piece links the drivers it needs in process where the language matches, and calls the others as processes over their ports.
- **A form that a piece cannot run in is not built.** A piece that needs a lasting local disk (the spool, `blob/fs`, `index/sqlite`) has no Lambda form.
- **The emitter is the only part that every product links.** It exists in Go and in Rust, because every process needs it. Both write the same bytes, and a shared set of vectors, including the RFC 8785 corpus, keeps them in agreement.
- **The versions** of this repository follow atoom's habit of one version for the repository. The Go tags start at v0.17.0, above the tags of the archived module. **Open:** the rest of the release flow (the candidate, the evidence and the tag run) is adopted from atoom's [release notes](https://github.com/truvity/atoom/blob/master/docs/release.md) when code moves here.
- **There is no environment-variable configuration driver.** Configuration comes through the `source` port, and the environment carries only start-up settings.

### The Lambda shape

- **Every function runs behind the Lambda Web Adapter,** vendored and pinned. A function is an **app zip** plus a **config layer**, plus one **merged drivers layer** only where a driver's language differs from the function's. The layers merge the extensions the function needs, so a function stays within Lambda's limit of five layers.
- **No function has a native handler,** the writer included. This is the group's one rule for Lambda.
- **The functions:**

  | Function | Trigger | Notes |
  |---|---|---|
  | Writer | SQS | One per project, reserved concurrency 1, standard queue. The write-once fence is the correctness guarantee, and the concurrency limit is an optimisation. |
  | Witness | HTTP | `tlog-witness` is an HTTP protocol. |
  | Indexer | schedule or notification | One instance, no lease. |
  | Reader | HTTP, behind the front | Reaches the index directly with IAM authentication. |
  | Front | HTTP | The edge of the audit ([0014](0014-the-front-the-console-page-and-audit-verify.md)). |

- **Drivers on Lambda:** the blob driver for S3, the SQS transport driver, the `postgres` index driver (Aurora), the key driver for the cloud key service, and the `source` drivers for AppConfig and SSM. Planned in atoom. The spool, the local blob drivers and the SQLite index have no Lambda form.
- **SQS events reach the writer as `POST /events`.** The Web Adapter passes a non-HTTP event through to the application as an HTTP request to a configured path, by default `/events`, with the event as the body, and returns the application's response as the function's response. The writer serves `/events`, validates the batch, seals it, and answers with the batch item failures that SQS expects.

### What is to be tested

The pass-through is the one path that audit has not proved. The test must show, on real Lambda:

1. an SQS batch arrives as one `POST /events` with the whole batch in the body;
2. the writer's answer carries a partial batch response (`batchItemFailures`), and SQS redelivers only the failed messages;
3. with reserved concurrency 1 and a standard queue, two writers are never active on one log, and a second instance during a deploy creates a conflict that the fence turns into an error and not a fork;
4. a cold start, including the load of the catalogue and the state, completes inside the initialisation limit, and its phases are logged;
5. the function's timeout is longer than the longest bounded operation of the writer ([0002](0002-pieces-and-holders.md)), and a timeout leaves the state consistent;
6. an idle queue invokes nothing and signs nothing.

**Open: the fallback.** If a test fails in a way the adapter cannot fix, the fallback is a native handler for the writer alone. That would be an exception to the group's rule, and it needs its own record.

### Built and planned

Built: nothing of this record is built. The writer runs as a process on a host today (in sluis). Planned: every row above, in the order of atoom's plan, with the SQS pass-through test first.

## Consequences

- One rule, "every function behind the adapter", holds for the writer too. A function runs the same app on a host, in a container and on Lambda.
- The writer needs an HTTP surface for events. It is small, and it is the same code path in every shape.
- The pass-through needs the test above before the Lambda shape is trusted.
- Layers stay within Lambda's limit because the install tooling merges the extensions of one function into one layer.

## Roads not taken

- **A native handler for the writer.** Proven, but it is an exception to the group's Lambda rule and a second entry point to maintain.
- **One bundle binary for all pieces.** One binary is one piece, and an installation takes only what it runs.
- **A Lambda shape with a lasting disk (EFS) for the spool.** It adds a network file system to the fence's neighbourhood, for a transport that SQS already provides.
- **A DynamoDB index on Lambda.** See [0012](0012-search-and-the-reader-api.md).
