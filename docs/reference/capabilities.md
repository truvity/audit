# Capabilities

What each platform can do today and what is designed. The
[architecture](../explanation/architecture.md) says how the parts fit; the decisions
[0016](../decisions/0016-three-parts-installed-independently.md) to
[0024](../decisions/0024-indexer-and-query-are-separate-processes.md) say where it is
going.

| mark | means |
|---|---|
| 📄 designed | described in a decision or reference page; no code |
| 🧪 built | the code exists and is tested; not yet run live |
| ✅ supported | built, tested in CI and run in an installation |
| — | does not apply to that platform |

Today's state is that the ingest path writes the v1 layout (🧪), the notary seals
it (🧪), the readers around it follow it, observe follows the bucket by cursor (🧪),
and the rest of the target architecture is 📄. The
v0 archive is not read or written by anything in this release: it is readable
only with the previous release's CLI (v0.6.x).

## Ingest

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| `http` sink (Connect to the receiver) | ✅ | 📄 | 📄 |
| `nats` sink and consumer (JetStream) | ✅ | — | 📄 |
| `sqs` sink and consumer, selectable as `forward.sqs` and `consume.sqs` (untested live) | — | 🧪 | — |
| `lambda` sink (direct invocation of the writer) | — | 📄 | — |
| the writer as an AWS Lambda behind an SQS event source mapping, with partial batch responses (`audit-writer-lambda`, [AWS](aws-pulumi-library.md)) | — | 🧪 | — |
| `s3` sink (in process: the writer puts the object) | ✅ | ✅ | ✅ |
| `log` sink | 🧪 | 🧪 | 🧪 |
| acknowledgement carries Archived, Queued or Logged | 🧪 | 🧪 | 🧪 |
| `require:` start-up guard, read from configuration (`require`, `forward`, `consume`, `sink.expect`) | ✅ | 🧪 | 🧪 |
| sink conformance suite (`sdk/sink/sinktest`) | 🧪 | 🧪 | 🧪 |
| a full spool fails the write | 📄 | 📄 | 📄 |
| deduplication on Postgres or in memory | ✅ | 📄 | 📄 |
| deduplication on JetStream (duplicate window, KV with TTL) | 📄 | — | — |
| deduplication on DynamoDB (conditional put with TTL, `dedupe/dynamodbdedupe`; LocalStack in CI) | — | 🧪 | — |
| one configuration file against a schema | ✅ | 📄 | 📄 |
| metrics over OTLP: acknowledgements by durability, write latency per transport, index lag, consumer failures | 🧪 | 🧪 | 🧪 |
| queue metrics: the age of each message at receive (`audit.queue.message.age`, from `SentTimestamp`), and CloudWatch alarms on the DLQ and the oldest message | — | 🧪 | — |
| chart value `telemetry.otlp` (`endpoint`, `protocol`, `extraEnv`): the OpenTelemetry environment on every pod | 🧪 | — | — |
| telemetry from a Lambda with the function role's identity and no secret (the OTLP extension layer `audit-otlp`, which sluis publishes, [AWS](../how-to/aws-send-lambda-telemetry.md)) | — | 🧪 | — |
| traces over OTLP: server and client spans, `traceparent` across NATS headers and SQS attributes, no personal data on a span | 🧪 | 🧪 | 🧪 |
| chart `renders: alerts`: seven alert rules as a `VMRule` or `PrometheusRule`, unit-tested on vmalert-tool | 🧪 | 🧪 | 🧪 |
| chart `renders: dashboards`: the audit overview for Grafana's sidecar, held to the observability dashboard lint | 🧪 | 🧪 | 🧪 |

The `s3` sink works against any store that speaks the S3 API; on a store
without Object Lock it is the `attested` tier of
[0014](../decisions/0014-lock-modes-and-store-tiers.md).

