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

Three implementations, all built: memory, Postgres, and an object-storage scan
with no index at all, for a deployment too small to run a database.

One implementation behind an interface is a description of that
implementation's habits with an interface drawn around it. The second finds
where the interface claimed something the first only happened to do. The third,
here, is the one that cannot cheat: a searcher backed by a table can quietly
grow a capability the interface never promised, and one that must open objects
and read them cannot.

What `Matches` means is defined once, in `index`, and exported for exactly that
reason — a searcher that worked the operators out again would be a second
opinion on the contract.

The scan states what it gives up rather than hiding it. `Capabilities` says it
counts no facets, because counting means reading everything that matches, which
is the work it exists to bound; it orders only by occurred time, because the
archive is laid out by day and any other order means reading everything before
answering. Each is refused with the reason rather than answered narrowly.

Its cursor is a place in the archive — an object and a line — where the indexed
searchers carry sort values. A cursor therefore belongs to the searcher that
issued it as well as to the query, and one from elsewhere is refused.

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

Closed by design (decision 0006). A caller names fields and operators from a
fixed set rather than handing over an expression, which is what lets the
Authorizer's grant be AND-ed in as one more term — there is no path to a row
outside the grant, because a query that forgot it would have to forget to name
a profile too.

Built for Postgres: predicates on the core fields, on targets, and on the
extension properties a catalogue marked filterable. No regular expression and
no substring search: both are unbounded work on a table that only grows, and an
index that cannot answer them quickly would answer them slowly instead. A
prefix is a range rather than a pattern, so it reads the same index a sorted
scan does.

Paging is keyset, never an offset. An offset re-reads everything before it, so
a deep page costs more than a shallow one, and a row appended meanwhile shifts
every page after it — in a trail that is appended to constantly, that means a
poller silently skipping records. Every ordering ends with the identifier, or
two rows with the same occurred time would have no defined order and a boundary
on the tie would repeat one or skip the other.

## Tail

`next` on the last page stays valid and advances on `recorded_at` plus the
writer sequence, so late events are never missed by a poller. The index orders
rows that way for the same reason. An empty page keeps the boundary it was
asked from, so a tail polling a quiet profile does not lose its place.

## Reading is recorded

Every answer produces a record: `audit.search`, `audit.facets`, `audit.get`,
naming the caller, the target and the rule that allowed it. A trail that shows
what everyone did except who looked at it is missing the half an investigation
usually starts from.

A refused read is recorded as well. An attempt to read the trail is a fact about
who was looking, and the refused one is the more interesting of the two.

A record the grant does not cover is reported as **absent**, not as forbidden:
"no such record" and "a record you may not read" are the same answer to someone
who should not know it exists.

The service carries an `OnUnrecorded` hook and a deployment alerts on it.
Reading going unrecorded is not a degraded service — it is the service failing
at one of the two things it is for.

## Bounding a scan

A query over a year is a year of reading, so a scan is bounded twice. A budget
in objects and seconds stops it and hands back a cursor, rather than running
until something times out and leaving the caller with nothing. And a query with
no time range walks back to a horizon rather than to the beginning: under a
seven-year retention, reading to the start is not an answer anybody is waiting
for. Both are the deployment's to set.

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
