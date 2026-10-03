# 0025. A source keeps its former names as aliases

- Status: accepted
- Date: 2026-10-03

## Context

A source is the identity of a producer. A record names it (`source`), every
action is under its namespace (`source.resource.verb`), and the archive keeps
its catalogue at `catalogue/<source>/<version>`. Nothing in the system could
say that a source had been **renamed**: two names meant two producers. A
producer that changes its name (`access-roster` becoming `sluis`) would
otherwise leave a trail that a query over time cannot follow, because the old
records say one name and the new ones another, and a filter finds only one of
them.

Records are immutable and locked, so the old ones cannot be rewritten, and
should not be: what the archive holds is what was written.

## Decision

**A catalogue declares the names its source used to have.**

```yaml
source: sluis
aliases: [access-roster]
version: "2.0.0"
```

`aliases` is a list of source names (the same grammar as `source`), none of
them the source itself, none repeated. The renamed source's actions are under
the new namespace (`sluis.grant.issued`); an action under an alias
(`access-roster.grant.issued`) is the same action.

- **The writer accepts either name.** A record naming an alias resolves to the
  catalogue that lists it, for the version it names, and is validated against
  it: the source may be the alias, and the action may be under the alias's
  namespace. The record is written as it was emitted, not rewritten. A name
  that is neither the source nor one of its aliases is refused as before.
- **The index holds the current name.** Observe indexes a record under its
  catalogue's source and the action under its namespace, whatever the record
  says, so one source is one value in search and in facet counts, and the
  Audit page shows the canonical name. Rebuilding the index (`audit reindex`)
  does the same.
- **A filter by either name finds both.** The query service rewrites an alias
  in a `source` or `action` predicate (`equal`, `not_equal`, `in`, `not_in`,
  and a `prefix` that includes the dot, `access-roster.`) to the current name.
  It learns the aliases from the catalogues in the archive when the service is
  given the archive and searches the index. Get returns the record as it was
  written, with its original name.
- **The bucket keeps the catalogue under each name.** The writer puts the same
  document at `catalogue/<source>/<version>` and at `catalogue/<alias>/<version>`
  (and its schemas under `schema/<alias>/<version>/`), so a record's own source
  and version find what describes it where the contract says. The put is the
  same conditional put: a version a former name already holds with other bytes
  is a conflict, and the writer refuses to run. **An alias does not make a
  version new**: the renamed catalogue takes a version its former name never
  used. A registry (the writer's database) holds the same entries under the
  aliases, with the same refusal.

A source's producers need not change on the day of the rename: the old name
keeps being accepted for as long as the catalogue lists it. A producer moves
to the new name when it releases; its catalogue's version says from where.

## Alternatives not taken

- **Rewrite the old records.** The archive is the record and is locked; a
  rewritten record is a different record, with a different hash.
- **Resolve at read time only, and index the name written.** Search, facets
  and counts would then show two producers for one, and every consumer would
  repeat the resolution.
- **Aliases in the query service's configuration.** A second place to say what
  the catalogue already says, which drifts. The service reads the catalogues.
- **A separate registry of renames.** One more thing to deploy, to say what a
  catalogue field says beside the catalogue it belongs to.

## Consequences

- A filter by an alias on the `s3scan` searcher, which reads the records as
  written and indexes nothing, matches the name each was written under. The
  rewrite is applied for the Postgres index only.
- A query service without the archive does not learn the aliases; it logs
  that, and a filter by the former name finds only what is indexed under it,
  which after a rebuild is nothing.
- The index rows of records written before the alias was declared keep their
  old name until they are re-indexed (`audit reindex`).
- The audit **source name** is not the grants preset's name nor any
  deployment's configuration key: those are the deployment's own.
