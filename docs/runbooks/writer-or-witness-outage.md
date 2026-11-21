# Recover from a writer or witness outage

## Purpose

Bring the write path back after the writer, the transport, the blob store or a witness was down, and know what was and was not lost.

## Preconditions

- You know the transport driver of the installation: the spool, SQS or NATS JetStream, and whether the emitters use direct mode with a spool as fallback.
- Access to the writer's log and to the metrics ([telemetry](../reference/telemetry-and-alerts.md)).

## Before you start

- **Nothing the transport answered was lost.** A transport answers an emit only when the message is durable. A message waits until the writer seals it and acknowledges it.
- **What fails depends on the class.**
  - An operation that produces a **usage** record, or that issues a credential, **fails** while the transport cannot accept the record. The operator sees an error, and nothing is silently missing.
  - A **best-effort** security fact is lost if the transport refuses it (a full or absent spool). The loss is counted and reported in the tenant's next record.
  - A **tightening change**, such as a revocation, never waits.
- **The writer may be down while the spool is healthy.** Then emits are accepted and nothing is lost.
- **A rollout is a pause with a spool, and a backlog with a queue.** The writer resumes from the oldest message not yet acknowledged, and its state removes the repeats of a batch in flight.
- **Records written after an outage keep their time of use.** The recorded time shows the delay.

## Steps

### 1. Tell which part is down

| Signal | Means |
|---|---|
| Spool depth rises and checkpoint age grows | The writer is down or stopped |
| Spool depth is zero and checkpoint age grows | The log is idle: not a fault |
| `audit.emit.lost` increases | Best-effort facts failed to deliver: the spool was full or absent |
| Usage operations fail | The transport does not accept records |
| Cosignature age grows | A witness or its link is down |
| The writer exited with an error that names a segment, or `ErrStateTooLarge`, or a fence conflict | See below |

Verify: you know whether `audit.emit.lost` moved, and since when. Roll back: none.

### 2. Restore the part

**The writer is down.** Start it. It reads its state and the newest checkpoint, adopts the entries of a crashed batch, and resumes the transport from the oldest message not yet acknowledged. After a failure it opens the stream again after 1 second, then 2, doubling to 30 seconds. Verify: the checkpoint age falls and the spool depth falls.

**The transport is full or absent.** Restore the disk or the queue. Messages the emitters already took are kept. Messages that were refused are lost if best effort, and the counted note says how many.

**The blob store is unreachable.** The writer cannot make anything durable, so it acknowledges nothing. Emits wait in the transport. Restore the store. Credentials that were refused with 401 or 403 are read again once without a cache, so a rotated credential recovers by itself.

**The writer stopped on a damaged segment.** A segment of remembered ids that the state names is missing or differs. The error names the segment. Restore the object from the bucket's backup or version history. No tool rebuilds a segment from the log yet. The writer stays stopped until the object is back, and the batch it needed is delivered again.

**The writer stopped with `ErrStateTooLarge`.** The writer has no trusted clock, and its state reached 3 MiB. It warned from 1.5 MiB. The fix is a time source (next section). Once the clock is trusted the next seal drops every hour older than the window, and the state shrinks.

**A fence conflict.** A second writer believes it owns the log, or a restore ran twice. Stop one of them. The conflict is an error and not a fork, because every object is created only if absent. Then [verify the log](verify-a-log.md).

### The clock is not trusted

The writer trusts its clock only when the kernel answers and does not say it is unsynchronised, and the wall clock has not stepped. When it does not, it accepts and remembers: it neither refuses by time nor forgets an id. The seal entry says `time_checks` `run: false` with `unobserved`, `unsynchronised` or `stepped`. Give the node a time source. A step that holds for 30 minutes with the kernel calling the clock synchronised becomes the new anchor.

### The witness is down

The log keeps sealing. Only the window in which the writer alone vouches for the log grows. Restore the witness. The writer asks again for the latest checkpoint every 5 minutes while the log is idle, and every new checkpoint at most every 5 seconds. Older checkpoints that no witness cosigned stay uncosigned: a later cosigned checkpoint shows that the log grew consistently. A restore must not bring a witness back to an older state: re-register the node with its peers. Verify: the cosignature age falls.

## Afterwards

- Run `audit verify` over the window ([Verify a log](verify-a-log.md)).
- If `audit.emit.lost` moved, the records it counts are in the tenants' next records as the note of lost facts, and in the callers' journals, and nowhere else. Tell the owners of the emitting products.
- If usage operations failed, tell the product: its operations failed and did not happen.
- After a long outage, check [quarantine](handle-quarantine.md): messages older than 14 days are quarantined as `stale`.
