# Deployment

An installation belongs to one application and runs in that application's
namespace, rendered by the application's own chart with this repository's
chart as a dependency
([0011](../decisions/0011-one-installation-per-service-or-product.md)).

There are two shapes. They write the same archive, under the same catalogue
rules and the same digest chain, and an auditor verifies either with the
same command. What differs is how a record gets from the application to the
bucket.

| shape | for | the receiver | writers | `async` loss window |
|---|---|---|---|---|
| [direct](direct.md) | an internal service, or any cluster without a stream | is the writer, and puts to the bucket | the receiver's own pods | one roll interval |
| [stream](stream.md) | a product: many pods, metering, quotas | publishes to JetStream | N consumers, scaled apart | milliseconds |

Switching between them is a change to the receiver's configuration, not to
any record.

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
| [billing](extensions/billing.md) | rollups at index time, an immutable monthly statement | a metering profile |
| [usage quotas](extensions/quotas.md) | a usage consumer, a counter cache, an hourly reconciler | stream mode |

## What each shape stores things in

| storage | direct | stream | what it holds |
|---|---|---|---|
| S3, Object Lock | the environment's bucket, under the application's prefix | the same | **the record**; nothing else is |
| Postgres | one database, in a cluster of its own if the application has none | one database in the application's existing cluster | index, dedupe table, rollups — rebuildable, not backed up |
| JetStream | not used | one stream on the application's own account | records not yet archived |
| Valkey or another cache | not used | the application's existing one, with the quotas extension | counters, corrected hourly |
| KMS | one signing key for the digest chain | the same | the chain's signature |
| OpenBAO | not used unless the deployment chooses a key provider | the same | pseudonymisation keys, off by default |
| pod disk | nothing | nothing | — |

Two of those deserve emphasis. The index is a projection: `audit reindex`
rebuilds it from the archive, so losing it costs search until it finishes,
not evidence, and it does not need a backup. And the bucket belongs to the
environment, not to the installation: one bucket with Object Lock,
replication and a deny-delete policy, under which each application writes
its own prefix.

## Before either shape

- A bucket with **Object Lock in compliance mode** and versioning, a policy
  that denies deletes to everyone, and a prefix for this application. The
  [S3 guide](../operations/s3-guide.md) has the policy.
- A **Postgres database** the writer owns, and a **read-only role** for the
  query service.
- A **signing key** for the digest chain — a KMS key is the usual choice, so
  that writing the archive and vouching for it stay separate privileges.
- A **reference clock** for the clock-synchronisation job. Every preset with
  a compliance obligation asks for a daily record of the clock's offset, and
  the chart refuses to render without one.
- Somewhere for the **Audit page** to live: the application's console, which
  calls the query service with the console's own token.
