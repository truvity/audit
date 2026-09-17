# Search

Two halves. The **index** is written on the write path and is built; the
**searcher** that reads it is not yet. This document describes both and says
which is which, because a design document that quietly describes intentions as
if they were code is worse than no document.

## The index is a projection

Nothing a search answers with is evidence. Everything in the index can be
derived again from the archive objects, and those are the ones under an object
lock that a signed digest chain accounts for. Three things follow, and they are
the reason the rest of this design looks the way it does.

A writer whose index is unreachable still writes the archive. The object is put,
the records are safe, and the rows are deferred; the deployment is told so it
can repair that day. Failing the write instead would let an outage of a search
database stop an audit trail, which is the wrong way round.

The shape of the index can change without migrating the trail. A new column, a
new facet, a different database: rebuild and the old one goes away.

An operator can throw the database away. `audit reindex` is not a recovery tool
kept for emergencies — it is the definition of what the index contains, and a
test asserts that what the writer indexed and what a rebuild produces are equal
row for row and count for count.

## Interfaces

```go
type Indexer interface {
    Index(ctx, profile string, rows []Row) error
    Purge(ctx, profile string, before time.Time, what Scope) error
}
```

`Index` is idempotent by `(profile, id)`, which is the whole contract: every hop
below the writer is at-least-once, and this is where that stops mattering.

**Counting has no call of its own.** An earlier draft of this document had
`Count(ctx, profile, deltas)` beside `Index`. It cannot work. A caller handed a
record cannot tell a re-delivery from a new record, so a caller that counts will
double-count exactly when the pipeline is doing its job. Only the transaction
that inserted the row knows whether it was new, so the counting belongs there.
`Row.Facets()` derives the deltas and the implementation applies them for the
rows its insert actually created.

Facets fall in the hour a record was **recorded**, not the hour it occurred. A
late record must land in a window still open to counting, or the counts drift
from the rows they are meant to summarise.

`Purge` takes a scope because a deployment may keep what happened for longer
than it keeps who it happened to. `Identifying` clears the actor, the subject, the
client address and the correlation identifiers and leaves the event and the
actor's *kind*; `Everything` removes the rows.

Three implementations were planned: memory, an object-storage scan, and
Postgres. Memory and Postgres are built. The object-storage scan is a searcher
with no index at all and belongs with the searcher work.

## What is indexed

The core columns come from the record. The extension properties do not: only
the properties an action's schema marked `filterable` are indexed, and only
those marked `facet` are counted. What a query may name is the catalogue's
decision, not the query writer's, and a schema that marks nothing gets an event
that is still findable by every core field.

A reindex resolves those marks against the source and version **the record
carries**, never the newest catalogue. A record written against 1.2.0 is
rebuilt against 1.2.0.

## Postgres layout

`events_core` is what happened; `events_context` is who it happened to, kept
apart so that a deployment can purge it on a shorter schedule of its own, and
can grant a reader the event without the person. No shipped preset states such
a schedule — the frameworks they cite want the actor for the whole retention —
so `audit purge --identifying-after` has no default. `events_data` holds the filterable
extension properties. `facet_counts` is what the viewer's navigation reads.

The three event tables are partitioned monthly on `recorded_at`, because a
retention expires by whole months and dropping a partition is the one way to
forget one that does not leave the table bloated. A partitioned table's unique
index must contain the partition key, so the key is `(profile, id,
recorded_at)`. That is no weaker than `(profile, id)` in practice: `recorded_at`
is stamped into the copy the archive holds, so a rebuild yields the same key,
and two copies of one record with different `recorded_at` can only exist where
deduplication was bypassed.

Partitions are created from the write path, not from a schedule. A writer
running over a month boundary at three in the morning does not stop, and a
deployment that forgot the job does not lose its index.

Indexes are tenant-leading composites, BRIN on time, GIN on `target_ids`.
Row-level security filters on `tenant_id` from a per-request setting, binding
roles that do not bypass it; the writer owns the tables and writes every
tenant's rows.

`audit migrate` applies the schema, and a writer whose database is at another
version refuses to start. Migrating is a step an operator takes, not something
several replicas race each other to do.

## Deduplication

The index database also holds `seen`, the shared record of what has been
written. It is what turns the writer from a single instance into a deployment:
with deduplication in one process only the replica that saw the original
absorbs the repeat, so the writer refuses to start more than one. See
[split-writer.md](split-writer.md) for the order of the two calls and why
asking and marking cannot be one.

## Query model

Closed by design (decision 0006). The Authorizer's grant is a filter term
AND-ed into every query. Cursors carry the keyset boundary, the direction and a
hash of the normalised query. Not yet built.

## Tail

`next` on the last page stays valid and advances on `recorded_at` plus the
writer sequence, so late events are never missed by a poller. The index orders
rows that way for the same reason. The paging itself is not yet built.

## Reindex

```
audit reindex --profile <p> --from <day> --to <day> \
    --database <url> --bucket <b> --catalogue <file>...
```

Safe to run over a range that is already indexed, which is the ordinary case:
an operator repairing an afternoon does not know exactly where the gap starts.

The catalogues are required. Without them a rebuild would produce an index
missing its data columns, and because indexing counts a record once, a later
run with the catalogues could not repair it. Refusing is the only honest
answer.
