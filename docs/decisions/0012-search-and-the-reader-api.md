# 0012. Search is an index fed from the log, and the reader is the audit API

Status: accepted, 2026-11-21

## Context

People need to find records, and the log is built to be verified and not searched. Search needs an index. The index holds readable copies of what the log holds sealed, so it must be kept safe, rebuilt from the log at any time, and never trusted as evidence.

audit-v1 built the same split. Its indexer listed the bucket from a cursor, its indexer and query service were separate processes with separate database roles, and its query contract (its record 0006) was a closed filter grammar with keyset cursors, facets, a get with provenance and an asynchronous export. That contract was right, and it is carried over here, re-based on OCSF.

## Decision

### The index

- **Search is the indexer, the index and the reader.** The indexer is the only writer of the index. The reader serves queries with only a read credential. They are separate processes with separate credentials.
- **The index port is atoom's** [index port](https://github.com/truvity/atoom/blob/master/index/docs/index-port.md): a cursor per log, `Apply` of the rows of a batch with the next cursor in one step, and `Search` over named partitions and tenants.
- **One index per scope, partitioned by project and log kind.** Every row carries its project, log kind and tenant, and every query names the partitions it reads.
- **The usage log has no search index.** It is read through the reader's get and export. The `billing/` prefix is not indexed either.
- **The indexer follows each indexed log of its scope by checkpoint.** It keeps one cursor per log, checks each new checkpoint's signature and its consistency with the previous one, and only then fetches the entry bundles up to the checkpoint's size. It indexes only entries that a signed checkpoint covers. Notifications only wake it. The index therefore lags the log by the seal interval plus the indexer's pass, a few seconds.
- **The indexer opens each record under the record's tenant's `evidence` key,** under a grant ([0013](0013-access.md)). It may open and never seal.
- **Indexing is idempotent, keyed by the record's hash.** The index can always be rebuilt from the log alone, is not backed up, and verification never trusts it. Rows of records past the log's retention go when the log's objects do.
- **One indexer runs per scope, and it takes no lease.** A second instance during a deploy is harmless: indexing is idempotent and each cursor only moves forward, by a conditional write in the index.
- **What the index stores:**
  - fixed columns for the core fields, mapped from OCSF paths: time, recorded at (the writer's stamp), project, log kind, log, scope, tenant, action, class and activity, outcome, actor, and the subject, target and request-id paths that the catalogue declares, trace id and record id. The column set is the same on every driver.
  - a typed attribute table for the properties the catalogue marks filterable or facetable, keyed by OCSF path;
  - the record itself, once;
  - one full-text table over the fields the catalogue marks searchable, and no other. A personal field is not searchable unless the catalogue says so.
- **A tenant's exit purges its rows,** and every later rebuild skips them. Its records stay in the log, readable under its `evidence` key until the evidence retention ends.
- **Word search without stemming, the same on every driver:** SQLite FTS5 `unicode61` with `remove_diacritics 2`, and PostgreSQL `simple` with `unaccent`. Prefix queries are allowed. There is no substring search.
- **A rebuild builds a new generation beside the serving one, then switches the reader to it atomically.** A failed rebuild leaves the old index serving. A rebuild runs after a restore, a schema or tokenizer change, a catalogue change that marks a new field, a tenant's exit, and on demand, for the whole index or for one project's partitions ([runbook](../runbooks/rebuild-the-index.md)).
- **`audit search` on the command line builds a local SQLite index from the log,** for a small installation or an auditor with read access to a project's bucket.

### Index drivers

| Driver | Where | The backend's own check | State |
|---|---|---|---|
| `sqlite` | one node | a file per indexer uid. The reader opens it read-only. | built (atoom `index/sqlite`) |
| `rqlite` | a cluster of small nodes | an rqlite cluster for the index. The indexer has the `execute` user. The reader has a `query`-only user. | planned (atoom driver) |
| `postgres` | any shape, by provisioning mode | PostgreSQL row-level security (below) | planned (atoom driver) |

- **The default index follows the shape:** `sqlite` on a single node, `rqlite` on a cluster of small nodes, `postgres` in the `aurora` mode on Lambda, `postgres` in the `cnpg` mode on Kubernetes.
- **`rqlite`** reads at a small freshness bound and at `weak` where a caller must read its own write, with writes batched in one transaction per request. Full-text search on a given node's own hardware is measured when that node exists. If it fails there, the fallback is `sqlite` on the indexer's node with the reader beside it, and no new decision is needed.
- **The `postgres` index is one driver with one provisioning mode per installation:** `rds` or `aurora` on AWS; `cnpg` on Kubernetes, in the same cluster; `external`, a connection string only, where audit provisions nothing and the operator owns security and availability. The configuration check probes the connection.
- **On Lambda the index is Aurora Serverless v2 with auto-pause.** The indexer, one instance, and the reader connect to it directly with IAM authentication, with one database role per piece and no connection proxy at first. A proxy is added if the reader's concurrency grows. The cost of auto-pause is that the first read after an idle period waits for the database to resume, and the Lambda installation's tests measure it. There is no DynamoDB index driver.
- **PostgreSQL row-level security** adds the database's own check to the reader's grant filter:
  - the indexer and the reader each have a fixed schema role, and grants separate them (the indexer writes, the reader reads);
  - row-level security on the project, log kind and tenant columns, through group-role membership, with `FORCE ROW LEVEL SECURITY` on the tables;
  - pieces never own tables, and a migration role owns them;
  - policies never trust session variables, since a caller that can run SQL can set one and a pooled connection can carry another caller's setting;
  - views are `security_invoker`, and there is no `SECURITY DEFINER` function;
  - the project and tenant columns are part of every unique key, and each role has its own connection pool;
  - PostgreSQL 15 or newer; clients authenticate with certificates (the common name is the role) or with IAM on RDS and Aurora.

### The reader

- **The reader is the audit API.** It authenticates every call itself and serves queries from the index and records from the blob. Its contract is Connect with unary calls only, with a TypeScript client from connect-es.
- **It offers** ([reference/reader-api.md](../reference/reader-api.md)):
  - **Search:** a closed filter grammar of typed predicates, keyset cursors whose next page never disappears, and free text `q` over the searchable fields;
  - **Facets:** counts over the facetable fields, for the same filter;
  - **Get:** a record as the canonical bytes the emitter produced, with its inclusion proof against a cosigned checkpoint;
  - **Export:** an asynchronous job that writes NDJSON of OCSF events, one project and log kind per export, and ends in a short-lived signed URL;
  - **A tail,** a repeated search with the cursor.

  Server streaming may come later, as an option on long-lived hosts.
- **The grammar is closed** so that a second index backend can be honest. It is a disjunction of up to four conjunctions of typed predicates, with an operator table fixed per field type. Sort is limited to indexed columns. The `next` cursor advances on recorded order, so late events are never missed.
- **The export adds enum captions, `observables` and the `record_integrity` attestation,** whose fingerprint (SHA-256 over the RFC 8785 bytes) points back to the log entry, and keeps `metadata.log_name` and the product object as written. Target-specific exporters are drivers, later. Down-mapping lives in the exporter and never in the record.
- **The reader opens a record under the record's tenant's `evidence` key** and under the grant of the call. It may open and never seal.
- **The reader merges the logs of a project by recorded time,** by the rule of [0009](0009-witnesses-and-verification.md).
- **Every read is itself audited,** with the rule that granted it.
- **Open: the index port's filters.** atoom's index port offers exact filters and a word search today. The grammar above needs more of it: typed attribute predicates, facets and the usage of the attribute table. Whether the port grows or the reader filters after the index is decided with the rewrite of that port ([atoom](https://github.com/truvity/atoom/blob/master/docs/plan.md)).

### Built and planned

Built: the SQLite index driver and port (atoom), the indexer and reader's first version (in sluis). Planned: the grammar, facets, proofs, export, tail, the `rqlite` and `postgres` drivers, the rebuild by generation, the purge at exit, and the local `audit search`.

## Consequences

- A search index can be added to a running log and dropped, because everything it holds comes from the log. A rebuild costs time and not data, and needs twice the index's storage.
- Word search without stemming finds names, ids and codes the same everywhere, but "login" does not find "logged". A newly marked field covers old records only after a rebuild.
- The index holds readable copies of what the log holds sealed per tenant, and the indexer can open every tenant's records. A tenant's exit has to reach the index, and the purge does.
- Ranking that differs between index drivers is declared.
- Every Lambda installation pays for an Aurora database, and its indexer and reader run in the VPC.
- One policy language serves both reads and writes ([0013](0013-access.md)), and reads are accountable: a read adds a record to the log.

## Roads not taken

- **List the bucket, as audit-v1 did.** A listing shows objects whether or not a checkpoint covers them, and tile names say nothing about time or tenant.
- **An index per tenant or per project, or an index of the clear envelope only.** Many indexes and a fan-out for the scope auditor's reviews, or no search beyond the envelope.
- **A lease for the indexer.** It would make an audit piece write through a store for a guarantee that idempotent indexing already gives.
- **A scan driver that reads the log for every query.** No consumer: the command line builds a local index.
- **A search engine of its own (OpenSearch or a log store), or the RDS Data API.** A heavy service to run and secure for a volume that SQLite FTS5 and PostgreSQL cover. The Data API is Aurora only, needs a secret per role and is a second SQL path to test.
- **A DynamoDB index driver for Lambda.** Not built, by decision: the index on Lambda is `postgres` in the `aurora` mode.
- **PostgreSQL isolation through session variables.**
- **Lucene or SQL passthrough as the query language.** Powerful and unportable: every backend becomes a leaky subset.
- **Offset pagination.** Unstable under inserts and expensive to count.
- **Tail on occurrence time.** It misses late events, so the tail advances on recorded order.
- **The reader facing the edge itself.** The piece that holds the index credential would parse internet input ([0014](0014-the-front-the-console-page-and-audit-verify.md)).
