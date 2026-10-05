# Recover from a receiver, writer or storage outage

## Purpose

Bring the write path back after the receiver, the writer or the object store was
down, and know what was and was not lost.

## Preconditions

- You know the installation's mode ([direct](../explanation/direct-mode.md) or
  [stream](../explanation/stream-mode.md)): most of what follows differs.
- Access to the pods' logs and to the application's metrics.

## Before you start

- **`block` actions fail while the receiver is down; `async` actions queue.** An
  `async` record waits in the emitter's bounded in-memory queue and is retried
  with backoff. It is dropped only if the queue overflows, and every drop is
  logged by the emitter as well as counted.
- **There is no outbox file.** What a pod holds is the queue, and it dies with the
  pod: one flush interval of `async` records plus the batch in flight. A `block`
  record is never lost, because the application had no acknowledgement to act on.
- **A rollout is a pause in direct mode and a backlog in stream mode.** Two
  replicas and a rolling update make the pause the time one pod takes to become
  ready; the dedupe table is in Postgres, not a pod.
- **Nothing the receiver acknowledged was lost**, because it acknowledges nothing it
  has not stored ([0017](../decisions/0017-sink-durability-and-transports.md)).

| | direct | stream |
|---|---|---|
| receiver | is the writer: one process validates and puts the object | publishes to the stream and acknowledges when it is replicated |
| writer | the receiver's own pods | `audit-writer` in consumer mode, N pods, scaled apart |
| "behind" looks like | `audit.emit.queue.pending` climbing in the application | the stream consumer's pending count |

## Steps

### 1. Tell which part is down

Two numbers from the application's own process say whether anything was lost:

| metric | what it means |
|---|---|
| `audit.emit.queue.pending` | records waiting to be delivered. Rising means the receiver is slow or down |
| `audit.emit.records.dropped` | records the queue gave up on. This is the incident |

Alert on the second and watch the first:

```
increase(audit_emit_records_dropped_total[15m]) > 0
```

The emitter's other counters are `audit.emit.records.written`,
`audit.emit.records.refused` (a bug in the emitting code, not an outage) and
`audit.emit.batches.failed`. Verify: you know whether `dropped` moved. Roll back: none.

### 2. Restore the part

#### The receiver is down

An action declared `block` fails, and the application's request fails with it:
that is what `block` is for. Restore the receiver; nothing it acknowledged was
lost.

#### The writer is down, or is being rolled

**Direct mode: a rollout is a pause.** The receiver is the writer, so while
no replica is ready, `block` calls fail and `async` records accumulate in
the applications' queues. Two replicas and a rolling update make the pause
the time one pod takes to become ready. Two replicas are safe because the
deduplication table is in Postgres and not in a pod.

**Stream mode: a rollout is a backlog.** The receiver keeps publishing and
acknowledging, the application notices nothing, and the stream's consumer
pending count rises and then falls again. The alert is a pending count that
rises and does not come back down: the writers cannot keep up, or cannot
write. Restore them; a writer resumes from its consumer position and the
dedupe table absorbs the redeliveries of whatever batch was in flight.

If the stream's horizon was exceeded, its discard-new policy refused
publishes rather than dropping, so nothing that was accepted was lost — the
refusals appear at the receiver, and from there as failed `block` calls and
a filling queue.

#### Object storage is unreachable

In direct mode the receiver cannot make anything durable, so it acknowledges
nothing: `block` fails and `async` queues, exactly as when the receiver is
down. In stream mode the writers stop acknowledging batches and the stream
holds them up to its horizon; the application is unaffected until that
horizon is reached.

Nothing is dropped in either case. After recovery, objects are written with
their original `occurred_at`; `recorded_at` shows the delay.


Verify: `/readyz` on the receiver and writer answers 200; `pending` falls. Roll
back: for a rollout that caused it, `kubectl rollout undo`.

## Afterwards

- Records written after a storage outage carry their original `occurred_at`;
  `recorded_at` shows the delay.
- If `audit.emit.records.dropped` moved, the records it counts are in the
  application's log (`Options.Logger`) and nowhere else: tell the application's owner.
- Run `audit verify` over the window ([verify the trail](verify-the-trail.md)).
