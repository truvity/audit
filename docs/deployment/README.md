# Deployment

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
| [direct](direct.md) | an internal service, or any cluster without a stream | is the writer, and puts to the bucket | the receiver's own pods | one flush interval, plus the batch in flight |
| [stream](stream.md) | a product: many pods, metering, quotas | publishes to JetStream | N consumers, scaled apart | milliseconds |

Switching between them is a change to the receiver's configuration, not to
any record.

A third shape is not Kubernetes at all: [AWS](aws.md) runs the writer and the
notary as Lambda functions behind an SQS queue, built by a Pulumi library. It is
built and tested and has not run in an account.

The two combine: **writer on Lambda, observe and query in Kubernetes.** The
chart with `writer.enabled: false` runs only the indexer, the query service, the
migration and the jobs, and every one of them that records sends to the Lambda's
ingest queue (`sink.sqs`) under its own pod identity. No writer pod is left
behind to host a front door. See
[AWS](aws.md#observe-and-query-in-kubernetes-writer-on-lambda).

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
| [billing](extensions/billing.md) | rollups at index time, an immutable monthly statement | a metering profile |
| [usage quotas](extensions/quotas.md) | a usage consumer, a counter cache, an hourly reconciler | stream mode |

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

## Before either shape

- A bucket with versioning, a policy that denies deletes to everyone, and a
  prefix for this application — with **Object Lock in compliance mode** for a
  profile that demands it, on any S3-compatible store without one where none
  does ([0014](../decisions/0014-lock-modes-and-store-tiers.md)). The
  [S3 guide](../operations/s3-guide.md) has the policy.
- A **Postgres database**, an owner for the migration, and a role each for the
  writer, the indexer and the query service (read-only). See [one more database](#one-more-database-in-a-cluster-you-already-run).
- A **reference clock** for the clock-synchronisation job. Every preset with
  a compliance obligation asks for a daily record of the clock's offset, and
  the job's configuration requires one.
- Somewhere for the **Audit page** to live: the application's console, which
  calls the query service with the console's own token.

## One more database in a cluster you already run

The index is a projection: `audit reindex` rebuilds it from the archive, so it
needs no backup and no replica. That makes it cheap to put in a Postgres the
application already has, rather than running one for it.

Make a database and four roles in that cluster: an owner the migration runs as,
and one role each for the parts that use it, the writer, the indexer
(`audit-observe`) and the query service. They must be different roles. The
tenant row-level policies bind the query service's role; an owner bypasses them,
so a part connecting as the owner would hold more than its own tables and would
have every tenant's isolation rest on the service alone. The chart refuses to
render when the writer, the indexer or the query service names the owner's, or
one another's, credentials.

```sql
create database audit;
create role audit_owner login password :'owner';
create role audit_writer login password :'writer';
create role audit_observe login password :'observe';
create role audit_query login password :'reader';
grant all privileges on database audit to audit_owner;
```

Then apply the schema and grant each role what its part needs, in one step:

```console
$ audit migrate --database "$OWNER_URL" --writer audit_writer \
    --observe audit_observe --reader audit_query
```

`--writer` gives the deduplication table, the registry and the key directory,
and none of the index; `--observe` gives the index and its cursors, and the one
function that creates a month's partition; `--reader` gives select on the
index and nothing else (each is a key of the job's file as well). The writer
and the indexer refuse to start against a schema version they do not know and
never migrate themselves: several replicas would race.

With an operator that manages clusters declaratively, the same thing is a
database and two users in the cluster's own manifest, and the migration is the
chart's pre-install hook (`migrate`, whose config carries the roles). Name each
role's password variable in `database.passwordEnv` of its own component, and
supply each from its Secret with `secretEnv`.
