# Configuration reference

The Go emitter's options, and the chart's values for each component. Every
name here exists in the code or in `charts/audit/values.yaml`, and the chart's
file has a comment on each value. Where something is designed and not built,
it says so.

One installation serves one application, in that application's namespace,
rendered by the application's own chart with this one as a dependency
([0011](../decisions/0011-one-installation-per-service-or-product.md)).
There is no registry service and nothing configures one.

## Emitter library

`emit.New(emit.Options{…})`:

| option | meaning |
|---|---|
| `Source`, `Catalogue` | the source this emitter speaks for, and its loaded catalogue; a record of an action the catalogue does not declare is refused |
| `Sink` | where records go: `sink.NewClient(httpClient, receiverURL)` for the receiver over Connect, with `auth.TokenFile` for the workload token. The application's sink is the receiver in its own namespace; the JetStream hop, in stream mode, is the receiver's, not the application's |
| `Timeout` | how long a `block` write may take. Default 10s |
| `Queue`, `Batch`, `Flush` | the `async` queue: how many records may wait (1024), how many are sent together (100), and how often (one second). A full queue drops the oldest, counts it and calls `OnDropped` |
| `Bounds` | size limits; default `record.Default` |
| `Version`, `Instance` | this process on every record; the writer replaces the observer's identity with the one it verified |
| `Hooks` | `OnDropped`, `OnFailed`, `OnWritten`, `OnRefused`: where a deployment counts and alerts |

`emit.Middleware(trustedHops)` records each request's client address, user
agent, request and trace ids on every record made while serving it.
`trustedHops` is how many proxies of your own sit in front: 0 records the
connection's peer, and getting it wrong records a load balancer as the actor's
address. `emit.Register(ctx, emit.Registration{…})` registers the catalogue
with the **receiver** at start-up — the same address the sink writes to,
because the receiver serves `RegistryService`.

`Options.Queue`, `Options.Batch` and `Options.Flush` size the async queue, and
`Options.Retry` is how long the emitter waits before trying a batch the sink
could not take, doubling up to a minute. There is no outbox option, and no
file: there are two deliveries
([0012](../decisions/0012-two-deliveries-and-a-durable-ack.md)).

Two metrics are worth alerting on, and they are the pair that says whether
anything was lost:

| metric | means |
|---|---|
| `audit.emit.queue.pending` | how many records are waiting to be acknowledged, and so what this process would lose if it stopped now. A number that only climbs is a receiver that has stopped acknowledging; drops follow. Published by `emit.InstrumentQueue` |
| `audit.emit.records.dropped` | records the queue overflowed and gave up on. Every one of them is also written to the application's log by the emitter (`Options.Logger`). This is the incident; the one above is the alert |

## Receiver and writer

One binary, `audit-writer`, in two roles, chosen with `--mode` (env
`AUDIT_MODE`). With `--mode writer`, the default, it serves the sink, writes
the archive and consumes a stream when `--stream-url` is set. With
`--mode receiver` it serves the sink and publishes to the stream, and takes
no `--bucket` and no key provider: a receiver holding either would be a
writer. In direct mode one process in `writer` mode is both. In stream
mode it publishes to JetStream and acknowledges the replicated publish, and
the same image runs again in consumer mode as the writer. In both it serves
`RegistryService`, so the application registers its catalogue with the
address it writes to.

These are the chart's values (`charts/audit/values.yaml`), which are the
binary's flags with dots.

