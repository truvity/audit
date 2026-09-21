# 0006. Search contract: DNF typed predicates, cursor page object, tail cursor

- Status: accepted
- Date: 2026-09-17

## Context

Product audit APIs surveyed (see the product survey in this repository's history)
converge on a small typed filter grammar with cursor pagination, and the
ones that grew a free query language regret its cost. A faceted-search
design already used by a public API of ours expresses filters as
disjunctive normal form over typed predicates with an operator table per
field type. The Zalando RESTful API guidelines give the page object and
the cursor rules.

## Decision

`QueryService.Search` (`proto/audit/v1/query.proto`) takes a `profile`, a
`filter` of up to four OR-joined conjunctions of typed predicates
discriminated on `operator`, a `sort` of up to four field-and-order pairs
limited to indexed columns, a `limit` that is a suggestion, and an opaque
`cursor`. The operator table per type is fixed: strings take equal,
not-equal, in, not-in, prefix, null checks; times take between and the four
comparisons; integers both; targets take in and not-in; extension
properties the catalogue marks filterable take path predicates.

The response is the page object: `items`, the normalised `query`, and
cursors `self`, `first`, `prev`, `next`. `last` is omitted because counting
is expensive. Cursors are opaque, encode the keyset boundary, the direction
and a hash of the normalised query, and are rejected with a different
query. `next` never disappears: on the last page it yields new records
later, advancing on **recorded** order so late events are never missed.
Default sort is `occurred_at` descending then `id`.

`Facets` takes the same filter plus facetable fields and reads from the
counts table. `Get` fetches one record by id with its provenance.
`Export` is an asynchronous job returning a short-lived signed URL. `q` is
reserved for free text. JSON is snake_case, timestamps RFC 3339.

## Consequences

- A second searcher backend can be honest because the grammar is closed.
- Every search, facet read, get and export emits a `log_access` record with
  the granting rule.
- A changed authorization grant invalidates old cursors by construction.
- Free text is a capability a searcher may declare later without a
  breaking change.

## Alternatives considered

- **Lucene or SQL passthrough.** Powerful and unportable; every backend
  becomes a leaky subset.
- **Offset pagination.** Unstable under inserts and expensive to count.
- **Tail on occurrence time.** Misses late events; that is why the tail
  advances on recorded order.
