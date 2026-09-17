# Storage, transport, indexing and tamper evidence

## Object storage as the record

Object Lock works only on versioned buckets and locks per object version.
Compliance mode cannot be overwritten or deleted by any user including
root, and retention can be extended but never shortened. Governance mode is
bypassable with a permission the console sends by default. A default bucket
retention applies to new versions; per-object settings override it.
Lifecycle transitions still apply to locked versions; expiration cannot
delete them. Deep archive carries a 180-day minimum and per-object
overhead, so small audit objects should not be tiered there. S3 Select is
closed to new customers; Express One Zone has no versioning hence no lock.
Athena guidance: partition by day, not hour, unless daily volume is large;
combine small files before writing; partition projection removes crawlers.
Sources: https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html ,
https://docs.aws.amazon.com/AmazonS3/latest/userguide/lifecycle-transition-general-considerations.html ,
https://docs.aws.amazon.com/athena/latest/ug/performance-tuning-data-optimization-techniques.html ,
https://docs.aws.amazon.com/athena/latest/ug/partition-projection.html

## OpenTelemetry as a write path

No delivery guarantee by default: an in-memory queue that drops when full,
retries for five minutes then drops the oldest, a persistent queue that
survives restarts but not disk failure, and no end-to-end acknowledgement.
Sampling and filter processors can silently drop records. There is no
upstream NATS exporter. The logs data model never mentions audit logs.
Verdict: a secondary mirror into a log store, never the primary path.
Sources: https://opentelemetry.io/docs/collector/resiliency/ ,
https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/exporter/awss3exporter/README.md ,
https://github.com/open-telemetry/opentelemetry-collector-contrib/pull/49827

## Log stores

VictoriaLogs: LogsQL, single binary, local disk only (object-storage
backend still an open issue). Loki 3.x: single-store on object storage,
per-tenant retention via the compactor, which is the wrong layer for
provable immutability. Quickwit: acquired, Apache 2.0, maintainer-attention
risk. OpenSearch: best facets, heaviest. ClickHouse: SQL facets, S3-backed
tables, row policies. Parseable: young. Postgres with pg_partman, BRIN and
GIN: fine as a facet index over object storage.
Sources: https://github.com/VictoriaMetrics/VictoriaLogs/issues/48 ,
https://grafana.com/docs/loki/latest/operations/storage/retention/ ,
https://quickwit.io/blog/quickwit-joins-datadog ,
https://clickhouse.com/docs/cloud/bestpractices/multi-tenancy ,
https://www.jusdb.com/blog/postgresql-partitioning-pg-partman

## Transport topologies

Direct writes from every pod: no single point of failure, small-file
explosion, lost buffer on crash. Collector in between: OpenTelemetry's
guarantees. Stream with a durable consumer: publish acknowledgement,
replay, bounded horizon, no object-storage tiering, use discard-new.
Transactional outbox: atomic with the business transaction, only for
database-originated events. Fail-closed: Vault refuses requests it cannot
audit; the counter-position warns against failing a request that already
committed. Synthesis: fail-closed before the side effect for privileged
actions, an outbox for billable ones, fail-open with alerting for
informational ones.
Sources: https://docs.nats.io/nats-concepts/jetstream/streams ,
https://developer.hashicorp.com/vault/docs/audit ,
https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html

## Tamper evidence

CloudTrail's design: hourly digest objects listing each log object's hash,
chained by the previous digest's signature, signed with a per-region key,
delivered even for empty hours, validated by a CLI that walks newest-first.
KMS asymmetric keys sign a digest of at most 4096 bytes; verification can
be offline with the exported public key. Transparency logs (Tessera, Rekor
v2) serve tiles from object storage and offer inclusion and consistency
proofs, at the cost of running a service. RFC 3161 time-stamps anchor a
chain head so even the signer cannot backdate.
Sources: https://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-log-file-validation-digest-file-structure.html ,
https://docs.aws.amazon.com/kms/latest/APIReference/API_Sign.html ,
https://github.com/transparency-dev/tessera , https://blog.sigstore.dev/rekor-v2-ga/

## Indexing and multi-tenancy

A metadata index of one row per event with the object key and line keeps
PII payloads out of the database and turns a lookup into an index hit plus
a ranged GET. DuckDB reads NDJSON and Parquet from object storage for
investigations. Tenant isolation: tenant as a prefix component, row-level
security with tenant-leading indexes, customer-facing export to the
customer's own bucket via a cross-account role rather than shared
credentials.
Sources: https://aws.amazon.com/blogs/big-data/building-and-maintaining-an-amazon-s3-metadata-index-without-servers/ ,
https://duckdb.org/docs/lts/core_extensions/httpfs/s3api ,
https://www.crunchydata.com/blog/row-level-security-for-tenants-in-postgres ,
https://aws.amazon.com/blogs/storage/design-patterns-for-multi-tenant-access-control-on-amazon-s3
