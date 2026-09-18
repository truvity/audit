# Reading the trail

How people and programs read the audit trail: who may see what, the API with
examples, the Go and TypeScript clients, and how an auditor checks the archive
without trusting anyone. The Go code is
[`examples/read`](../../examples/read/main.go), compiled on every run of the
gate.

```mermaid
flowchart LR
  P(["person, tool<br/>or auditor"]) -- "bearer token" --> Q["audit-query"]
  Q -- "1. verify token<br/>(trusted issuers)" --> I[("your identity<br/>provider")]
  Q -- "2. grants from the<br/>grants file" --> Q
  Q -- "3. search, narrowed<br/>to the grant" --> PG[("Postgres index")]
  Q -- "4. every read<br/>is recorded" --> W["audit-writer"]
  Q -- "provenance,<br/>resolve" --> S3[("archive")]
  A(["auditor"]) -- "audit verify<br/>read-only + public key" --> S3
```

## Access

The query service trusts only the issuers you name, and grants only what its
**grants file** (`--grants`) says:

```yaml
issuers:
  - url: https://id.example.com          # exactly as the tokens' iss claim says it
    audience: audit-prod                 # required: this deployment's own audience

# Groups named <scope>:audit:<role> grant themselves — see the table below.
presets:
  - name: access-roster
    issuer: https://id.example.com

# Anything a group name cannot say is an explicit rule.
rules:
  - name: assessor-2026-q3               # an external assessor: one period only
    issuer: https://id.example.com
    claim: groups
    value: all:audit:assessor
    grant:
      all_tenants: true
      profiles: [security]
      operations: [search, get]
      from: 2026-07-01T00:00:00Z
      until: 2026-10-01T00:00:00Z
  - name: dpo-resolve                    # undoing pseudonyms: one named person
    issuer: https://id.example.com
    claim: sub
    value: dpo@example.com
    grant:
      all_tenants: true
      profiles: [security]
      operations: [resolve]
```

With the `access-roster` preset, a group's name is the grant:

| group | may |
|---|---|
| `<tenant>:audit:viewer` | search, facets, get on the history profile of that tenant |
| `all:audit:security` (or `<tenant>:…`) | search, facets, get, tail, export on the security profiles |
| `all:audit:auditor` | search, facets, get, export on every profile but billing |
| `all:audit:billing` | search, facets, get, export on the billing profiles |
| `all:audit:evidence` | search, get, export on the evidence profile |

`all:audit:viewer` grants nothing, and **no group name grants `resolve`**. A
caller holding several groups gets their union on each profile, never across
profiles. The service refuses to start if the file names no issuer, an issuer
has no audience, or a rule could be satisfied by more than one issuer. The full
rules are in [the configuration reference](../reference/configuration.md#query-service).

Every read — search, facets, get, export, resolve — is itself recorded in the
trail, naming the caller and the rule that allowed it.

## The API

Connect RPC, so every method is a `POST` with a JSON body (or binary protobuf).
JSON field names are snake_case. The contract is
[`query.proto`](../../proto/audit/v1/query.proto); the reference, with the
operators and limits, is [API](../reference/api.md).

**Search**, newest first, one page at a time:

```sh
curl -s https://audit-query.example.com/audit.v1.QueryService/Search \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "profile": "security",
    "filter": [{
      "occurred_at": {"between": {"from": "2026-09-01T00:00:00Z", "to": "2026-09-18T00:00:00Z"}},
      "action":      {"prefix": "shop.order."},
      "outcome":     {"in": {"values": ["failure", "denied"]}}
    }],
    "limit": 100
  }'
```

The response has `items`, the normalised `query`, and `cursors`. Send
`cursors.next` back as `"cursor"` with the same query for the next page. The
last page's `next` never disappears: asked again later, it returns what was
recorded since, which is how a **tail** works. A search that matches nothing
has no `items` key at all.

Filters are up to four OR-joined conjunctions of typed predicates (`equal`,
`in`, `prefix`, time ranges, predicates on the extension properties a catalogue
marks filterable). There is no free text and no regular expression: both are
unbounded work on a table that only grows.

**Facets**, for navigation:

```sh
curl -s …/audit.v1.QueryService/Facets -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"profile": "security", "fields": ["action", "outcome"], "limit_per_field": 10}'
```

**Get** one record, with where its copy is and whether the digest chain vouches
for it:

```sh
curl -s …/audit.v1.QueryService/Get -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"profile": "security", "id": "0199b100-0000-7000-8000-00000000001a"}'
```

`provenance.object_key` and `line` locate the copy in the archive;
`digest_id` names the digest that accounts for it; `verified_at` is when that
digest was last verified clean. An empty `verified_at` means not verified yet —
the current hour is never sealed — or the last check found a problem.

**Export** starts a job, **GetExport** polls it and returns a short-lived
download link:

```sh
curl -s …/audit.v1.QueryService/Export -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"profile": "security", "filter": [{"action": {"prefix": "shop."}}], "format": "FORMAT_NDJSON"}'
curl -s …/audit.v1.QueryService/GetExport -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"job_id": "…"}'
```

**Resolve** maps a pseudonym back to the person, for the cases the law
requires. It needs the `resolve` operation, is recorded before it answers, and
is impossible once the tenant's key has been destroyed:

```sh
curl -s …/audit.v1.QueryService/Resolve -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"profile": "security", "tenant_id": "acme", "pseudonym": "ps_…"}'
```

Errors are Connect codes a client can act on: `permission_denied`,
`invalid_argument`, `resource_exhausted`, `not_found` (also for a record outside
your grant), `failed_precondition` (resolving an erased pseudonym),
`unimplemented`, and `unavailable` — retry that one.

## From Go

```go
client := auditv1connect.NewQueryServiceClient(httpClientWithYourBearer, "https://audit-query.example.com")
page, err := client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{Profile: "security", Limit: 100}))
```

[`examples/read`](../../examples/read/main.go) builds a filtered search, pages
it to the end, and reads one record with its provenance.

## From TypeScript

The generated contract is in [`ts/src/gen`](../../ts/src/gen) — protobuf-es v2
types and service descriptors — for `@connectrpc/connect` v2. No npm package is
published yet.

```ts
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { QueryService } from "./gen/audit/v1/query_pb";

const audit = createClient(QueryService, createConnectTransport({
  baseUrl: "https://audit-query.example.com",
  jsonOptions: { useProtoFieldName: true },
  interceptors: [(next) => async (req) => {
    req.header.set("Authorization", `Bearer ${await token()}`);
    return next(req);
  }],
}));

const page = await audit.search({ profile: "security", limit: 100 });
for (const r of page.items) console.log(r.occurredAt, r.action, r.actor?.id);
```

## A viewer

There is **no UI yet**. The design is an embeddable React package and a
standalone console on the same API ([viewer](../design/viewer.md)); until it
exists, the API above and the examples are how to look.

## For an auditor

An auditor does not have to trust the operator, the database or this service.
With read-only access to the archive and the public half of the signing key:

```sh
audit key public --kms-key alias/audit-digest > public.pem   # or --transit-key, or --key
audit verify --profile security --from 2026-09-01 --to 2026-09-18 \
  --bucket example-audit --public-key public.pem
```

It walks the signed chain and reports any object changed, taken away or slipped
in beside it, any gap in the chain, and any lock shorter than the profile
requires. Exit status zero means nothing was found. See
[verification](../operations/verify.md).
