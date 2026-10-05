# Telemetry

What the audit processes publish, what alerts watch it, and how to install the
alerts and the dashboard on the platform that runs the metrics store.

Telemetry is OpenTelemetry's own environment and nothing else
([0021](../decisions/0021-one-validated-configuration-file.md)): nothing about it
is in a configuration file. The chart's one value for it, `telemetry.otlp`,
renders that environment on every pod and nothing more
([below](#the-chart-sets-the-environment)). Each process
pushes over OTLP only when a collector is named, and publishes nothing, with no
error, when none is:

| variable | effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | the collector, for metrics and traces. The estate's gateway (`http://<gateway>:4318`) |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | one signal only |
| `OTEL_SERVICE_NAME` | defaults to `audit-writer` or `audit-query` |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | the sampler. Unset, a parent-based `always_on` (the SDK default): a caller's decision always wins and every new trace is kept. Set `OTEL_TRACES_SAMPLER=parentbased_traceidratio` and `OTEL_TRACES_SAMPLER_ARG=0.1` to keep a tenth |

### The chart sets the environment

`telemetry.otlp` is the chart's way to say where signals go, and it renders only
the variables above:

```yaml
telemetry:
  otlp:
    endpoint: http://gateway.observability.svc:4318   # empty: render nothing
    protocol: http/protobuf                           # or http/json; the exporters are OTLP/HTTP
    extraEnv:                                         # OTEL_* only
      OTEL_TRACES_SAMPLER: parentbased_traceidratio
      OTEL_TRACES_SAMPLER_ARG: "0.1"
```

With an `endpoint`, every pod the chart renders (the writer, the receiver and the
consumers, the query service, the indexer, the migration Job and each CronJob)
gets `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL` and an
`OTEL_SERVICE_NAME` of its own, then each `extraEnv` entry, sorted:
`audit-writer` for the front door, the receiver and the consumers (they are one
binary), `audit-query`, `audit-observe`, `audit-notary`, and `audit` for the
toolchain's jobs, which export nothing and carry the variables for uniformity.
Without an `endpoint` nothing is rendered, and a release that never set the
value renders byte for byte what it did before it existed. The chart refuses an
`extraEnv` key that does not start with `OTEL_` (a secret reaches a pod through
`secretFiles`, never through a value rendered into the manifest) and one that
carries `OTEL_EXPORTER_OTLP_ENDPOINT`, which has a value of its own, and an
endpoint that is not an http(s) URL.

On AWS, a function's telemetry goes through the Lambda extension rather than a
gateway address: [AWS, telemetry](../how-to/aws-send-lambda-telemetry.md).

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
| `audit_queue_message_age_seconds` | histogram | `transport` | seconds from a message being sent to a queue to the writer receiving it (AWS: the Lambda's `SentTimestamp`); a redelivery's wait is in it, so a message that keeps failing is a long tail. The depth and the dead-letter queue are CloudWatch's, and alarmed there ([AWS](aws-pulumi-library.md#alarms)) |
| `audit_writer_dead_lettered_total` | counter | | records the writer could not process |
| `audit_writer_catalogue_unknown_total` | counter | `source`, `catalogue_version` | records refused because the catalogue version they name is not registered in this writer (also dead-lettered): a writer and its emitters out of step on a catalogue. The labels are what the record said, so after 20 distinct pairs the rest count as `other`; the log line (`event=unknown_catalogue`) has them all. On AWS the library alarms on that line ([AWS](aws-pulumi-library.md#alarms)) |
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

<!-- generated: alert-rules -->
| alert | fires when | threshold and why | severity |
|---|---|---|---|
| `AuditRecordsDeadLettered` | any increase in 15m | zero is the only healthy count: the writer accepts every well-formed record | critical |
| `AuditEmitterDroppingRecords` | any increase in 15m, any namespace | a dropped record never exists | critical |
| `AuditSealStale` | the newest sealed hour of a profile older than 3h, for 10m | the hourly notary seals an hour about ten minutes after it ends, so a healthy newest seal is at most about 1h20m old and one missed run leaves it near 2h20m; three hours fires on the second, when a gap in the chain has opened that no later seal can close | critical |
| `AuditIndexLagHigh` | p99 index lag above 600s, for 10m | the settle window (default 2m) is the floor of the lag, so ten minutes is an indexer that has stopped or is stuck; raise it with `settle` | warning |
| `AuditIndexRowsDeferred` | any increase in 15m | an object the indexer could not take: search is late or missing it | warning |
| `AuditWriterRejectingRecords` | over 5% of records refused and at least 10, for 10m | a share, so one buggy producer on a busy stream is seen and one bad record on a quiet one is not | warning |
| `AuditQueueConsumerFailing` | a NATS or SQS consumer failing for 15m | one failure is a restart or an election; fifteen minutes is batches going round | critical |
<!-- /generated -->

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
  runbookBaseUrl: https://github.com/truvity/audit/blob/master/docs/how-to/respond-to-alerts.md
```

`alerts.format: prometheusrule` renders a `PrometheusRule` instead of a `VMRule`;
`alerts.rules.<name>.enabled|severity|for|labels` and the thresholds tune each
rule, and a release with every rule off is refused.

### Runbook

What to do about each is in [respond to an audit alert](../how-to/respond-to-alerts.md); each alert's section there is named for it.

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
