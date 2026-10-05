# Configuration: observe and query

The keys of the indexer (`audit-observe`) and the query service (`audit-query`), with its grants file.
Shared blocks are in [configuration](configuration.md#shared-blocks).

## Indexer

`audit-observe` follows the archive and writes the index
([0020](../decisions/0020-observe-follows-the-bucket.md),
[0024](../decisions/0024-indexer-and-query-are-separate-processes.md)). It
lists `records/<profile>/<tenant>/` from a cursor kept in Postgres, indexes the
objects older than the settle window, and moves the cursor in the transaction
that writes their rows. It reads the archive and never writes it, and it serves
only `/healthz` and `/readyz`: the query service is `audit-query`, a process of its own under
a role that can only read.

<!-- generated: config-audit-observe -->
| key | type | default | meaning |
|---|---|---|---|
| `listen` | `listen` | `:8080` | the address `/healthz` and `/readyz` are served on. `/healthz` says the process is up (the liveness probe); `/readyz` says it can work now (the readiness probe): the index database answers and the archive's catalogues can be listed. A failing check is named in the 503, never its error |
| `archive` | `archive`, required | | the archive to follow (`bucket`, `prefix`). It has no `lockMode`: this process reads. The catalogues and their extension schemas are read from it too |
| `database` | `database`, required | | the index, as the indexer's **own** role (`audit migrate --observe`): read and write on the index and its cursors, nothing of the deduplication table, not the owner, and not the writer's or the query service's |
| `settle` | duration | `2m` | how far behind now the cursor stays. An object's key is fixed when its put starts and it is visible when it ends, so it must be longer than a put can take and than the clocks of the writers and of this process can disagree. It is the least time between a record's acknowledgement and its appearance in search |
| `interval` | duration | `30s` | the poll: how often a pass runs when nothing woke it. A lost wake-up costs at most this |
| `batch` | integer, at least 1 | `500` | rows written in one transaction; a transaction ends at an object's end |
| `profiles` | list of strings, at least one, unique | every profile the archive has | the profiles to follow. Profiles and tenants are discovered by listing |
| `wake.nats.nats`, `wake.nats.subject` | `nats` (url, `tokenFile`), string | | a subject carrying the bucket's notifications. Their content is never read |
| `wake.sqs` | `sqs` | | a queue of the bucket's notifications that is the indexer's own: each message wakes a pass and is deleted. Credentials are the SDK's ambient ones |
<!-- /generated -->

Exactly one of `wake.nats` and `wake.sqs`, or neither. A wake-up only makes the
next pass come sooner: nothing a pass does depends on it, so a notification
that is lost, repeated or reordered costs latency and nothing else, and the
poll finds what it missed.

An object that does not decode is skipped and counted (`reason=unreadable`):
it will not read later either. One that cannot be fetched, or whose
catalogue cannot be found, stops that tenant's cursor where it is and is tried
again by the next pass (`reason=retry`); the other tenants carry on.
`audit reindex --reset-cursor` makes the indexer read a profile again from the
start, which changes nothing it has already indexed.

## Query service

`audit-query` serves search, facets, get, export, tail and resolve, behind
the grants. Every read it serves is recorded through the writer.

<!-- generated: config-audit-query -->
| key | type | default | meaning |
|---|---|---|---|
| `listen` | `listen` | `:8080` | the address it is served on |
| `grants` | path, required | | the grants file, below |
| `sink` | `sink`, required | | the writer every read is recorded through: its `url`, or the ingest `sqs` queue of a writer that runs elsewhere ([AWS](../how-to/aws-run-readers-in-kubernetes.md)) |
| `require` | `logged`, `queued` or `archived` | none: checks nothing | the weakest durability the writer's acknowledgements may carry. Needs `sink.expect` at least as strong ([durability](configuration-writer.md#durability-require-forward-consume)) |
| `deployment` | path | none | the profile configuration. A grants preset needs it, because it turns roles into the deployment's own profiles |
| `searcher` | `postgres` or `s3scan` | `postgres` | `postgres` is the index; `s3scan` is the archive, within a budget, for a deployment with no database. The scan orders by `occurred_at` only and refuses `recorded_at`, so a deployment on it can search the trail but cannot follow it: there is no live tail ([search](../explanation/search.md#tail)) |
| `database` | `database` | | the index, as the query service's **own** role (`audit migrate --reader`): `usage` on the schema, `select` on the index's tables, not the owner. Tenant row-level security binds only a non-owner. Required unless `searcher` is `s3scan` |
| `archive.bucket`, `archive.prefix` | `bucket`, string | | what `s3scan` reads, and where Get finds a record's object. Required with `s3scan`. Without it Get still answers, with where the copy is and nothing about whether it has been verified (nothing yet sets that: it is for seals). The service's region is `archive.bucket.region` |
| `exports.bucket` | `bucket`, required with `exports` | | a separate bucket with no Object Lock, which clears it. Without `exports` the export operation is refused. It inherits nothing from the archive: name its endpoint, path style and `credentialsSecret` here |
| `exports.expiry` | duration | `168h` | how long an export is kept before the bucket clears it |
| `exports.linkValid` | duration | `1h` | how long a download link works |
| `keys` | `keys` | none | the writer's key provider, which turns resolve on. With provider `none`, or no `keys`, there is nothing to resolve and the RPC is `unimplemented`. `local` reads the writer's key directory, which must be shared; `transit` signs in as the query service's own identity, never the writer's |
<!-- /generated -->

The service's limits (`filter` 4 terms, `sort` 4, `in` 100 values, `limit`
1000) are fixed in the service, not configured; see the
[API reference](api.md).

**The grants file.** `audit-query` reads who may authenticate and what each
caller may see from one file, named by `grants` (in the chart,
`/etc/audit/grants.yaml`, rendered from `query.grants`):

```yaml
issuers:
  - url: https://id.example.com        # exactly as the tokens' iss claim says it
    audience: audit                    # required
  - url: https://customers.example.com
    audience: audit
rules:                                 # first match wins
  - name: auditors
    issuer: https://id.example.com     # required once more than one issuer is trusted
    claim: groups
    value: all:audit:auditor
    grant:
      all_tenants: true
      profiles: [security, operational]
      operations: [search, facets, get, export]
  - name: assessor-2026-q3             # an external assessor sees one period, not the archive
    issuer: https://id.example.com
    claim: groups
    value: all:audit:assessor
    grant:
      all_tenants: true
      profiles: [security]
      operations: [search, get]
      from: 2026-07-01T00:00:00Z         # by when records happened; end exclusive
      until: 2026-10-01T00:00:00Z
  - name: acme-viewers
    issuer: https://customers.example.com
    claim: groups
    value: acme:audit:viewer
    grant:
      tenants: [acme]
      profiles: [security]
      operations: [search, get]
```

Instead of a rule per group, an installation whose groups already say who
may read what names a **grants preset** (a named bundle of grant rules, not a framework profile):

```yaml
presets:
  - name: access-roster
    issuer: https://id.example.com       # required once more than one issuer is trusted
    claim: groups                        # the default
```

The `access-roster` preset reads groups named `<scope>:audit:<role>`, the
estate's grant grammar from sluis (`access-roster` is the identifier the code gives it,
from sluis's former name, and stays until a code change renames it). The scope is `all` or an audit tenant id, byte for
byte; an environment is never in the name, because each deployment's query
service requires its own token audience and the issuer decides who may hold
which. A role grants operations over the profiles built from certain framework profiles,
so the deployment's own profile names need no mention — which is why a grants preset
needs `deployment` in the query service's configuration:

| role | profiles built from | operations | `all` allowed |
|---|---|---|---|
| `viewer` | `history` | search, facets, get | no: `all:audit:viewer` grants nothing |
| `security` | `security`, `dora`, `pci-dss`, `nen-7513` | search, facets, get, tail, export | yes |
| `auditor` | every profile built from no `billing-*` framework profile | search, facets, get, export | yes |
| `billing` | `billing-*` | search, facets, get, export | yes |
| `evidence` | `evidence-etsi` | search, get, export | yes |

`resolve` comes from no group name; it is an explicit rule naming the person.
There is no assessor role, because a name carries no dates; a time-boxed grant
is an explicit rule with a window.

**Every grant a caller holds counts** — each matching rule and each audit
group — and which apply is decided per request: on the profile asked for, the
tenants of the grants covering it are unioned, and the record of the read
names all of them (`acme:audit:viewer,all:audit:security`). A union never
crosses profiles, so a viewer of one tenant's history plus a security role over
every tenant does not become every tenant's history. A time window does not
union: an unbounded grant on the profile makes the answer unbounded, and two
different windows on one profile are refused.

It refuses to start when:

- the file names no issuer, because then nobody could ever sign in;
- an issuer has no audience. An audit log must not accept a token minted for
  another service, because any workload holding that token could replay it here;
- more than one issuer is trusted and a rule names none. Every issuer can assert
  any claim, so a rule matching a group from anyone gives operator access to
  whoever administers the least-trusted issuer;
- a rule names an issuer that is not listed, or an operation that does not
  exist;
- a grants preset is named and the configuration has no `deployment`, or one that does not exist.

A bearer token in `Authorization` is accepted, and so is the access token the
fleet gateway forwards. Verification is
[gateway-auth](https://github.com/truvity/gateway-auth)'s, one verifier per
issuer: discovery, a key set refreshed in the background, signature, issuer,
audience and expiry. That library allows no clock skew.

