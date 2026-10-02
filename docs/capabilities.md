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

Today's state is that the v0 archive and everything around it is ✅, and the
target architecture is 📄.

## Ingest

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| `http` sink (Connect to the receiver) | ✅ | 📄 | 📄 |
| `nats` sink and consumer (JetStream) | ✅ | — | 📄 |
| `sqs` sink and consumer | — | 🧪 | — |
| `lambda` sink (direct invocation of the writer) | — | 📄 | — |
| `s3` sink (in process: the writer puts the object) | ✅ | ✅ | ✅ |
| `log` sink | 🧪 | 🧪 | 🧪 |
| acknowledgement carries Archived, Queued or Logged | 🧪 | 🧪 | 🧪 |
| `require:` start-up guard (`sink.Require`, `sink.Guard`; not yet read from configuration) | 🧪 | 🧪 | 🧪 |
| sink conformance suite (`sink/sinktest`) | 🧪 | 🧪 | 🧪 |
| a full spool fails the write | 📄 | 📄 | 📄 |
| deduplication on Postgres or in memory | ✅ | 📄 | 📄 |
| deduplication on JetStream (duplicate window, KV with TTL) | 📄 | — | — |
| deduplication on DynamoDB | — | 📄 | — |
| one configuration file against a schema | ✅ | 📄 | 📄 |

The `s3` sink works against any store that speaks the S3 API; on a store
without Object Lock it is the `attested` tier of
[0014](decisions/0014-lock-modes-and-store-tiers.md).

## The archive

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| v0 layout, one object per profile, tenant and day | ✅ | ✅ | ✅ |
| v1 layout ([bucket contract](reference/bucket-contract.md)) | 📄 | 📄 | 📄 |
| Object Lock, compliance mode | ✅ | ✅ | — |
| governance trial, then compliance ([0023](decisions/0023-archive-retention-and-lifecycle.md)) | 📄 | 📄 | — |
| lifecycle to Glacier Instant Retrieval and Deep Archive | — | 📄 | — |
| bucket-contract conformance suite | 📄 | 📄 | 📄 |

## Notary

The signers exist today for the v0 digest; using them for ES384 seals is
part of the v1 work.

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| hourly digest chain (v0) | ✅ | 📄 | 📄 |
| seals, chained through `prev` ([0019](decisions/0019-seals.md)) | 📄 | 📄 | 📄 |
| delegation and revocation | 📄 | 📄 | 📄 |
| signer: AWS KMS | ✅ | ✅ | — |
| signer: OpenBAO transit | ✅ | — | ✅ |
| signer: key file | ✅ | ✅ | ✅ |
| signer: PKCS#11 | — | — | 📄 |
| signer: TPM | — | — | 📄 |
| notary as a function on a schedule | — | 📄 | — |
| `audit verify` | ✅ | ✅ | ✅ |

## Observe

| feature | Kubernetes | AWS | self-hosted |
|---|---|---|---|
| index and search on Postgres, fed by the writer | ✅ | 📄 | 📄 |
| cursor listing over the bucket, settle window | 📄 | 📄 | 📄 |
| notifications as a wake-up | 📄 | 📄 | 📄 |
| reindex from the archive | ✅ | ✅ | ✅ |
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
