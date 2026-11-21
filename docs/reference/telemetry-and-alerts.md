# Telemetry and alerts

What the pieces publish, and what to alert on. **This page is a proposal.** The metric names and thresholds are fixed when each piece moves here and is built. The rules below are decided.

## Rules

- **Telemetry is OpenTelemetry's own environment and nothing else.** The pieces use the OpenTelemetry SDK of their language. Exporters and endpoints are `OTEL_*` settings. There is no telemetry key in a configuration document.
- **Telemetry is best effort and never the audit channel.** A failed export never fails a request. It is not a source of usage ([0007](../decisions/0007-meters.md)).
- **No metric is labelled by tenant.** A tenant is unbounded, and a label that grows with customers is a series count that grows with them. A tenant appears on a trace or in the log, where it costs nothing. The labels used here have a handful of values each.
- **A record points at a trace by id, and the trace never carries what the record says.** A trace may carry the record id in an attribute, so that someone who may read both can join them. Deleting traces never weakens the log.

## Metrics (proposed)

| Metric | Type | Labels | What it answers |
|---|---|---|---|
| `audit.emit.lost` | gauge | | The total of best-effort facts that failed to deliver, over all tenants ([0005](../decisions/0005-the-security-class.md)) |
| `audit.emit.refused` | counter | `reason` | Records that the emitter refused: a bug in the emitting code |
| `audit.transport.spool.depth` | gauge | | Messages waiting. The total is the sum over spools. An alert on one spool reads the maximum. |
| `audit.writer.checkpoint.age` | gauge | `kind` | Seconds since the latest checkpoint. An idle log signs nothing, so this alone is not a fault. Read it with the spool depth. |
| `audit.writer.sealed` | counter | `kind` | Batches sealed |
| `audit.writer.refused` | counter | `reason` | Messages refused and quarantined: `stale`, `future`, `schema`, `catalogue` |
| `audit.writer.duplicates` | counter | | Messages dropped as repeats |
| `audit.writer.state.bytes` | gauge | | The size of the state object. The writer warns from 1.5 MiB. |
| `audit.writer.clock.trusted` | gauge | | 1 while the clock is trusted, 0 when it is not |
| `audit.writer.fence.conflicts` | counter | | Create-if-absent refusals that mean another writer exists |
| `audit.writer.seal.duration` | histogram | `kind` | The time to seal a batch, the signature included |
| `audit.witness.cosignature.age` | gauge | `log` | Seconds since the latest cosignature |
| `audit.index.lag` | histogram | `partition` | Seconds from a checkpoint to its rows being in the index. The seal interval is its floor. |
| `audit.index.deferred` | counter | `reason` | Entries the indexer could not index: `retry` is tried again, `unreadable` was skipped |
| `audit.reader.requests` | counter | `call`, `outcome` | Calls and their outcomes, including `denied` |

## Alerts (proposed)

| Alert | Condition | What it means | Runbook |
|---|---|---|---|
| Spool growing | `audit.transport.spool.depth` rises and does not come back down | The writer cannot keep up or cannot write | [Writer or witness outage](../runbooks/writer-or-witness-outage.md) |
| Writer stopped | Checkpoint age grows while the spool is not empty | The writer is down, or stopped by a damaged segment or a state over the limit | [Writer or witness outage](../runbooks/writer-or-witness-outage.md) |
| Fence conflict | `audit.writer.fence.conflicts` increases | A second writer believes it owns a log | [Investigate a failed verification](../runbooks/investigate-a-failed-verification.md) |
| Witness silent | Cosignature age grows beyond the idle interval (5 minutes) plus a margin | The witness or its link is down | [Writer or witness outage](../runbooks/writer-or-witness-outage.md) |
| Clock not trusted | `audit.writer.clock.trusted` is 0 for 30 minutes | The writer's time checks do not run | [Writer or witness outage](../runbooks/writer-or-witness-outage.md) |
| State large | State bytes pass 1.5 MiB | No trusted clock for a long time | [Writer or witness outage](../runbooks/writer-or-witness-outage.md) |
| Quarantine | `audit.writer.refused` increases | Messages are kept whole and wait for a decision | [Handle quarantine](../runbooks/handle-quarantine.md) |
| Lost facts | `audit.emit.lost` increases | Best-effort facts failed to deliver | [Writer or witness outage](../runbooks/writer-or-witness-outage.md) |
| Index behind | Lag p99 is above the operator's bound | The indexer fails or is slow | [Rebuild the index](../runbooks/rebuild-the-index.md) |
| Verification failed | A scheduled `audit verify` fails | An incident | [Investigate a failed verification](../runbooks/investigate-a-failed-verification.md) |

A scheduled verification is the check that the log is intact. It is a job of the installation, and it records what it checked as a security record.

## Built and planned

Not built as a set. The emitter's total and the writer's warnings exist in sluis. The rest are proposed.
