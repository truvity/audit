# 0024. The indexer and the query service are separate processes, under separate database roles

- Status: accepted
- Date: 2026-10-03

## Context

[0016](0016-three-parts-installed-independently.md) names three parts and calls
the third one **observe**: it follows the archive, projects it into a search
index and serves queries. [0020](0020-observe-follows-the-bucket.md) says how it
follows: by a cursor over the bucket's listing, behind a settle window.

Observe is two jobs with opposite database rights. Following the bucket
**writes** the index and its cursors. Serving queries **reads** the index on a
caller's behalf, as a role that can write nothing and that row-level security
binds to the tenants of the caller's grant, and it parses what callers send.

Before this change the indexer ran inside the writer, and the writer's database
role was the owner of every table: the deduplication table the write path
needs, the catalogue registry, and the index it had no other reason to touch.

## Decision

**`audit-observe` is the indexer and nothing else.** It reads the archive,
writes the index and its cursors, and serves a health check. It has no
searcher, takes no caller, and holds no grants file. The query service stays
`audit-query`, which reads the index and the archive and records each read
through the writer. Both are observe in the sense of 0016: a deployment that
wants search runs both, and each is a Deployment of its own with its own
ServiceAccount, configuration and database role.

One process would have been simpler to deploy, and was rejected: it would hold
the index's write credential in the process that faces callers, so that a flaw
in a query parser were a flaw in a writer of the index. The two are the same
image family and the same chart, and the cost of the split is one more
Deployment.

**Three roles, none of them the owner.** The tables belong to the role the
migration job connects as, which nothing else uses. `audit migrate` grants each
part's role what the part needs and takes back the rest
(`index/postgres.GrantRoles`):

| role | holds | cannot |
|---|---|---|
| writer | the deduplication table, the catalogue registry, the key directory | read or write the index or its cursors |
| observe | the index tables and `index_cursor`, and one function that creates a month's partition | touch the deduplication table or the registry; delete index rows |
| query | `select` on the index tables, bound by row-level security | write anything; read cursors, deduplication or the registry |
| purge | delete from the index and the deduplication table | add a row |

A month's partition is created by observe as it indexes. Creating a partition
takes the owner, so the migration defines `audit_ensure_month(date)`, a
`SECURITY DEFINER` function with a fixed search path that creates the three
partitions of a month and nothing else, and `execute` on it is granted to
observe's role alone.

The chart refuses an indexer that runs as the writer's or the query service's
ServiceAccount, or connects as the writer's, the query service's or the
migration's database role, and a writer that connects as the migration's.

**The writer stops indexing.** `Roller` writes the object and nothing else.
The writer no longer has an index to ask where an earlier record is, so an
addendum locates it by scanning the archive within the scanner's budget, as it
already did with no database.

**Observe reads the catalogues from the bucket.** A record names the catalogue
version it was written against, and which of its properties are indexed is the
catalogue's answer. The writer puts `catalogue/<app>/<version>` and the
extension schemas beside it before the first record that names them
([0018](0018-v1-bucket-layout.md)), so the indexer needs no registry, no
database grant on the writer's tables and no catalogue files of its own. A
record whose catalogue it cannot find stops that tenant's cursor and is
counted; it is never indexed without its data columns, which a later run could
not repair.

## Consequences

- A compromised writer cannot change what search answers with, a compromised
  query service cannot write the index, and a compromised indexer cannot mark a
  record as already written.
- The freshness floor is the settle window, as in 0020. The index lag metric is
  `audit.observe.index.lag`, from the object's put to its rows being indexed,
  and its alert threshold is set above the window.
- An installation that runs the query service and not the indexer has an empty
  index. The chart does not refuse it, because the indexer may be another
  release's.
- `schema/` is not part of the bucket contract, and the indexer reads it for
  the extension schemas of a catalogue. A different implementation of observe
  that keeps to the contract alone has to get them from the catalogue's
  registrar.

## Alternatives considered

- **One process, two pools.** The simplest chart. It leaves the write
  credential next to the caller-facing code, which is what the roles are for.
- **Observe reads the registry table.** It would give the indexer a grant on a
  table the writer owns, and miss the catalogues a writer was given as files.
- **Keep the writer indexing as well, for latency.** Two writers of one index,
  one of them on the write path, which is the coupling 0016 removes.
