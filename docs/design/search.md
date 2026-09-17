# Search

## Interfaces

```go
type Indexer interface {
    Index(ctx, profile string, rows []Row) error   // idempotent by (profile, id)
    Count(ctx, profile string, deltas []FacetDelta) error
    Purge(ctx, profile string, before time.Time) error
}

type Searcher interface {
    Search(ctx, Query) (Page, error)
    Facets(ctx, Query, fields []string) (Counts, error)
    Get(ctx, profile, id string) (Record, Provenance, error)
    Capabilities() Caps                            // free text? regex? facets?
}
```

Three searchers ship: memory (tests), object-storage scan (no index; small
deployments), Postgres (default). The interface stays honest because two
production implementations exist from the start.

## Postgres layout

- `events_core`: `(profile, id, tenant_id, occurred_at, recorded_at,
  source, action, operation, outcome, target_types[], target_ids[],
  object_key, line)`. Monthly partitions. Kept as long as the prefix.
- `events_context`: `(profile, id, actor_kind, actor_id, subject_id,
  client_address, request_id, trace_id, observer_id)`. Purged on the
  security schedule.
- `events_data`: `(profile, id, path, value_text, value_int, value_time)`
  for extension properties marked filterable.
- `facet_counts`: `(profile, tenant_id, hour, field, value, count)`.
- Row-level security by `tenant_id` from a per-request setting;
  tenant-leading composite indexes; BRIN on time.

## Query model

Closed by design (decision 0006). The Authorizer's grant is a filter term
AND-ed into every query. Cursors carry the keyset boundary, the direction
and a hash of the normalised query.

## Tail

`next` on the last page stays valid and advances on `recorded_at` plus the
writer sequence, so late events are never missed by a poller.

## Reindex

`audit reindex --profile <p> --from <day> --to <day>` replays prefixes into
the index and counts. Safe to run at any time.