| setting | meaning |
|---|---|
| `mode` | `direct` (one process: the front door and the write path) or `stream` (a receiver in front, `writer.consumers` writers behind). It renders the binaries' `--mode` |
| `bucket`, `prefix`, `region`, `kmsKey` | the archive. `prefix` is required in a bucket shared with other applications: it is what keeps two installations apart |
| `lockMode` | the Object Lock mode every object is written in: `compliance` (the default), `governance`, or `none` for a store without Object Lock or profiles that demand none ([0014](../decisions/0014-lock-modes-and-store-tiers.md)). It renders `AUDIT_LOCK_MODE` for every component that writes the archive, and each refuses to start when a profile demands a stricter mode |
| `governance` | deprecated: the same as `lockMode: governance` |
| `endpoint`, `pathStyle`, `existingSecret` | an S3-compatible store that is not AWS: its URL (`--endpoint`, `AUDIT_S3_ENDPOINT`; the SDK's `AWS_ENDPOINT_URL_S3` works too), path-style addressing (`--path-style`, `AUDIT_S3_PATH_STYLE`) for a certificate that does not cover a bucket subdomain, and a Secret with `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` given to every archive container through `envFrom`. All empty by default, which is AWS with the pod's own identity |
| `query.exports.endpoint`, `.pathStyle`, `.existingSecret` | the same three for the exports bucket, when it is on a store of its own; empty inherits the archive's endpoint and path style. The Secret's keys reach the one query process as `AUDIT_EXPORTS_ACCESS_KEY_ID` and `AUDIT_EXPORTS_SECRET_ACCESS_KEY`, a second identity beside the archive's |
| `profiles` | composition of presets and prefixes, the document `audit-writer --deployment` reads |
| `externalIdentifiersAreOpaque` | the deployment declares that the identifiers it receives for external people mean nothing outside its own database, which relaxes a profile's `external: pseudonym` to `clear` ([presets](presets.md#what-a-deployment-can-relax)) |
| `replicas` | receiver pods |
| `writer.consumers` | writer pods consuming the stream, in stream mode. Default 3 |
| `stream.url`, `stream.name`, `stream.consumer` | JetStream, in stream mode. The chart refuses `mode: stream` without a URL |
| `stream.batch` | how many records are taken at once. Default 100 |
| `stream.ackWait` | how long the stream waits for a batch to be taken before offering it again. Default 2m, and it must exceed `roll.interval` plus the longest a put can take: records are acknowledged only once they are in the archive, and a stream that gives up waiting sooner offers them to a second writer. The chart refuses to render otherwise |
| `stream.token.enabled`, `.audience`, `.expirationSeconds` | how the receiver and the writers identify themselves to a broker that verifies who connects: a projected service-account token of that audience (`nats`) and lifetime (3600), presented as the NATS token and read afresh on every connect. It renders the binary's `--stream-token-file`. Off by default: the pods then connect with no credentials ([stream](../deployment/stream.md#authenticating-to-the-stream)) |
| `roll.interval`, `roll.maxRecords` | the roll: how much a writer gathers from the stream before it writes, and so how many objects a day of records becomes. Whichever of the two is reached first, or the roller's byte limit. In direct mode there is no stream to gather from, and `interval` is only how long an object may stay open inside one write |
| `database.url` or `database.existingSecret` | the index and the shared deduplication table, one database in the application's Postgres. Without it the writer indexes nothing and deduplicates in process |
| `database.migrate` | apply the schema from a pre-upgrade hook Job. The writer refuses to start on a version it does not know and never migrates itself |
| `keys.provider` | `none` (the default), `local` (a root and a directory) or `transit` (OpenBAO — see [OpenBAO keys](../operations/openbao-keys.md)). With `none` there are no pseudonyms, no key directory, no login to a secret manager and no resolve ([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)) |
| `openbao.address`, `.mount`, `.namespace` | the OpenBAO the transit key provider and the transit digest signer reach: the server, where transit is mounted (`transit`), and the namespace (empty is root) |
| `openbao.auth.mount`, `.audience`, `.expirationSeconds` | the JWT auth mount each component signs in on with its projected service-account token (e.g. `jwt-devel`), the token's audience (`openbao`) and lifetime (600) |
| `keys.transit.prefix` | what every key name starts with (`audit`) |
| `keys.transit.role`, or `.token.existingSecret`, or `.tokenFile` | how the writer signs in, exactly one: a role on `openbao.auth.mount` (nothing stored), a token Secret, or a token file |
| `trust.configMap`, `trust.key` | a CA bundle trusted beside the system roots, e.g. trust-manager's for a private chain; mounted by every pod that reaches OpenBAO or Postgres (`PGSSLROOTCERT`) |
| `keys.local.existingSecret` | the 32-byte root the data keys are wrapped under |
| `keys.local.persistence` | where the wrapped keys live. They are random, not derived, so this is the only copy: back it up, and use ReadWriteMany for more than one replica |
| `catalogues` | catalogue documents mounted at start-up, by name. An application that registers its own over `RegisterCatalogue` needs none |
| `extensions.billing.enabled`, `extensions.quotas.enabled` | the two projections, both off. The toggles are here so that a deployment's values need not change when the work behind them lands; today each renders nothing |

The receiver verifies who writes with `--workloads`/`AUDIT_WORKLOADS`, a file
naming the issuers trusted to name a workload (see "Workload identity"
below), and stamps the caller's service account as each record's observer.
Without it, it refuses to start unless given `--anonymous-writes`, which is
for a trial install. Records that arrive over the stream carry no verified
observer: the stream's own authentication is what admits a publisher there.

The built `audit-writer` takes these as flags or environment variables:
`--database`/`AUDIT_DATABASE` is the index, `--replicas`/`AUDIT_REPLICAS` is how
many writers share the stream, `--stream-token-file`/`AUDIT_STREAM_TOKEN_FILE`
is the token both ends present to a broker that verifies who connects. A
writer whose database is at a schema version this build does not know refuses
to start; run `audit migrate --database <url>` first, from one place.

## Query service

In the chart, under `query` (`query.enabled`):

| value | meaning |
|---|---|
| `query.searcher` | `postgres` (the index) or `s3scan` (the archive, within a budget; no database). The scan orders by `occurred_at` only and refuses `recorded_at`, so a deployment on it can search the trail but cannot follow it: there is no live tail ([search](../design/search.md#tail)) |
| `query.database.existingSecret` or `.url` | the query service's **own** role: `usage` on the schema, `select` on its tables, not the owner. Tenant row-level security binds only a non-owner, so the chart refuses the writer's credentials here |
| `query.database.role` | that role's name. When set, the migration job grants it usage and select, now and on tables created later (`audit migrate --reader`), and nothing else; the role must already exist |
| `query.grants` | the grants file below, inline |
| `query.exports.bucket`, `.expiry`, `.linkValid` | a separate unlocked bucket for exports; empty refuses export |
| `query.resolve.enabled` | give this service the keys to open sealed identifiers. It needs a key provider: with `keys.provider: none` there is nothing to resolve and the RPC is `unimplemented`. `local` mounts the writer's key directory read-only (ReadWriteMany required); `transit` signs in its own way (`query.resolve.transit.role`, `.token.existingSecret` or `.tokenFile`), never as the writer |
| `query.serviceAccount.annotations` | its role: read on the archive, write on the exports bucket |
| `networkPolicy.queryIngressFrom` | who may reach it, normally the gateway; empty leaves it open in the cluster |

The service's limits (`filter` 4 terms, `sort` 4, `in` 100 values, `limit`
1000) are fixed in the service, not configured; see the
[API reference](api.md).

The built `audit-query` reads who may authenticate and what each caller may
see from one file, `--grants`/`AUDIT_GRANTS`:

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
may read what names a **preset**:

```yaml
presets:
  - name: access-roster
    issuer: https://id.example.com       # required once more than one issuer is trusted
    claim: groups                        # the default
```

The access-roster preset reads groups named `<scope>:audit:<role>`, the
estate's grant grammar. The scope is `all` or an audit tenant id, byte for
byte; an environment is never in the name, because each deployment's query
service requires its own token audience and the issuer decides who may hold
which. A role grants operations over the profiles built from certain presets,
so the deployment's own profile names need no mention — which is why a preset
needs `--deployment`:

| role | profiles built from | operations | `all` allowed |
|---|---|---|---|
| `viewer` | `history` | search, facets, get | no: `all:audit:viewer` grants nothing |
| `security` | `security`, `dora`, `pci-dss`, `nen-7513` | search, facets, get, tail, export | yes |
| `auditor` | every profile built from no `billing-*` preset | search, facets, get, export | yes |
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
- a preset is named without `--deployment`, or a preset that does not exist.

A bearer token in `Authorization` is accepted, and so is the access token the
fleet gateway forwards. Verification is
[gateway-auth](https://github.com/truvity/gateway-auth)'s, one verifier per
issuer: discovery, a key set refreshed in the background, signature, issuer,
audience and expiry. That library allows no clock skew.

## Digest job

| value | meaning |
|---|---|
| `jobs.digest.enabled`, `.schedule` | hourly by default (`7 * * * *`) |
| `jobs.digest.signingKey.existingSecret`, `.secretKey`, `.keyID` | sign with an ed25519 PEM key from a Secret |
| `jobs.digest.kmsKey` | or with an AWS KMS `ECC_NIST_P256` signing key |
| `jobs.digest.transit.key`, `.role` / `.token` / `.tokenFile` | or with an OpenBAO transit ed25519 key, signing in with its own role on `openbao.auth.mount` |
| `jobs.digest.lookback` | how far before a window to look for objects keyed under an older day. Default 7 days; the verify job's must be at least this |
| `jobs.digest.maxWindows` | how many windows one run may seal when catching up. Default 168 |
| `jobs.digest.serviceAccount` | its own identity: the signing key is its, never the writer's |

Exactly one signer. `audit digest --deployment … (--key | --kms-key |
--transit-key) --bucket --sink [--lookback --max-windows --instance]` is what
the job runs; `--sink` is the writer it records itself through
(`audit.digest.written`).

## Verify job

| value | meaning |
|---|---|
| `jobs.verify.enabled`, `.schedule`, `.window` | nightly, over the last 24 hours, per profile |
| `jobs.verify.publicKey.existingSecret`, `.secretKey` | the public half only |
| `jobs.verify.record` | write what each verification found under `verified/`, which `Get` reports as a record's `verified_at`; needs `s3:PutObject` there |

## Clock job

| value | meaning |
|---|---|
| `jobs.clockSync.ntp` | time references; the quickest to answer is believed, and one being unreachable is survivable |
| `jobs.clockSync.maxOffset` | the offset beyond which the run fails. Default 1s; 0 records any offset and never fails |
| `jobs.clockSync.schedule` | daily |

`audit clock-sync --ntp … --sink … [--max-offset --timeout]`.

## Purge job

| value | meaning |
|---|---|
| `jobs.purge.enabled`, `.schedule` | daily |
| `jobs.purge.identifyingAfter` | how long the index keeps who an event happened to. No default: no shipped preset states one, so it is the deployment's own policy |
| `jobs.purge.dedupeWindow` | how long a written identifier is remembered. Default the widest window the profiles ask for |

`audit purge --deployment --database [--identifying-after --dedupe-window
--dry-run]`. It never touches the archive.

## Workload identity

The receiver reads one file, `--workloads`/`AUDIT_WORKLOADS`:

```yaml
issuers:
  - url: https://oidc.example.com/id/CLUSTER   # the cluster's service-account issuer
    audience: audit
```

A caller presents its projected service-account token as a bearer. The tools
in this repository read it from the file named by `AUDIT_TOKEN_FILE` on every
request, because the kubelet replaces it before it expires.

Whose catalogue a registration is, comes from that verified identity and never
from the document. The file's `workloads` list is what says so: which service
account speaks for which source. A caller missing from it registers as nobody
and its registration is refused, which is also why an installation that keeps
an index and verifies callers must fill the list in — the chart refuses to
render otherwise. There is deliberately no shortcut that lets any verified
caller register for the application: a workload that could register under
another source could describe another application's records, and everything
downstream reads the description.

The issuer's discovery document is fetched at start-up, so it must be reachable
over HTTPS from the pods. A managed cluster's public OIDC provider is. The API
server's own in-cluster issuer usually is not without its CA and a credential,
which this does not yet take.
