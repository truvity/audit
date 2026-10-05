# Repair or rebuild the index

## Purpose

Bring the search index back in step with the archive when `audit-observe` is
behind, wrong or gone, and keep the index and the deduplication table bounded.

## Preconditions

- The archive is intact (`audit verify` passes); the index is only a projection.
- The owner's database URL (`$OWNER_URL`) for `audit migrate`, and the roles for
  the writer, the indexer, the query service and the purge job.

## Before you start

- **The index is not backed up, on purpose.** Everything in it is derived from the
  archive, which is what is under the lock. Losing it costs search, facets and tail
  until the rebuild finishes, and the deduplication table, so a redelivery that
  arrives during the gap is written a second time: that costs an object, the index
  keeps one row per identifier, and a reader sees the record once. No record is lost.
- **An index is always at least the settle window behind** (`settle`, default two
  minutes), by design ([0020](../decisions/0020-observe-follows-the-bucket.md)). A
  put the indexer has not reached yet is not a fault; a lag far above the window is.
- **A writer whose database is at another schema version refuses to start**, and the
  writer and indexer never migrate themselves: several replicas would race. Run
  `audit migrate` before starting them against a new database.
- **A record's catalogue is read from the archive's `catalogue/<app>/<version>`**
  unless a file is given; one that cannot be found is an error, because a rebuild
  without it would omit the data columns.
- **Every repair is safe at any time and over a range already indexed:** a record is
  counted once however many times it is read.

## Steps

### 1. Tell whether the index is behind

The indexer says so two ways, when a collector is named
(`OTEL_EXPORTER_OTLP_ENDPOINT`, set on the pod by the platform):

- `audit.observe.index.lag`, a histogram by profile: seconds from an object's put to
  its rows being in the index. `AuditIndexLagHigh` is its p99 above ten minutes.
- `audit.observe.index.deferred`, a counter by profile and `reason`. Alert on any
  increase:

  ```
  increase(audit_observe_index_deferred_total[15m]) > 0
  ```

Look first at whether `audit-observe` is running and at its log. It logs `an indexing
pass failed; the next one resumes from the cursors` and retries tenant by tenant:

- `reason=retry`: the archive could not be read, the database refused a write, or a
  record names a catalogue that is not in the archive. The tenant's cursor stays
  where it was; fix the cause (the bucket's permissions, the database, or the
  missing `catalogue/<app>/<version>`) and the next pass resumes.
- `reason=unreadable`: an object that does not decode. It is skipped, because it will
  not read later either, and it is the thing to look at: an object in the archive
  that nothing can read is a finding. The log names its key.

Notifications only shorten the wait (`wake`), so an installation without them is not
behind by more than `interval`. Verify: `/readyz` on the indexer answers. Roll back: none.

### 2. Read a profile again from the start

```
audit reindex --profile <p> [--tenant <t>] --reset-cursor --database <url>
```

### 3. Or read a range of ingest days directly

```
audit reindex --profile <p> --from <day> --to <day> \
    --database <url> --bucket <b> [--catalogue <file>...]
```

Verify: the lag histogram falls and a search returns the range. The tail cursor
advances on recorded order, so pollers catch up on their own.

### 4. If the index is wrong, or gone

A bad migration, a partial restore, a dropped database: drop what is left, recreate the
schema and the roles, and start `audit-observe`.

```
audit migrate --database "$OWNER_URL" --writer audit_writer \
    --observe audit_observe --reader audit_query --purge audit_purge
```

With no cursors the indexer reads every profile and tenant from the first key,
oldest first, from the bucket alone. A rebuild is asserted to produce the same rows
and counts as following the bucket does. To rebuild a range sooner, or one profile
first, use `audit reindex --from/--to`. Writers keep writing throughout and need only
the deduplication table: start them against the new database once `audit migrate`
has run. Roll back: none needed; the archive is unaffected.

### 5. Keep it bounded

Neither the index nor the deduplication table is bounded by anything but this:

```
audit purge --deployment <file> --database <url> [--dry-run]
```

It removes index rows past each profile's own retention and forgets written
identifiers past the deduplication window. It never touches the archive: those objects
are released by their object lock. `--identifying-after <duration>` additionally
clears who an event happened to while keeping what happened; it has no default on
purpose, because none of the framework profiles states a separate, shorter life for
the actor and subject columns.

## Afterwards

- `audit verify` reads the archive only, so the trail can be checked while the index is
  rebuilt ([verify the trail](verify-the-trail.md)).
- Writer counters to watch: `audit.writer.objects.written`, `audit.writer.records.written`,
  `audit.writer.dead_lettered` (a fault upstream is otherwise silent),
  `audit.writer.meta.dropped`, `audit.writer.duplicates.likely`,
  `audit.writer.retention.not_extended`.
- The last one means an addendum could not lengthen the lock on an earlier record. The
  `audit.retention.extended` record with outcome failure says which record and why
  (typically the record is older than the scan's horizon, or the role lacks
  `s3:PutObjectRetention`). Fix the cause and lengthen it by hand; repeating is safe:

  ```
  aws s3api put-object-retention --bucket <b> --key <object> \
      --retention Mode=COMPLIANCE,RetainUntilDate=<retain_until from the record>
  ```
