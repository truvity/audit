# Deployment shapes

An installation belongs to one application and runs in that application's
namespace, rendered by the application's own chart with this repository's
chart as a dependency
([0011](../decisions/0011-one-installation-per-service-or-product.md)).

There are two shapes. They write the same archive, under the same catalogue
rules and the same bucket layout, and an auditor verifies either with the
same command. What differs is how a record gets from the application to the
bucket.

| shape | for | the receiver | writers | `async` loss window |
|---|---|---|---|---|
| [direct](direct-mode.md) | an internal service, or any cluster without a stream | is the writer, and puts to the bucket | the receiver's own pods | one flush interval, plus the batch in flight |
| [stream](stream-mode.md) | a product: many pods, metering, quotas | publishes to JetStream | N consumers, scaled apart | milliseconds |

Switching between them is a change to the receiver's configuration, not to
any record.

A third shape is not Kubernetes at all: [AWS Lambda](aws-lambda.md) runs the writer and the
notary as Lambda functions behind an SQS queue, built by a Pulumi library. Its
status is on the [capabilities](../reference/capabilities.md) page.

The two combine: **writer on Lambda, observe and query in Kubernetes.** The
chart with `writer.enabled: false` runs only the indexer, the query service, the
migration and the jobs, and every one of them that records sends to the Lambda's
ingest queue (`sink.sqs`) under its own pod identity. No writer pod is left
behind to host a front door. See
[run observe and query in Kubernetes](../how-to/aws-run-readers-in-kubernetes.md).

How much of the stack an installation runs is a separate axis, the
[levels](levels.md): `full`, `lite` and `log`.

There is deliberately **no shape where the writer runs inside the
application**. The packages the services are built on (`writer.Open`,
`query.New`) stay public, because the binaries use them and a test may, but
a deployment that puts the bucket's credentials in the application's pods
and every fix in the application's release is not one this component
supports. [0011](../decisions/0011-one-installation-per-service-or-product.md)
says why.

## Extensions

Switched on per installation, and neither adds anything to the request path.

| extension | what it adds | needs |
|---|---|---|
| [billing](../how-to/enable-billing.md) | rollups at index time, an immutable monthly statement | a metering profile |
| [usage quotas](../how-to/enable-usage-quotas.md) | a usage consumer, a counter cache, an hourly reconciler | stream mode |

## What each shape stores things in

| storage | direct | stream | what it holds |
|---|---|---|---|
| S3, with Object Lock where a profile demands it | the environment's bucket, under the application's prefix | the same | **the record**; nothing else is |
| Postgres | one database, in a cluster of its own if the application has none | one database in the application's existing cluster | index, cursors and rollups (rebuildable, not backed up), and the writer's dedupe table and registry |
| JetStream | not used | one stream on the application's own account | records not yet archived |
| Valkey or another cache | not used | the application's existing one, with the quotas extension | counters, corrected hourly |
| OpenBAO | not used unless the deployment chooses a key provider | the same | pseudonymisation keys, off by default |
| pod disk | nothing | nothing | — |

Two of those deserve emphasis. The index is a projection: `audit reindex`
rebuilds it from the archive, so losing it costs search until it finishes,
not evidence, and it does not need a backup. And the bucket belongs to the
environment, not to the installation: one bucket with Object Lock,
replication and a deny-delete policy, under which each application writes
its own prefix.

## What an installation needs before either shape

- A **bucket** on the right tier, with a prefix for this application
  ([prepare the bucket](../how-to/prepare-the-bucket.md)).
- A **Postgres database**, an owner for the migration, and a role each for the writer, the
  indexer and the query service ([prepare the database](../how-to/prepare-the-database.md)).
- A **reference clock** for the clock-synchronisation job: every framework profile with a
  compliance obligation asks for a daily record of the clock's offset, and the job's
  configuration requires one.
- Somewhere for the **Audit page** to live: the application's console, which calls the
  query service with the console's own token.

To start: [Kubernetes](../getting-started/kubernetes.md) or
[AWS Lambda](../getting-started/aws-lambda.md); to connect a product that already runs
sluis, [a sluis-connected install](../getting-started/sluis.md).
