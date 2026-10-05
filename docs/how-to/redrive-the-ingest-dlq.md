# Redrive the ingest dead-letter queue

## Purpose

Move the messages that landed in the ingest queue's DLQ back to the ingest queue
once the cause is fixed, so that the writer Lambda archives them.

## Preconditions

- The installation runs on AWS Lambda ([getting started](../getting-started/aws-lambda.md)).
- You know `DlqArn` and `QueueArn` (stack outputs `DlqArn` and `QueueArn`).
- An operator role that is **named in `Ingest.Redrivers`** (a break-glass role).
  Its identity policy has `sqs:SendMessage` on `QueueArn`,
  `sqs:StartMessageMoveTask` on the DLQ, and `sqs:ReceiveMessage` and
  `sqs:DeleteMessage` on the DLQ.

## Before you start

- **The move task sends to the ingest queue as the caller.** The queue policy
  denies `sqs:SendMessage` to every principal that is not named, so an operator
  who is not in `Ingest.Redrivers` is refused even with the right identity
  policy. `Redrivers` is allowed and excepted from the deny, and is not a sender
  the trail relies on. A stack change (`pulumi up`) adds it.
- **`Senders` and `Redrivers` must be role or user ARNs**
  (`arn:aws:iam::<account>:role/<path>/<name>`), which is what `aws:PrincipalArn`
  carries for a role session. Not an assumed-role session ARN
  (`arn:aws:sts::<account>:assumed-role/<name>/<session>`), not an `sts` ARN, not a
  bare account id and no wildcard: the library refuses those at preview, on the
  resolved values. What it looks like when you get it wrong: a preview that fails
  naming the field, or, if the operator simply is not listed, an `AccessDenied`
  from `StartMessageMoveTask`.
- **Fix the cause first.** A message moved back with its cause in place goes
  round again and returns to the DLQ after `MaxReceiveCount` deliveries (the
  writer's catalogue, a bad release). A record naming a catalogue version the
  writer lacks is **not** in this DLQ: it was dead-lettered in the archive
  and acknowledged. See [replay dead-lettered records](replay-dead-lettered-records.md)
  and the `<name>-writer-unknown-catalogue` alarm.
- **A message that is not a record is a poison message.** It will return to the
  DLQ every time; inspect it before redriving the batch.

## Steps

1. **See what is waiting.**

   ```sh
   aws sqs get-queue-attributes --queue-url "$DLQ_URL" \
     --attribute-names ApproximateNumberOfMessages
   ```

   Expected: a count above zero (the `<name>-ingest-dlq-not-empty` alarm is why you
   are here). Roll back: none; this reads.

2. **Fix the cause** (the writer's release, the catalogue, the operator's own
   permission) and deploy it. Verify: the writer's log shows no new failures.
   Roll back: redeploy the previous program.

3. **Start the move.**

   ```sh
   aws sqs start-message-move-task --source-arn "$DLQ_ARN" --destination-arn "$QUEUE_ARN"
   ```

   Or the console's "Start DLQ redrive". Expected: a `TaskHandle`. Roll back:
   `aws sqs cancel-message-move-task --task-handle <handle>` stops the task;
   messages already moved stay in the ingest queue and are written by the writer.

4. **Verify.**

   ```sh
   aws sqs list-message-move-tasks --source-arn "$DLQ_ARN"
   ```

   Expected: the task `COMPLETED` and the DLQ empty. The records keep their
   identifiers, so one that did get through is deduplicated and costs nothing.

## Afterwards

- The `<name>-ingest-dlq-not-empty` alarm returns to OK.
- If messages came back to the DLQ, the cause was not the one you fixed: read the
  writer's log for the reason before trying again.
- Tell the owner of the emitting application which records were late: they carry
  their original `occurred_at`, and `recorded_at` shows the delay.
