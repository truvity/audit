#!/usr/bin/env python3
"""The audit overview dashboard, generated.

Written as code so that units, thresholds and descriptions are decisions made
once. `python3 hack/dashboards/audit-overview.py > charts/audit/dashboards/audit-overview.json`
regenerates it; `just telemetry` fails when the committed file differs.

It holds to the contract of truvity/observability's docs/dashboards.md, which
its `dashboardlint` enforces: a `datasource` variable that every panel uses, a
`cluster` variable chained off it and populated by label_values(), a
`namespace` variable chained off `cluster`, `$cluster` in the title, and
`$cluster` in every query.

Series are the ones the writer, the receiver and the emitters publish over
OTLP, as the metrics gateway names them (dots to underscores, `_total` on
counters, the unit as a suffix). Only the cluster, namespace and tier become
labels from the resource, so every dimension used below (transport,
durability, profile, delivery) is a metric attribute. Nothing is labelled by
tenant: a tenant is unbounded, and a label that grows with customers is a
series count that grows with them.
"""
import json

DS = {"type": "prometheus", "uid": "${datasource}"}
K = 'k8s_cluster_name=~"$cluster"'
W = '%s,namespace=~"$namespace"' % K  # the audit installation's own series
GREEN, RED, ORANGE, BLUE = "green", "red", "orange", "blue"

panels = []
_id = [0]


def nid():
    _id[0] += 1
    return _id[0]


def target(expr, legend="", ref="A", instant=False):
    return {"datasource": DS, "editorMode": "code", "expr": expr, "legendFormat": legend,
            "range": not instant, "instant": instant, "refId": ref}


def row(title, y):
    panels.append({"id": nid(), "type": "row", "title": title, "collapsed": False,
                   "gridPos": {"h": 1, "w": 24, "x": 0, "y": y}, "panels": []})


