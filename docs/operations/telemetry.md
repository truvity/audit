# Telemetry

What the audit processes publish, what alerts watch it, and how to install the
alerts and the dashboard on the platform that runs the metrics store.

Telemetry is OpenTelemetry's own environment and nothing else
([0021](../decisions/0021-one-validated-configuration-file.md)): nothing about it
is in a configuration file or a chart value of the write path. Each process
pushes over OTLP only when a collector is named, and publishes nothing, with no
error, when none is:

| variable | effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | the collector, for metrics and traces. The estate's gateway (`http://<gateway>:4318`) |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | one signal only |
| `OTEL_SERVICE_NAME` | defaults to `audit-writer` or `audit-query` |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | the sampler. Unset, a parent-based ratio of 0.1: a caller's decision always wins, and a tenth of new traces is kept |

The gateway turns delta temporality into cumulative and keeps only the cluster,
namespace and tier from the resource as labels
([truvity/observability, emitting](https://github.com/truvity/observability/blob/master/docs/emitting.md)).
So every dimension below is a metric attribute, never a resource attribute.

## Metrics

Names are as the gateway stores them: dots become underscores, a counter gains
`_total`, a unit becomes a suffix.

| series | type | labels | what it answers |
|---|---|---|---|
| `audit_sink_records_acknowledged_total` | counter | `transport`, `durability` | ingest rate, and the durability mix: archived, queued or logged |
| `audit_sink_records_rejected_total` | counter | `transport` | records refused for their own sake |
| `audit_sink_write_duration_seconds` | histogram | `transport`, `outcome` | how long a write took at each hop |
| `audit_sink_consume_failures_total` | counter | `transport` | batches a queue consumer's target failed, to be delivered again |
| `audit_writer_index_lag_seconds` | histogram | `profile` | seconds from an object in the archive to its rows in the index |
| `audit_writer_index_deferred_total` | counter | `profile` | rows that reached the archive and not the index |
| `audit_digest_age_seconds` | gauge | `profile` | seconds since the newest sealed digest window ended |
| `audit_writer_dead_lettered_total` | counter | | records the writer could not process |
| `audit_writer_objects_written_total`, `audit_writer_records_written_total` | counter | `profile` | the writer's output |
| `audit_emit_records_dropped_total` | counter | `action` | records an emitter's queue gave up |
| `audit_emit_queue_pending` | gauge | | records an emitter holds, unacknowledged |
| `audit_emit_batches_failed_total`, `audit_emit_records_written_total`, `audit_emit_records_refused_total` | counter | `delivery` or `action` | the emitter's other counts |

`transport` is one of `connect-server` (the writer's or receiver's front door),
`connect-client`, `nats` and `sqs`. `durability` is `archived`, `queued`,
`logged` or `unspecified`.

**Cardinality.** No series is labelled by tenant. A tenant is unbounded, a label
that grows with customers is a series count that grows with them, and a series
with too many labels is dropped by the store while the write answers 200. The
labels used here have a handful of values each: profiles are the few names a
deployment chose, transports are four, durabilities three. The tenant is on the
span, where it costs nothing.

**The digest age is observed by the writer**, which runs all day, and not
pushed by the hourly job: a job that exits in seconds is attributed by the
gateway to the previous holder of its address, and its series goes stale minutes
later. The writer asks the archive for each profile's most recent digests (one
`HEAD` per hour, backwards, at most a day, at most every five minutes), so a
healthy chain costs one request. A profile with no digest in the last day
reports 24 hours.

**Index lag** is the insert's own time after the put. It is a histogram and
is recorded only for rows that arrived; rows that did not are
`audit_writer_index_deferred_total`.

## Traces

One span per write at each hop, continued across processes:

| span | kind | where |
|---|---|---|
| HTTP server, then the RPC | server | writer, receiver and query service (`otelhttp`, `otelconnect`) |
| `audit.sink.write <transport>` | client, producer, internal | `sink.Client`, the NATS and SQS publishers, the front door |
| `audit.sink.consume <transport>` | consumer | the NATS and SQS consumers |

The W3C `traceparent` crosses a queue in the message: NATS message headers and
SQS message attributes (`traceparent`, `tracestate`). A consumer's span is a
child of the first message's trace and links up to sixteen others, since one
write is many messages. The liveness probe is not traced.

**No personal data.** Spans are read unscoped by everyone with any grant on the
trace store, so this is enforced where spans leave the process, not left to each
caller: the exporter drops every attribute not on
`telemetry.SpanAttributeAllowlist`, and every span event, status text and link
attribute (a recorded error's message can quote a record). Allowed: action,
outcome, tenant id, durability, delivery, record and rejection counts,
transport, and the shape of the RPC or HTTP request (method, route, status, path).
Never the actor, a subject, a client address or record data. A test holds the
instrumentation and the exporter to the list.

## Alerts

Seven rules in one group, `audit.write-path`. Each threshold and its reason is
in the comment above the rule in `charts/audit/templates/alerts.yaml`.

| alert | fires when | threshold and why | severity |
|---|---|---|---|
| `AuditRecordsDeadLettered` | any increase in 15m | zero is the only healthy count: the writer accepts every well-formed record | critical |
| `AuditEmitterDroppingRecords` | any increase in 15m, any namespace | a dropped record never exists | critical |
| `AuditDigestStale` | newest digest older than 2h, for 10m | a healthy hourly chain is at most about 1h05m; two hours tolerates one missed run | critical |
| `AuditIndexLagHigh` | p99 index lag above 30s, for 10m | the insert takes milliseconds; 30s is two orders over | warning |
| `AuditIndexRowsDeferred` | any increase in 15m | search answers wrongly until a reindex | warning |
| `AuditWriterRejectingRecords` | over 5% of records refused and at least 10, for 10m | a share, so one buggy producer on a busy stream is seen and one bad record on a quiet one is not | warning |
| `AuditQueueConsumerFailing` | a NATS or SQS consumer failing for 15m | one failure is a restart or an election; fifteen minutes is batches going round | critical |

### Installing them

The chart renders only the rules when `renders: alerts`, so the platform that
runs the ruler installs them a second time, beside the release that runs the
write path, the way truvity/openbao installs `openbao-ops` in alert-only mode.
No write-path values are needed or validated:

```yaml
renders: alerts
alerts:
  namespace: audit              # where the write path runs: the writer rules' series
  objectNamespace: monitoring   # where the ruler reads rule objects from
  emitterNamespace: ".+"        # the applications' namespaces: their series carry theirs
  ruleLabels:                   # on every rule, for routing
    k8s_cluster_name: prod
  runbookBaseUrl: https://github.com/truvity/audit/blob/master/docs/operations/telemetry.md
```

`alerts.format: prometheusrule` renders a `PrometheusRule` instead of a `VMRule`;
`alerts.rules.<name>.enabled|severity|for|labels` and the thresholds tune each
rule, and a release with every rule off is refused.

### Runbook

#### AuditRecordsDeadLettered

The writer could not process a record and kept it aside under the archive's
dead-letter prefix. The record is not lost, and it is not in the trail in its
proper form. Read the writer's log for `dead letter` lines (they name the record
id, the action and the reason), then the dead-letter objects. The usual causes
are a catalogue the writer was never given, a schema version it does not know,
and a record that does not satisfy its catalogue. Fix the cause, then replay the
dead letters (`audit replay`).

#### AuditEmitterDroppingRecords

An application's async queue overflowed. The records are gone. The emitter's
log has a line for each. Look at `audit_emit_queue_pending` for the climb that
preceded it and at the sink for why it was away: the receiver down, the stream
full, the network. Raise the queue depth only after fixing the sink; a deeper
queue over a dead sink only delays the loss.

#### AuditDigestStale

No digest window has been sealed for the profile in over two hours. Check the
digest CronJob (`kubectl -n <ns> get cronjob`, its last Job and its log): a
suspended job, a signer that cannot sign (KMS permission, OpenBAO sealed), or
an archive that refuses the put. The job catches up on the windows it missed, so
once it runs the chain has no gap. A profile that has no digest at all reports
24 hours: either the chain never started or the job names another profile set.

#### AuditIndexLagHigh

Index inserts are slow. Rows still arrive and the archive is unaffected. Look at
the database's CPU, locks and connection pool, and the writer's replicas count
against the pool size.

#### AuditIndexRowsDeferred

The index refused or could not take rows of objects the archive holds. The log
line `object written but not indexed` names the object key. Once the database is
healthy repair the day: `audit reindex --profile <name> --from <day> --to <day>`
([runbook](runbook.md#the-index-is-behind)).

#### AuditWriterRejectingRecords

A producer is sending records the writer refuses. The writer's log names the
record and the reason for each refusal. The cause is a producer sending what its
catalogue does not allow, or a catalogue that changed under it. Find the producer
by the observer on the logged records.

#### AuditQueueConsumerFailing

The consumer's target refuses or fails its batches, and the queue delivers them
again. Read the writer's log for the refusal (`the writer refused a batch from
the stream`). Check the archive and the database first: the consumer fails a
batch only when the writer could not put it. Meanwhile the queue's backlog grows
and its oldest message ages; both are the broker's own metrics.

## The dashboard

`charts/audit/dashboards/audit-overview.json`, "Audit overview - $cluster":
dead letters, rejections, index rows deferred, the newest digest's age, emitter
drops and consumer failures as tiles; ingest rate by transport, the durability
mix, rejection and dead-letter rates, write latency, index lag, digest age by
profile, and each application's emitter queue and drops. It is generated by
`hack/dashboards/audit-overview.py`, so thresholds, units and descriptions are
decided once.

It holds to truvity/observability's dashboard contract: a `datasource` variable
that every panel uses (no UID is written into it), a `cluster` variable chained
off it and filled by `label_values`, a `namespace` variable chained off
`cluster`, `$cluster` in the title and in every query.

Install it where Grafana runs, as its own release:

```yaml
renders: dashboards
dashboards:
  namespace: monitoring   # Grafana's
  folder: Audit
```

One ConfigMap per dashboard, labelled `grafana_dashboard: "1"` for the sidecar.
The folder is honoured only when the sidecar reads
`k8s-sidecar-target-directory` (`sidecar.dashboards.folderAnnotation`).

## How it is proven

`just telemetry` (a CI recipe) runs, with every tool pinned:

- the dashboard is regenerated and must equal the committed file;
- `dashboardlint`, from truvity/observability at a pinned release by `go run`,
  runs over it, and over a copy with a literal datasource, which it must refuse;
- every rule is rendered and unit-tested with `vmalert-tool unittest` from the
  same VictoriaMetrics release the observability stack runs, downloaded with its
  published checksum verified: each rule has a test that fires it, with its
  labels and text, and at least one that must not, and a test refuses a rule
  with only one kind;
- the chart's refusals (`tests/invalid/audit/alerts-*.yaml`) and the golden
  renders of both modes (`tests/golden/audit/alerts.yaml`, `dashboards.yaml`).
