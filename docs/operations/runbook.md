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
schema violation, oversized. Read what is waiting first, which sends nothing
and groups the reasons:

```
audit replay --dlq --bucket <b> --from 2026-09-17 --to 2026-09-17
```

Fix the cause: register the catalogue version, configure the missing profile,
correct the emitter and deploy it. Then replay the one cause you fixed, not
the rest:

```
audit replay --dlq --bucket <b> --from 2026-09-17 --to 2026-09-17 \
  --reason "no catalogue" --sink https://audit-writer:8080
```

The records keep their identifiers, so a replay of something that did get
through is deduplicated and costs nothing. The command reports what came back
under `dlq/` and exits non-zero if anything did. A record refused for not
satisfying its schema will be refused again, and should be: the archive is not
where an emitter's mistakes are corrected.

## Digest verification failed

Treat as an incident. `audit verify --verbose` names the object or digest.
Check for re-uploads (a new version under the same key), lifecycle
transitions that moved objects, or a KMS key change.

## The clock-sync job is failing

```
audit clock-sync --ntp <server> --ntp <server> --sink <url> [--max-offset 1s]
```

It compares this machine's clock with the references and records the reading as
`audit.clock.synchronised`. A failure means either that the offset is larger
than `--max-offset` or that no reference answered; the report says which. The
reading is recorded either way when a reference did answer, because an hour
whose timestamps are suspect is the hour an auditor most wants the measurement
from. Nothing is recorded when no reference answered, because the clock was not
checked and saying it was would be worse than a red job.

The offset is the correction this clock needs: positive means it is behind.
The job never sets the clock — whatever runs the machine does that.

## The digest chain has a gap

An hour with no digest cannot be told from one whose digest was removed, which
is why `audit verify` reports both the same way. `audit digest` resumes from the
hour after the last one sealed, so a job that missed its runs catches up on its
own; run it by hand to catch up now:

```
audit digest --deployment <file> --key <file> --bucket <b> --sink <writer>
```

`--sink` is what puts `audit.digest.written` in the trail for each window
sealed; leave it off only when running by hand, where you can see the output.

It never seals the hour it wakes in — objects are still being written into it —
and it seals at most a week of windows per run, reporting how many are left. To
backfill a specific range, name it with `--from` and `--to`; a window already
sealed is left alone.

An unsigned chain proves nothing, so the command refuses without `--key`.

## The index is behind

The writer logs `object written but not indexed` with the object's key when it
puts an object it could not index. The records are safe and the object is in
the archive under its lock; what is behind is the projection.

```
audit reindex --profile <p> --from <day> --to <day> \
    --database <url> --bucket <b> --catalogue <file>...
```

Safe at any time and over a range already indexed, which is the usual case: a
record is counted once however many times it is read. The catalogues are
required — without them the rebuild would omit the data columns and a later run
could not repair it. The tail cursor advances on recorded order, so pollers
catch up on their own.

If the index is not merely behind but wrong — a bad migration, a partial
restore — drop it, run `audit migrate`, and reindex the range. Nothing in the
index is evidence, and the archive is unaffected.

## The index or the deduplication table is growing without end

Neither is bounded by anything but this:

```
audit purge --deployment <file> --database <url> [--dry-run]
```

It removes index rows past each profile's own retention and forgets written
identifiers past the deduplication window. It never touches the archive: those
objects are released by their object lock, which is what makes the retention a
retention rather than a setting.

`--identifying-after <duration>` additionally clears who an event happened to
while keeping what happened. It has no default on purpose: the presets cite
retention for the record, and none of them states a separate, shorter life for
the actor and subject columns, so the number is a deployment's own policy.

## A tenant asks for erasure

Confirm no legal hold. Destroy the tenant's pseudonymisation keys for the
purposes not under a legal duty; the security and history copies become
unlinkable. Billing and evidence copies stay under Art. 17(3)(b). Record is
automatic (`audit.key.destroyed`).

## A new source arrives

Register its catalogue; the registry validates categories against the
profiles it emits into. First records copy the schemas into the archive.
