# Capabilities

What each platform can do today and what is designed. The
[architecture](architecture.md) says how the parts fit; the decisions
[0016](decisions/0016-three-parts-installed-independently.md) to
[0023](decisions/0023-archive-retention-and-lifecycle.md) say where it is
going.

| mark | means |
|---|---|
| 📄 designed | described in a decision or reference page; no code |
| 🧪 built | the code exists and is tested; not yet run live |
| ✅ supported | built, tested in CI and run in an installation |
| — | does not apply to that platform |

Today's state is that the ingest path writes the v1 layout (🧪), the
readers around it follow it, and the rest of the target architecture is 📄. The
v0 archive is not read or written by anything in this release: it is readable
only with the previous release's CLI (v0.6.x).

## Ingest

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| `http` sink (Connect to the receiver) | ✅ | 📄 | 📄 |
| `nats` sink and consumer (JetStream) | ✅ | — | 📄 |
| `sqs` sink and consumer, selectable as `forward.sqs` and `consume.sqs` (untested live) | — | 🧪 | — |
| `lambda` sink (direct invocation of the writer) | — | 📄 | — |
| `s3` sink (in process: the writer puts the object) | ✅ | ✅ | ✅ |
| `log` sink | 🧪 | 🧪 | 🧪 |
| acknowledgement carries Archived, Queued or Logged | 🧪 | 🧪 | 🧪 |
| `require:` start-up guard, read from configuration (`require`, `forward`, `consume`, `sink.expect`) | ✅ | 🧪 | 🧪 |
| sink conformance suite (`sdk/sink/sinktest`) | 🧪 | 🧪 | 🧪 |
| a full spool fails the write | 📄 | 📄 | 📄 |
| deduplication on Postgres or in memory | ✅ | 📄 | 📄 |
| deduplication on JetStream (duplicate window, KV with TTL) | 📄 | — | — |
| deduplication on DynamoDB | — | 📄 | — |
| one configuration file against a schema | ✅ | 📄 | 📄 |
| metrics over OTLP: acknowledgements by durability, write latency per transport, index lag, consumer failures | 🧪 | 🧪 | 🧪 |
| traces over OTLP: server and client spans, `traceparent` across NATS headers and SQS attributes, no personal data on a span | 🧪 | 🧪 | 🧪 |
| chart `renders: alerts`: six alert rules as a `VMRule` or `PrometheusRule`, unit-tested on vmalert-tool | 🧪 | 🧪 | 🧪 |
| chart `renders: dashboards`: the audit overview for Grafana's sidecar, held to the observability dashboard lint | 🧪 | 🧪 | 🧪 |

The `s3` sink works against any store that speaks the S3 API; on a store
without Object Lock it is the `attested` tier of
[0014](decisions/0014-lock-modes-and-store-tiers.md).

## The archive

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| v1 layout, written and read ([bucket contract](reference/bucket-contract.md)): one object per ingest batch, keyed by ingest time | 🧪 | 🧪 | 🧪 |
| `catalogue/<app>/<version>`, written once, compared when present | 🧪 | 🧪 | 🧪 |
| `audit verify`: key, metadata, sha256 and per-record hashes of every object | 🧪 | 🧪 | 🧪 |
| Object Lock, compliance mode | ✅ | ✅ | — |
| governance trial, then compliance ([0023](decisions/0023-archive-retention-and-lifecycle.md)) | 📄 | 📄 | — |
| lifecycle to Glacier Instant Retrieval and Deep Archive | — | 📄 | — |
| bucket-contract conformance suite (records, catalogue and ordering, against the memory store and S3) | 🧪 | 🧪 | 🧪 |
| conformance of seals, delegation and revocation | 📄 | 📄 | 📄 |

## Notary

The v0 digest job was removed with the v0 layout; seals replace it and are not
built yet. The signers stay for them.

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| seals, chained through `prev` ([0019](decisions/0019-seals.md)) | 📄 | 📄 | 📄 |
| delegation and revocation | 📄 | 📄 | 📄 |
| signer: AWS KMS | ✅ | ✅ | — |
| signer: OpenBAO transit | ✅ | — | ✅ |
| signer: key file | ✅ | ✅ | ✅ |
| signer: PKCS#11 | — | — | 📄 |
| signer: TPM | — | — | 📄 |
| notary as a function on a schedule | — | 📄 | — |
| `audit verify` of seals | 📄 | 📄 | 📄 |

## Observe

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| index and search on Postgres, fed by the writer | ✅ | 📄 | 📄 |
| cursor listing over the bucket, settle window | 📄 | 📄 | 📄 |
| notifications as a wake-up | 📄 | 📄 | 📄 |
| reindex from the archive (v1) | 🧪 | 🧪 | 🧪 |
| usage quotas | 📄 | 📄 | 📄 |
| billing statements | 📄 | 📄 | 📄 |

## Packaging

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| `audit` toolchain archives and container images | ✅ | ✅ | ✅ |
| Helm chart | ✅ | — | — |
| Helm chart modes per part | 📄 | — | — |
| Pulumi library (bucket, queue, functions, keys, a cross-account read role) | — | 📄 | — |
| rpm and deb packages | — | — | 📄 |
