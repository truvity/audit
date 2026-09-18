# API reference

Contracts: [sink.proto](../../proto/audit/v1/sink.proto),
[registry.proto](../../proto/audit/v1/registry.proto),
[query.proto](../../proto/audit/v1/query.proto). Served over ConnectRPC;
JSON is snake_case, timestamps RFC 3339. As in all proto3 JSON, a field
at its zero value is omitted, so a search that matches nothing returns no
`items` key at all rather than an empty list; read absence as empty.

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

`first` is a cursor like the others and means this same question from the
beginning, so a client holds one kind of cursor rather than two. There is no
`last`: counting what is behind a query is the expense keyset paging exists to
avoid.

Operators per type. **Built** is what the service compiles today; the rest are
in the proto and are refused, because a reference that promises an operator the
service ignores is worse than one that admits the gap.

| type | operators | built |
|---|---|---|
| string | equal, not_equal, in, not_in, prefix | yes |
| string | is_null, is_not_null | no |
| time | between, greater_than, greater_than_or_equal, less_than, less_than_or_equal | yes, each as a half-open range |
| integer | equal | yes |
| integer | not_equal, in, not_in, between, comparisons | no |
| targets | in, not_in (empty id matches the type) | yes |
| attributes | equal, not_equal, key_is_null, key_is_not_null | no |
| data path | string, integer or time predicate on a filterable property | yes |

A predicate on `id` takes whole identifiers: record ids are UUIDs, so a value
that is not one, or a `prefix`, is `invalid_argument`. `Get` of an id that is
not a UUID is `not_found`.

`Facets` counts values of fields under the same filter:

```json
{"profile": "security", "filter": [ … ], "fields": ["action", "outcome"], "limit_per_field": 10}
→ {"facets": [{"field": "action", "values": [{"value": "shop.order.placed", "count": "42"}]}]}
```

`Get` returns one record and where its copy is, with the digest that covers it
and when that was last verified clean:

```json
{"profile": "security", "id": "0199b100-…"}
→ {"record": { … },
   "provenance": {"object_key": "profile=security/tenant=acme/…ndjson.zst", "line": "3",
                  "digest_id": "digest/profile=security/year=2026/…/hour=10.json",
                  "verified_at": "2026-09-18T03:23:11Z"}}
```

`Export` starts a job over a filter; `GetExport` polls it and, when ready,
returns a signed link that expires:

```json
{"profile": "security", "filter": [ … ], "format": "FORMAT_NDJSON"}
→ {"job_id": "x-…", "state": "EXPORT_STATE_PENDING"}

{"job_id": "x-…"}
→ {"state": "EXPORT_STATE_READY", "url": "https://…", "expires_at": "…", "records": "1204"}
```

64-bit integers (`count`, `line`, `records`) are strings in JSON, as proto3
JSON maps them. Limits, enforced by the
service rather than by whichever searcher is configured: `filter` 4 terms,
`sort` 4, `in` 100 values, `limit` ceiling 1000, and an export size cap. A
grant's window narrows a wider request rather than refusing it.

## Resolve

`audit.v1.QueryService/Resolve` maps a pseudonym back to the identity behind
it: `{"profile": "security", "tenant_id": "acme", "pseudonym": "ps_…"}` →
`{"identifier": "…"}`. Pseudonyms differ per profile, so name the profile whose
copy carried it.

It needs the `resolve` operation on that profile, which no read grant implies
and no group name grants — only an explicit rule. The tenant must be one the
grant covers. The resolution is recorded as `audit.pseudonym.resolved`, and
the record confirmed, **before** the identity is returned; if the trail cannot
take it, nothing is resolved. The record names the pseudonym and the rule,
never the identity. The service offers resolve only when it is given the keys
(`audit-query --key-root --key-dir --bucket`, or `--key-provider transit
--transit-address … --bucket` with a token that may decrypt); otherwise
`unimplemented`.

Only actor and subject pseudonyms resolve. The writer keeps each one's
identifier sealed under the same tenant's key (`identity/` in the archive), so
destroying the key — erasure — makes resolving impossible: `failed_precondition`.
Values hashed by `x-audit-sensitive: hmac` are findable and never readable, and
have no way back.

## Errors

Connect codes:

| code | when |
|---|---|
| `invalid_argument` | a malformed filter, or a cursor from a different query |
| `permission_denied` | the grant excludes the profile, the operation or the tenant |
| `resource_exhausted` | a published limit exceeded, or an export over its cap |
| `failed_precondition` | resolve of a pseudonym whose tenant key was destroyed — erasure did what it is for; retrying never helps |
| `not_found` | no such record — **also** what a record outside the grant returns, because "no such record" and "a record you may not read" are the same answer to somebody who should not know it exists |
| `unimplemented` | something this deployment does not offer, such as export with no bucket configured |
| `unavailable` | the searcher is down |

`unavailable` is the default for anything unrecognised, deliberately. Calling a
searcher outage `invalid_argument` would tell a well-behaved client never to try
again, and turn a database restart into an outage that outlives it.
