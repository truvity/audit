# API reference

Contracts: [sink.proto](../../proto/audit/v1/sink.proto),
[registry.proto](../../proto/audit/v1/registry.proto),
[query.proto](../../proto/audit/v1/query.proto). Served over ConnectRPC;
JSON is snake_case, timestamps RFC 3339.

## Write

`audit.v1.SinkService/Write` — batch of records with a delivery hint.
Reachable by workloads and adapters; never by browsers.

## Registry

`audit.v1.RegistryService/RegisterCatalogue`, `GetCatalogue`,
`ListCatalogues`.

## Query

`audit.v1.QueryService/Search`:

```json
{
  "profile": "security",
  "filter": [
    {
      "occurred_at": {"between": {"from": "2026-09-01T00:00:00Z", "to": "2026-09-17T00:00:00Z"}},
      "action":      {"prefix": "wallet.credential."},
      "outcome":     {"in": {"values": ["failure", "denied"]}},
      "targets":     {"in": {"values": [{"type": "credential"}]}}
    },
    { "actor_id": {"equal": "…"} }
  ],
  "sort": [{"field": "FIELD_OCCURRED_AT", "order": "ORDER_DESC"}, {"field": "FIELD_ID", "order": "ORDER_ASC"}],
  "limit": 100
}
```

Response:

```json
{
  "items": [ … ],
  "query": { "profile": "security", "filter": [ … ], "sort": [ … ], "limit": 100 },
  "cursors": { "self": "…", "first": "…", "prev": "…", "next": "…" }
}
```

Operators per type:

| type | operators |
|---|---|
| string | equal, not_equal, in, not_in, prefix, is_null, is_not_null |
| time | between, greater_than, greater_than_or_equal, less_than, less_than_or_equal |
| integer | equal, not_equal, in, not_in, between, and the four comparisons |
| targets | in, not_in (empty id matches the type) |
| attributes | equal, not_equal, key_is_null, key_is_not_null |
| data path | string, integer or time predicate on a filterable property |

`Facets`, `Get`, `Export`, `GetExport` as in the proto. Limits: `filter`
4 terms, `sort` 4, `in` 100 values, `limit` ceiling 1000, one time-range
predicate per conjunction, export size cap and maximum range per grant.

## Errors

Connect codes: `invalid_argument` for a malformed filter or a cursor from a
different query, `permission_denied` when the grant excludes the profile or
tenant, `resource_exhausted` for range or size caps, `unavailable` when the
searcher is down.
