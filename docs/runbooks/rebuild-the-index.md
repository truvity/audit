# Rebuild the index

## Purpose

Bring the search index back in step with the log when the indexer is behind, the index is wrong, or it is gone.

## Preconditions

- The log is intact: `audit verify` passes ([Verify a log](verify-a-log.md)). The index is only a projection.
- The credentials of the indexer and the reader, and for a `postgres` index the owner role of the migration.

## Before you start

- **The index is not backed up, on purpose.** Everything in it comes from the log. Losing it costs search, facets and the tail until the rebuild ends. It costs no evidence.
- **The index lags the log by a few seconds** by design: the seal interval plus the indexer's pass. A lag far above that is the fault.
- **The indexer indexes only what a signed checkpoint covers.** It verifies each checkpoint's signature and consistency before it fetches entries.
- **A rebuild builds a new generation beside the serving one,** and then switches the reader to it. A failed rebuild leaves the old index serving.
- **Every repair is safe at any time and over a range already indexed:** a record is counted once, keyed by its hash.
- **A tenant's exit purges its rows,** and a rebuild skips them.
- **The usage log has no index.** A rebuild does not touch it.

## Steps

### 1. Tell whether the index is behind

Read `audit.index.lag` and `audit.index.deferred` ([telemetry](../reference/telemetry-and-alerts.md)). Look at the indexer's log first. It names the reason:

- `retry`: the log could not be read, the database refused a write, or a record names a catalogue that is not published. The log's cursor stays where it was. Fix the cause (the credential, the database, the missing catalogue) and the next pass resumes from the cursor.
- `unreadable`: an entry that does not open or decode. It is skipped, because it will not read later either. It is the thing to look at: an entry that nothing can read is a finding. The log names its path.

Verify: the indexer's health answers, and the cursor of each log moves. Roll back: none.

### 2. Reindex some partitions

```
audit reindex --partition PROJECT/KIND [--from-size N]
```

Expected: a new generation is built for those partitions. Verify: lag falls and a search returns the range. Roll back: none needed. The old generation serves until the switch.

### 3. Rebuild the whole index

When the index is wrong or gone: after a bad migration, a partial restore or a dropped database.

1. Recreate the schema and the roles (`postgres`), or the file (`sqlite`), or the rqlite cluster.
2. Start the indexer. With no cursors it reads every indexed log of the scope from the first checkpoint, from the log alone.

A rebuild is asserted to produce the same rows and counts as following the log does. The writers keep writing throughout, and need nothing from the index.

Verify: the cursors reach each log's newest checkpoint, and a search of a known record finds it. Roll back: none needed. The log is unaffected.

### 4. After a change that needs one

Run a rebuild after: a restore, a schema or tokenizer change, a catalogue change that marks a new field filterable, facetable or searchable, and a tenant's exit.

## Afterwards

- `audit verify` reads the log only, so the log can be checked while the index is rebuilt.
- **Open: `audit reindex`.** The command is planned. Until it exists, the indexer's start with cleared cursors is the way.