def stat(title, desc, expr, x, y, unit="short", w=4, steps=None, no_value="no data"):
    steps = steps or [(None, GREEN), (1, RED)]
    panels.append({
        "id": nid(), "type": "stat", "title": title, "description": desc, "datasource": DS,
        "gridPos": {"h": 4, "w": w, "x": x, "y": y},
        "targets": [target(expr, instant=True)],
        "fieldConfig": {"defaults": {
            "unit": unit, "noValue": no_value, "decimals": 0, "mappings": [],
            "thresholds": {"mode": "absolute", "steps": [{"color": c, "value": v} for v, c in steps]}},
            "overrides": []},
        "options": {"colorMode": "background", "graphMode": "none", "justifyMode": "center",
                    "textMode": "value", "orientation": "auto",
                    "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}},
    })


def series(title, desc, targets, x, y, w, unit, h=8, stack=False):
    panels.append({
        "id": nid(), "type": "timeseries", "title": title, "description": desc, "datasource": DS,
        "gridPos": {"h": h, "w": w, "x": x, "y": y},
        "targets": [target(e, legend=l, ref=chr(65 + i)) for i, (e, l) in enumerate(targets)],
        "fieldConfig": {"defaults": {
            "unit": unit, "noValue": "no data",
            "custom": {"drawStyle": "line", "lineWidth": 1, "fillOpacity": 25 if stack else 10,
                       "showPoints": "never", "spanNulls": False,
                       "stacking": {"mode": "normal" if stack else "none", "group": "A"}},
            "thresholds": {"mode": "absolute", "steps": [{"color": GREEN, "value": None}]}},
            "overrides": []},
        "options": {"legend": {"displayMode": "list", "placement": "bottom", "showLegend": True},
                    "tooltip": {"mode": "multi", "sort": "desc"}},
    })


y = 0
row("Is the trail whole?", y)
y += 1
stat("Dead-lettered (1h)",
     "Records the writer could not process and kept aside. Healthy is zero: every one is a record a producer sent that the writer could not "
     "make sense of. Read the dead-letter objects in the archive and the writer's log for the reason; see the telemetry page's runbook.",
     "sum(increase(audit_writer_dead_lettered_total{%s}[1h]))" % W, 0, y)
stat("Rejected (1h)",
     "Records a hop refused for the record's own sake: an invalid record, a catalogue mismatch, a message too large. A steady count is a "
     "producer sending something it should not; the share of all writes is the alert.",
     "sum(increase(audit_sink_records_rejected_total{%s}[1h]))" % W, 4, y, steps=[(None, GREEN), (1, ORANGE)])
stat("Index rows deferred (1h)",
     "Rows of objects that reached the archive and not the index. The archive is fine; search is behind until `audit reindex` repairs the day.",
     "sum(increase(audit_writer_index_deferred_total{%s}[1h]))" % W, 8, y)
stat("Newest digest age",
     "How old the newest sealed digest window is, the worst profile. The hourly job seals the hour that has just closed, so up to about an "
     "hour is normal. Past two hours the chain has stopped growing: look at the digest CronJob.",
     "max(audit_digest_age_seconds{%s})" % W, 12, y, unit="s", steps=[(None, GREEN), (3600 * 1.5, ORANGE), (7200, RED)])
stat("Emitters dropping (1h)",
     "Records emitters gave up because their queue overflowed, across every namespace of the cluster. Healthy is zero: a drop is a record "
     "that will never exist. Look at the per-application panel below for who.",
     "sum(increase(audit_emit_records_dropped_total{%s}[1h]))" % K, 16, y)
stat("Consumers failing (1h)",
     "Batches a queue consumer's target refused or failed, to be delivered again. A few across a writer restart are normal; a count that "
     "keeps growing is a consumer going round in circles.",
     "sum(increase(audit_sink_consume_failures_total{%s}[1h]))" % W, 20, y, steps=[(None, GREEN), (5, ORANGE), (30, RED)])
y += 4

row("Ingest", y)
y += 1
series("Records acknowledged per second, by transport",
       "Records each hop acknowledged, by transport: connect-server is the writer's or receiver's front door, nats and sqs the publishers "
       "behind a receiver. The shape should follow the application's traffic.",
       [('sum by (transport) (rate(audit_sink_records_acknowledged_total{%s}[$__rate_interval]))' % W, "{{transport}}")],
       0, y, 12, "ops")
series("Durability mix",
       "What the acknowledgements promised: archived (in the bucket), queued (on a durable queue, the writer will archive it), logged "
       "(the process's log only). A deployment that requires archived should show nothing else on its front door.",
       [('sum by (durability) (rate(audit_sink_records_acknowledged_total{%s}[$__rate_interval]))' % W, "{{durability}}")],
       12, y, 12, "ops", stack=True)
y += 8
series("Rejected records per second, by transport",
       "Records refused for their own sake. Compare with the acknowledgement rate: the alert is on the share, not the count.",
       [('sum by (transport) (rate(audit_sink_records_rejected_total{%s}[$__rate_interval]))' % W, "{{transport}}")],
       0, y, 12, "ops")
series("Dead-lettered records per second",
       "Records the writer kept aside because it could not process them. Should be a flat zero.",
       [('sum(rate(audit_writer_dead_lettered_total{%s}[$__rate_interval]))' % W, "dead-lettered")],
       12, y, 12, "ops")
y += 8
series("Write latency p99, by transport",
       "The 99th-percentile time a write took at each hop. A rise at nats or sqs is the broker; at connect-server it is the archive or the index.",
       [('histogram_quantile(0.99, sum by (le, transport) (rate(audit_sink_write_duration_seconds_bucket{%s}[$__rate_interval])))' % W, "{{transport}}")],
       0, y, 12, "s")
series("Consumer failures per second",
       "Batches a queue consumer's target refused, by transport. Each is delivered again by the queue, so a sustained rate is records going "
       "round and not through.",
       [('sum by (transport) (rate(audit_sink_consume_failures_total{%s}[$__rate_interval]))' % W, "{{transport}}")],
       12, y, 12, "ops")
y += 8

row("Index and digest", y)
y += 1
series("Index lag, p50 and p99",
       "Seconds from an object being in the archive to its rows being searchable. Healthy is well under a second; rows that never arrive are "
       "counted as deferred instead and do not appear here.",
       [('histogram_quantile(0.5, sum by (le) (rate(audit_writer_index_lag_seconds_bucket{%s}[$__rate_interval])))' % W, "p50"),
        ('histogram_quantile(0.99, sum by (le) (rate(audit_writer_index_lag_seconds_bucket{%s}[$__rate_interval])))' % W, "p99")],
       0, y, 12, "s")
series("Index rows deferred, by profile",
       "Rows written to the archive and not to the index, by profile. Repair with `audit reindex` for the day; see the runbook.",
       [('sum by (profile) (increase(audit_writer_index_deferred_total{%s}[$__rate_interval]))' % W, "{{profile}}")],
       12, y, 12, "short")
y += 8
series("Digest age, by profile",
       "Seconds since the end of the newest sealed digest window, per profile. A sawtooth under about an hour is the hourly job; a line that "
       "climbs past two hours is a chain that stopped.",
       [('max by (profile) (audit_digest_age_seconds{%s})' % W, "{{profile}}")],
       0, y, 12, "s")
series("Objects and records written per second",
       "Objects the writer put into the archive and the record copies in them. Many records per object is the roll working; one record per "
       "object is a roll interval too short for the traffic.",
       [('sum(rate(audit_writer_objects_written_total{%s}[$__rate_interval]))' % W, "objects"),
        ('sum(rate(audit_writer_records_written_total{%s}[$__rate_interval]))' % W, "records")],
       12, y, 12, "ops")
y += 8

row("Emitters, by application namespace", y)
y += 1
series("Emitter queue pending, by namespace",
       "Records queued for async delivery and not yet acknowledged, per application namespace. It is what that process would lose if it "
       "died now; a line that only climbs is a sink that has been away longer than the queue is deep, and drops follow.",
       [('sum by (namespace) (audit_emit_queue_pending{%s})' % K, "{{namespace}}")],
       0, y, 12, "short")
series("Emitter drops, by namespace",
       "Records an application's queue gave up. Should be a flat zero: each is a record that will never exist.",
       [('sum by (namespace) (increase(audit_emit_records_dropped_total{%s}[$__rate_interval]))' % K, "{{namespace}}")],
       12, y, 12, "short")
y += 8
series("Emitter batches failed, by delivery",
       "Batches a sink refused or could not take, by how the application asked for delivery. Block failures fail the action being recorded; "
       "async failures are retried until the queue overflows.",
       [('sum by (delivery) (increase(audit_emit_batches_failed_total{%s}[$__rate_interval]))' % K, "{{delivery}}")],
       0, y, 12, "short")
series("Records the emitters refused as invalid",
       "Records that do not satisfy their catalogue, by application namespace: a bug in the emitting code, caught before it left the process.",
       [('sum by (namespace) (increase(audit_emit_records_refused_total{%s}[$__rate_interval]))' % K, "{{namespace}}")],
       12, y, 12, "short")


def var_ds():
    return {"name": "datasource", "label": "datasource", "type": "datasource", "query": "prometheus",
            "current": {}, "hide": 0, "includeAll": False, "multi": False, "options": [],
            "refresh": 1, "regex": "", "skipUrlSync": False}


def var_cluster():
    q = "label_values(audit_sink_records_acknowledged_total, k8s_cluster_name)"
    return {"name": "cluster", "label": "cluster", "type": "query", "datasource": DS, "definition": q,
            "query": {"query": q, "refId": "cluster-Variable-Query"}, "current": {}, "hide": 0,
            "includeAll": False, "multi": False, "options": [], "refresh": 2, "regex": "",
            "skipUrlSync": False, "sort": 1}


def var_namespace():
    q = "label_values(audit_sink_records_acknowledged_total{%s}, namespace)" % K
    return {"name": "namespace", "label": "audit namespace", "type": "query", "datasource": DS,
            "definition": q, "query": {"query": q, "refId": "namespace-Variable-Query"}, "current": {},
            "hide": 0, "includeAll": True, "allValue": ".*", "multi": True, "options": [],
            "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1}


dashboard = {
    "uid": "audit-overview",
    "title": "Audit overview - $cluster",
    "description": "The audit trail's write path: ingest, rejections, durability, dead letters, index lag, digest age, and what the emitters of each application are queueing and dropping.",
    "editable": False, "graphTooltip": 1, "refresh": "1m", "schemaVersion": 39,
    "time": {"from": "now-6h", "to": "now"}, "timezone": "", "annotations": {"list": []},
    "links": [], "tags": ["audit"],
    "templating": {"list": [var_ds(), var_cluster(), var_namespace()]},
    "panels": panels,
}

print(json.dumps(dashboard, indent=2))
