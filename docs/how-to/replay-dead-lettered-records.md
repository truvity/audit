# Replay dead-lettered records

## Purpose

Read what the writer set aside under `dlq/` in the archive, fix the cause, and
send the records through again.

## Preconditions

- Read access to the archive bucket, and a writer to send to (`--sink`).
- The cause is understood: `audit.writer.dead_lettered` names the reason
  (unknown catalogue version, schema violation, oversized).

## Before you start

- **Replay only the one cause you fixed**, not the rest. A record refused for not
  satisfying its schema will be refused again, and should be: the archive is not
  where an emitter's mistakes are corrected.
- **An unknown catalogue version is dead-lettered and acknowledged**, which neither
  queue's alarm sees. Look for `event=unknown_catalogue`, and give the writer the
  version first ([change what a source records](change-what-a-source-records.md)).
- **This is the archive's `dlq/`, not the SQS dead-letter queue.** For that one see
  [redrive the ingest DLQ](redrive-the-ingest-dlq.md).

## Steps

1. **Read what is waiting.** This sends nothing and groups the reasons.

   ```
   audit replay --dlq --bucket <b> --from 2026-09-17 --to 2026-09-17
   ```

   Verify: the reasons listed match the alert. Roll back: none.

2. **Fix the cause:** register the catalogue version, configure the missing
   profile, correct the emitter and deploy it.

3. **Replay the one cause.**

   ```
   audit replay --dlq --bucket <b> --from 2026-09-17 --to 2026-09-17 \
     --reason "no catalogue" --sink https://audit-writer:8080
   ```

   Expected: the command reports what came back under `dlq/` and exits non-zero
   if anything did. Verify: records of the replayed range are in the archive
   (`audit verify`). The records keep their identifiers, so a replay of something
   that did get through is deduplicated and costs nothing. Roll back: none; a
   replay is idempotent.

## Afterwards

Anything left under `dlq/` after replay is a record the writer will keep refusing:
report it to the emitter's owner with its reason.