## The archive

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| v1 layout, written and read ([bucket contract](bucket-contract.md)): one object per ingest batch, keyed by ingest time | 🧪 | 🧪 | 🧪 |
| `catalogue/<app>/<version>`, written once, compared when present | 🧪 | 🧪 | 🧪 |
| `audit verify`: key, metadata, sha256 and per-record hashes of every object | 🧪 | 🧪 | 🧪 |
| `audit verify --root`: the seals of a range, against pinned roots (below) | 🧪 | 🧪 | 🧪 |
| Object Lock, compliance mode | ✅ | ✅ | — |
| governance trial, then compliance ([0023](../decisions/0023-archive-retention-and-lifecycle.md)) | 📄 | 📄 | — |
| lifecycle to Glacier Instant Retrieval and Deep Archive | — | 📄 | — |
| bucket-contract conformance suite (records, catalogue and ordering, against the memory store and S3) | 🧪 | 🧪 | 🧪 |
| conformance of seals, delegation and revocation: against the memory store and S3, with a key file and a KMS P-384 key (LocalStack) | 🧪 | 🧪 | 🧪 |

## Notary

The v0 digest job was removed with the v0 layout; seals replace it
([0019](../decisions/0019-seals.md)). The notary is `audit-notary`, a binary and an
image of its own.

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| seals, chained through `prev`, one per profile, tenant and hour, empty hours too; settle window; idempotent; refuses to seal what does not match its metadata | 🧪 | 🧪 | 🧪 |
| Merkle root over per-record hashes (RFC 6962), inclusion proofs and the vectors for n = 0, 1, 2, 3 and 5 (`internal/merkle`) | 🧪 | 🧪 | 🧪 |
| `keys/roots.jwks`, written once; thumbprints pinned by the verifier | 🧪 | 🧪 | 🧪 |
| chart: `jobs.notary`, an hourly CronJob under an identity of its own, refused if it is the writer's | 🧪 | — | — |
| seal age: `audit.seal.age`, the `AuditSealStale` alert and a dashboard panel | 🧪 | 🧪 | 🧪 |
| verifying a delegation (window, scope, 25 hours) and a revocation | 🧪 | 🧪 | 🧪 |
| signing a delegation: a notary on a short-lived delegated key (decision L3: daily) | 📄 | 📄 | 📄 |
| signer: AWS KMS `ECC_NIST_P384` | 🧪 | 🧪 | — |
| signer: OpenBAO transit `ecdsa-p384` | 🧪 | — | 🧪 |
| signer: P-384 key file | 🧪 | 🧪 | 🧪 |
| signer: PKCS#11 | — | — | 📄 |
| signer: TPM | — | — | 📄 |
| notary as a function on an EventBridge Scheduler schedule (`audit-notary-lambda`) | — | 🧪 | — |
| `audit verify` of seals: signature, chain, count and root against the objects, missing seals | 🧪 | 🧪 | 🧪 |

The seals are tested against the in-memory store and against LocalStack S3, with
the notary signing with a key file and with a KMS key; they have not run in an
installation, which is what ✅ asks for. The signers that predate seals (an
ed25519 or P-256 key) still sign and verify in `keys`, and sign no seal.

## Observe

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| index and search on Postgres | ✅ | 📄 | 📄 |
| cursor observe: `audit-observe` lists the bucket from a durable cursor per profile and tenant, behind a settle window ([0020](../decisions/0020-observe-follows-the-bucket.md)) | 🧪 | 🧪 | 🧪 |
| notifications as a wake-up (a NATS subject or an SQS queue that only shortens the poll) | 🧪 | 🧪 | 🧪 |
| one database role per part: the writer's has no index, observe's writes it, the query service's reads it ([0024](../decisions/0024-indexer-and-query-are-separate-processes.md)) | 🧪 | 🧪 | 🧪 |
| reindex from the archive (v1), and a cursor reset | 🧪 | 🧪 | 🧪 |
| usage quotas | 📄 | 📄 | 📄 |
| billing statements | 📄 | 📄 | 📄 |

## Packaging

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| `audit` toolchain archives and container images | ✅ | ✅ | ✅ |
| Helm chart | ✅ | — | — |
| Helm chart modes per part | 📄 | — | — |
| Pulumi library (bucket with Object Lock and lifecycle, keys, queue and DLQ, functions, roles, schedule, alarms, a cross-account read role; `deploy/pulumi`, mocks only) | — | 🧪 | — |
| CloudWatch alarm set: throttles, DLQ not empty, oldest message age, errors, notary silence, to SNS and alert-ingress | — | 🧪 | — |
| rpm and deb packages | — | — | 📄 |
