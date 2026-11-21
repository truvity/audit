# Handle quarantine

## Purpose

Find out why the writer refused messages, keep them safe, and fix the cause so that new messages are accepted.

## Preconditions

- Read access to the project's bucket, including `quarantine/`.
- A way to read records under the tenant's `evidence` key, through the reader and a grant that covers the tenant. A quarantined message is sealed under the evidence key of the tenant its record names.
- The writer's log, and the seal entries of the log ([log layout](../reference/log-layout.md)).

## Before you start

- **A quarantined message is kept whole,** once, as `quarantine/<sha384>` in the project's bucket. Delivering it again keeps it once. Nothing is lost, and nothing is in the log.
- **A refusal never blocks the log.** The writer acknowledges the message and carries on. The seal entry of the batch says how many it refused and why (`refused_by`).
- **No tool re-emits a quarantined message yet.** This runbook finds, keeps and explains. It does not release.
- **A batch that refused messages is sealed even when it took none,** so the seal entry is the signed trace that the refusal happened.

## The reasons

| Reason | Cause | Fix |
|---|---|---|
| `stale` | The message's time, or its id's time, is more than 14 days behind the writer's clock. A spool that was down longer than the window delivers such messages. | Re-emit is the operator's work, and no tool does it. Keep the message. Decide whether the event matters, and if so record it again by the product's own means. |
| `future` | More than an hour ahead of the clock. An emitter's clock is wrong, or the writer's is. | Fix the clock. See [Writer or witness outage](writer-or-witness-outage.md#the-clock-is-not-trusted). |
| `schema` | The record does not validate against its catalogue, or its id's time is more than two minutes from its record's, or its bytes are not canonical, or it times out of validation three times | Find the emitter and its release. A bug is the product's. |
| `catalogue` | The record names a release that has no catalogue, or a newer release than the catalogue's, or an action the catalogue lacks | Publish the catalogue of that release before its emitters run ([0004](../decisions/0004-the-record-format.md)). |

## Steps

1. **See that it happened and how many.** Read the seal entries of recent batches, or the metric `audit.writer.refused`. Verify: you know the reasons and the counts.

2. **List the quarantine.**

   ```
   aws s3api list-objects-v2 --bucket BUCKET --prefix quarantine/
   ```

   Expected: one object per distinct message. Verify: the count is the number of distinct refused messages.

3. **Open a message** through the reader's `Get` with a grant that covers the tenant. Read its product, release and action, and the reason. Verify: you can name the emitter and the release.

4. **Fix the cause** in the table above. For a `catalogue` refusal, publish the catalogue and confirm that new messages of that release are accepted.

5. **Keep the evidence.** The quarantined objects stay. They are locked and expire with the project's lifecycle. If a matter needs them longer, place a hold ([Place and release a legal hold](legal-hold.md)).

## Afterwards

- Tell the owner of the emitting product which action, release and reason, and how many messages.
- The quarantine of a tenant that left stays sealed under its `evidence` key until the evidence retention ends, and then it is unreadable like the rest of its records.
- **Open:** a tool that re-emits a quarantined message. Until it exists, a message that must be in the log is written again by the product with a new record that says what happened and when.
