# Respond to an audit alert

## Purpose

Work out what a firing write-path alert means and what to do about it. The rules are in
[telemetry](../reference/telemetry.md#alerts); each links to its section here by name (the chart's
`alerts.runbookBaseUrl`).

## Preconditions

- The alerts are installed ([telemetry](../reference/telemetry.md#installing-them)) and routed to someone.
- Access to the writer's, indexer's and notary's logs, and read access to the archive.

## Before you start

- **`AuditEmitterDroppingRecords` means records are gone,** not late: treat it as the incident.
- **`AuditRecordsDeadLettered` is the only alert whose cause is often a catalogue out of step** with the
  emitter: the writer also logs `event=unknown_catalogue` for it, and on AWS the
  `<name>-writer-unknown-catalogue` alarm fires; give the writer the version first
  ([change what a source records](change-what-a-source-records.md)).
- **An hour with no seal cannot be told from one whose seal was removed,** so do not let
  `AuditSealStale` age.
- **The alarms on AWS are CloudWatch's, not these rules:** see
  [alarms](../reference/aws-pulumi-library.md#alarms) and [redrive the ingest DLQ](redrive-the-ingest-dlq.md).

## Steps

Find the alert's section.

### AuditRecordsDeadLettered

The writer could not process a record and kept it aside under the archive's
dead-letter prefix. The record is not lost, and it is not in the trail in its
proper form. Read the writer's log for `dead letter` lines (they name the record
id, the action and the reason), then the dead-letter objects. The usual causes
are a catalogue the writer was never given, a schema version it does not know,
and a record that does not satisfy its catalogue. Fix the cause, then replay the
dead letters (`audit replay`).

### AuditEmitterDroppingRecords

An application's async queue overflowed. The records are gone. The emitter's
log has a line for each. Look at `audit_emit_queue_pending` for the climb that
preceded it and at the sink for why it was away: the receiver down, the stream
full, the network. Raise the queue depth only after fixing the sink; a deeper
queue over a dead sink only delays the loss.

### AuditSealStale

No hour has been sealed for a profile for three hours. An hour with no seal cannot
be told from one whose seal was removed. Look at the notary CronJob
(`kubectl get cronjob`, then the last Job's log). The usual causes are the seal
key (a KMS or OpenBAO policy, a key that is gone, a role the notary does not
have), the archive (a `Put` refused), and an object of the profile that does not
match its own metadata, which the notary refuses to seal past: its log names the
object and the rule (`hour ... is not sealed: ... problem(s) in its objects`).
The notary resumes from the last seal on its own once the cause is fixed, and
needs no operator to catch up; `audit verify --root ...` then confirms the chain.

### AuditIndexLagHigh

Objects reach the index long after they were put, well past the settle window.
The archive is unaffected. Look at whether `audit-observe` is running and at its
log, then at the database's CPU, locks and connection pool. A large backlog (a
new index, a reset cursor) shows here too until the indexer has caught up.

### AuditIndexRowsDeferred

The indexer could not index objects the archive holds. Its log line
`an object was not indexed` names each, and `reason` says what to do:
`retry` resumes by itself once the cause (the bucket's permissions, the
database, a catalogue missing from the archive) is fixed, and `unreadable` is an
object that does not decode and has been skipped, which is the thing to
investigate. `audit reindex --profile <name> --from <day> --to <day>` reads a
range again ([repair or rebuild the index](rebuild-the-index.md)).

### AuditWriterRejectingRecords

A producer is sending records the writer refuses. The writer's log names the
record and the reason for each refusal. The cause is a producer sending what its
catalogue does not allow, or a catalogue that changed under it. Find the producer
by the observer on the logged records.

### AuditQueueConsumerFailing

The consumer's target refuses or fails its batches, and the queue delivers them
again. Read the writer's log for the refusal (`the writer refused a batch from
the stream`). Check the archive and the database first: the consumer fails a
batch only when the writer could not put it. Meanwhile the queue's backlog grows
and its oldest message ages; both are the broker's own metrics.

## Afterwards

Confirm the alert resolves and, for a dead letter or a drop, tell the owner of the emitting application which
records were affected ([replay dead-lettered records](replay-dead-lettered-records.md)).
