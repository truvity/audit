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
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | the sampler. Unset, a parent-based `always_on` (the SDK default): a caller's decision always wins and every new trace is kept. Set `OTEL_TRACES_SAMPLER=parentbased_traceidratio` and `OTEL_TRACES_SAMPLER_ARG=0.1` to keep a tenth |

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
| `audit_observe_index_lag_seconds` | histogram | `profile` | seconds from an object's put into the archive to its rows being in the index, as `audit-observe` measures it; the settle window is its floor |
| `audit_observe_index_deferred_total` | counter | `profile`, `reason` | objects the indexer could not index: `retry` is tried again, `unreadable` was skipped |
| `audit_observe_objects_indexed_total`, `audit_observe_records_indexed_total` | counter | `profile` | the indexer's output |
| `audit_writer_dead_lettered_total` | counter | | records the writer could not process |
| `audit_seal_age_seconds` | gauge | `profile` | seconds since the end of the newest sealed hour, of the tenant furthest behind, **as of the notary's last run** |
| `audit_seal_written_total` | counter | `profile` | seals the notary wrote |
| `audit_seal_failures_total` | counter | `profile` | tenants a notary run could not seal further |
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

**Seal age** is pushed by the notary, which is an hourly job: it reports what it
found when it ran, over OTLP at exit, and the series then stops. The alert and
the dashboard therefore add the time since the last sample
(`last_over_time(m[1d]) + (time() - tlast_over_time(m[1d]))`), so a notary that
no longer runs shows as an age that keeps climbing and not as a series that
vanished. Each profile reports the tenant furthest behind, so one stalled tenant
is seen. What the notary does not report, because it did not run, is whether it
ran: a notary that never reported at all (no collector configured for it, a job
that cannot start) leaves no series to age, and the CronJob's own state is the
evidence then.

**Index lag** is the time from an object's put to its rows being in the index,
taken from the object's own timestamp in the archive. It is a histogram and is
recorded only for objects that arrived; the ones that did not are
`audit_observe_index_deferred_total`. The indexer does not look at an object
younger than its settle window (`settle`, default 2 minutes,
[0020](../decisions/0020-observe-follows-the-bucket.md)), so the lag never
reads below that and a healthy p99 sits a little above it. The writer does not
index and counts no index metrics: they come from `audit-observe`, which
publishes over OTLP like the others.

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
| `AuditSealStale` | the newest sealed hour of a profile older than 3h, for 10m | the hourly notary seals an hour about ten minutes after it ends, so a healthy newest seal is at most about 1h20m old and one missed run leaves it near 2h20m; three hours fires on the second, when a gap in the chain has opened that no later seal can close | critical |
| `AuditIndexLagHigh` | p99 index lag above 600s, for 10m | the settle window (default 2m) is the floor of the lag, so ten minutes is an indexer that has stopped or is stuck; raise it with `settle` | warning |
| `AuditIndexRowsDeferred` | any increase in 15m | an object the indexer could not take: search is late or missing it | warning |
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

#### AuditSealStale

No hour has been sealed for a profile for three hours. An hour with no seal cannot
be told from one whose seal was removed. Look at the notary CronJob
(`kubectl get cronjob`, then the last Job's log). The usual causes are the seal
key (a KMS or OpenBAO policy, a key that is gone, a role the notary does not
have), the archive (a `Put` refused), and an object of the profile that does not
match its own metadata, which the notary refuses to seal past: its log names the
object and the rule (`hour ... is not sealed: ... problem(s) in its objects`).
The notary resumes from the last seal on its own once the cause is fixed, and
needs no operator to catch up; `audit verify --root ...` then confirms the chain.

#### AuditIndexLagHigh

Objects reach the index long after they were put, well past the settle window.
The archive is unaffected. Look at whether `audit-observe` is running and at its
log, then at the database's CPU, locks and connection pool. A large backlog (a
new index, a reset cursor) shows here too until the indexer has caught up.

#### AuditIndexRowsDeferred

The indexer could not index objects the archive holds. Its log line
`an object was not indexed` names each, and `reason` says what to do:
`retry` resumes by itself once the cause (the bucket's permissions, the
database, a catalogue missing from the archive) is fixed, and `unreadable` is an
object that does not decode and has been skipped, which is the thing to
investigate. `audit reindex --profile <name> --from <day> --to <day>` reads a
range again ([runbook](runbook.md#the-index-is-behind)).

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

`charts/audit/dashboards/audit-overview.json`, "Audit overview - $cluster": dead
letters, rejections, index rows deferred, the newest seal's age, emitter drops and
consumer failures as tiles; ingest rate by transport, the durability mix, rejection
and dead-letter rates, write latency, index lag, the age of the newest seal by
profile, and each application's emitter queue and drops.
It is generated by `hack/dashboards/audit-overview.py`, so thresholds, units and
descriptions are decided once.

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
