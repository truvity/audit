# 0020. Observe follows the bucket by cursor; notifications only wake it

- Status: accepted
- Date: 2026-10-02

## Context

Observe turns the archive into a search index
([0016](0016-three-parts-installed-independently.md)). Today the indexer runs
inside the writer: it is handed each record as it is stored. That makes the
index a by-product of the write path, so a rollout, a slow database or a
crash in one affects the other, and an object written by anything but that
writer is never indexed.

An index that is fed by events has the failure every event feed has. A
notification can be lost, duplicated or reordered, and an index built from
them is correct only as long as none of those happened.

## Decision

**The source of truth is a listing of the bucket.** Observe keeps a cursor
per profile and tenant: the key of the last object it indexed. A pass lists
`records/<profile>/<tenant>/` starting after that key, indexes the objects it
finds in key order, and moves the cursor. Because keys sort by ingest time
([0018](0018-v1-bucket-layout.md)), a listing from a cursor is exactly "what
has arrived since".

**Notifications only wake it.** A bucket notification, a queue message or a
timer makes observe run a pass now instead of at its next interval. A
notification carries no data the pass depends on, so a lost or duplicated
one costs latency and nothing else. Observe also runs a pass on a timer
whatever it hears.

**A settle window.** An object's key is fixed when its put starts and it
becomes visible when the put ends, so a later key can be visible before an
earlier one. Observe therefore does not advance its cursor past a key newer
than *now minus the settle window* (longer than the writer's put timeout, so
that no put can still be in flight at that age). A key behind the cursor that
appears anyway is a fault that observe reports, and a reindex over the range
repairs.

**Tenants are discovered by listing** with a delimiter under the profile.
**Indexing is idempotent**, keyed by record hash, so a pass that is repeated
or a range that is reindexed changes nothing it has already done.

**Observe stays on Postgres.** The index, the facet counts and the cursors
live in the application's existing Postgres, as today. The search contract
([0006](0006-search-contract.md)) is unchanged.

## Consequences

- **The index is rebuildable from the bucket alone**, with no event history:
  drop the cursors and run.
- **Observe can be absent, paused or replaced** and ingest does not notice.
- **Freshness has a floor**: the settle window, in minutes, is the least
  time between a record's acknowledgement and its appearance in search.
  Anything that must see a record sooner reads the sink's acknowledgement,
  not the index.
- **Several observers on one archive are fine**, each with its own cursor.

## Alternatives considered

- **Event-driven indexing**, with the bucket's notifications as the feed.
  Lower latency, and an index that is only as complete as the delivery of the
  feed. Kept as the wake-up.
- **Keep indexing inside the writer.** It is what exists, and it is the
  coupling [0016](0016-three-parts-installed-independently.md) removes.
- **A different store for the index.** Postgres already carries the
  application's other data and the search contract was written for it; a
  second engine buys nothing the contract asks for.
