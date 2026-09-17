# Runbook

## The writer is down

Emitters with block delivery see publish failures and their requests fail
closed. Emitters with outbox delivery accumulate locally. The stream holds
messages up to its horizon. Restore the writer; it resumes from its
consumer position; dedupe absorbs redeliveries. If the horizon was
exceeded, the stream's discard-new policy refused publishes rather than
dropping, so nothing accepted was lost.

## Object storage is unreachable

The writer buffers, stops acking, and alerts. Nothing is dropped while the
stream horizon holds. After recovery, objects are written with their
original `occurred_at`; `recorded_at` shows the delay.

## A record was dead-lettered

`audit.writer.dead_lettered` names the reason: unknown catalogue version,
schema violation, oversized. Fix the cause (register the catalogue, fix the
emitter), then replay from `dlq/` with `audit replay --dlq --from --to`.

## Digest verification failed

Treat as an incident. `audit verify --verbose` names the object or digest.
Check for re-uploads (a new version under the same key), lifecycle
transitions that moved objects, or a KMS key change.

## The index is behind

`audit reindex --profile <p> --from <day> --to <day>`. Safe at any time.
The tail cursor advances on recorded order, so pollers catch up on their
own.

## A tenant asks for erasure

Confirm no legal hold. Destroy the tenant's pseudonymisation keys for the
purposes not under a legal duty; the security and history copies become
unlinkable. Billing and evidence copies stay under Art. 17(3)(b). Record is
automatic (`audit.key.destroyed`).

## A new source arrives

Register its catalogue; the registry validates categories against the
profiles it emits into. First records copy the schemas into the archive.
