# Levels

How much of audit an installation runs. The [shapes](README.md) say how a record
reaches the bucket; the level says what protects it once there.

| level | the archive | seals | the index | for |
|---|---|---|---|---|
| `full` | S3 with Object Lock | signed by KMS or OpenBao Transit | Postgres (observe, query) | a record that must stand as evidence |
| `lite` | S3 or S3-compatible, no lock | a local key, or none | Postgres | an internal service, a homelab, a trial |
| `log` | none: no installation | none | none | the emitter's `logsink` writes each record as a structured log line |

`log` is the entry point: an application emits with the SDK and its log pipeline
keeps the lines. Moving up changes configuration, not records.

## Presets

The decision-tree presets, and the level each normally gives. The names are
vocabulary for choosing a deployment, not chart or library options.

| preset | what it is | level | built from |
|---|---|---|---|
| `aws-serverless` | writer and notary Lambdas, S3, DynamoDB dedupe; observe and query where you like | `full` (lock on) or `lite` | [AWS](aws.md) library |
| `aws-eks` | the chart on EKS, Pod Identity, S3, notary job on KMS | `full` | chart, [direct](direct.md) or [stream](stream.md) |
| `aws-hybrid` | Lambdas for ingest and the notary in AWS, observe and query on Kubernetes with IRSA | `full` | AWS library plus chart (`writer.enabled: false`, `observe`, `query`) |
| `k8s-openbao` | the chart on any cluster, seals and keys on OpenBao Transit, S3 or compatible | `full` with a lock, else `lite` | chart (`jobs.notary` with `signer.transit`) |
| `k8s-minimal` | the chart, S3-compatible store, no lock, no notary | `lite` | chart |
| `server` | the binaries on a host, a local seal key | `lite` | [direct](direct.md) |

The two production estates:

- **Truvity** (stack `access`, cluster kernel) is `aws-hybrid`, `full`: writer and
  notary Lambdas, SQS and DynamoDB dedupe, KMS ES384 seals, Object Lock beginning
  in `GOVERNANCE` and later `COMPLIANCE`, observe and query on Kubernetes.
- **hive** is `aws-hybrid` with `k8s-openbao`'s notary, and `lite`: the writer
  Lambda and S3 with SSE-S3 and never a lock, the notary a Kubernetes CronJob
  signing with OpenBao Transit (`Notary.Disabled`, an `ArchiveWriter` role for its
  pod), observe and query on CloudNativePG.

Both Lambdas run outside a VPC.

### Writer on Lambda, observe and query in Kubernetes

This is what `aws-hybrid` asks of the chart. Set `writer.enabled: false`: the
release then renders no writer, no receiver, no stream consumers and no writer
Service, and keeps the indexer (`observe`), the query service, the migration
hook and the jobs (the notary, with OpenBao Transit or KMS). Everything in the
release that records, which is the query service's read records and the
notary's, verify's and clock-sync's, sends to the writer's ingest queue with
`sink.sqs` and the pod's own identity instead of an in-cluster front door; the
chart refuses a sink that names the release's own front door, and a
`writer.enabled: false` release whose components would run as the writer's
ServiceAccount. The values, the IAM and a worked file are in
[AWS](aws.md#observe-and-query-in-kubernetes-writer-on-lambda).

## What is implemented

| capability | code | Pulumi | chart |
|---|---|---|---|
| Writer on Lambda, SQS, DynamoDB dedupe | implemented | implemented | not applicable |
| Notary on Lambda with a KMS key | implemented | implemented | not applicable |
| Notary as a CronJob signing with OpenBao Transit | implemented (`keys.TransitSigner`) | `Notary.Disabled`, `ArchiveWriter` | implemented (`jobs.notary`, `signer.transit`; golden `transit`) |
| Notary with a local key | implemented (`signer.file`) | on request | implemented |
| Archive without a lock | implemented (`lockMode: none`) | implemented (`NONE`) | implemented |
| Lock beginning in GOVERNANCE, then COMPLIANCE | implemented | implemented | not applicable (the bucket's) |
| SSE-S3 or SSE-KMS | implemented | implemented (`Archive.Encryption`) | not applicable |
| The application's catalogue in the writer Lambda | implemented (`catalogues`) | implemented (`Writer.Catalogues`, `CataloguePaths`) | not applicable |
| Observe by IRSA from a non-EKS cluster | implemented | implemented (`Observe.IRSA`) | implemented |
| Observe and query with Postgres | implemented | not applicable | implemented |
| Observe, query and the notary in Kubernetes, the writer on Lambda | implemented (`sink.sqs` in `audit-query` and the jobs) | `QueueURL`, `QueueArn`, `Ingest.Senders` | implemented (`writer.enabled: false`; example and golden `external-writer`) |
| An S3-compatible store other than AWS (R2, MinIO) | implemented | not applicable | implemented |
| `log` level (`logsink`) | implemented | not applicable | not applicable |
| Pseudonymisation keys on Transit from a Lambda | implemented, needs a public Transit address | `Writer.Keys` | not applicable |

"On request" means designed and not built.
