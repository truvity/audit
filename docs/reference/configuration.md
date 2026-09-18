# Configuration reference

The Go emitter's options, and the chart's values for each component. Every
name here exists in the code or in `charts/audit/values.yaml`; the chart's
file has a comment on each.

## Emitter library

`emit.New(emit.Options{…})`:

| option | meaning |
|---|---|
| `Source`, `Catalogue` | the source this emitter speaks for, and its loaded catalogue; a record of an action the catalogue does not declare is refused |
| `Sink` | where records go: `sink.NewClient(httpClient, writerURL)` for a writer over Connect (with `auth.TokenFile` for the workload token), a JetStream sink, or a writer in the same process |
| `Outbox` | `emit.OpenFileOutbox(dir)`: the local store outbox delivery needs. An emitter without one refuses a catalogue that declares `outbox` |
| `Publish` | how often the outbox is drained. Default one second |
| `Timeout` | how long a `block` write may take. Default 10s |
| `Queue`, `Batch`, `Flush` | best-effort buffering: how many records may wait (1024), how many are sent together (100), and how often (one second) |
| `Bounds` | size limits; default `record.Default` |
| `Version`, `Instance` | this process on every record; the writer replaces the observer's identity with the one it verified |
| `Hooks` | `OnDropped`, `OnFailed`, `OnWritten`, `OnRefused`: where a deployment counts and alerts |

`emit.Middleware(trustedHops)` records each request's client address, user
agent, request and trace ids on every record made while serving it.
`trustedHops` is how many proxies of your own sit in front: 0 records the
connection's peer, and getting it wrong records a load balancer as the actor's
address. `emit.Register(ctx, emit.Registration{…})` registers the catalogue
with an installation's registry at start-up.

## Split writer

These are the chart's values (`charts/audit/values.yaml`), which are the
binary's flags with dots.

| setting | meaning |
|---|---|
| `bucket`, `prefix`, `region`, `kmsKey` | the archive |
| `governance` | lets a privileged role shorten a retention. Off, and the chart refuses it on: a deployment that wants it says so in a values file of its own |
| `profiles` | composition of presets and prefixes, the document `audit-writer --deployment` reads |
| `replicas` | writers sharing the stream. Above one needs `database` and a key directory every replica can write |
| `stream.url`, `stream.name`, `stream.consumer` | JetStream. Without a URL the writer only serves its sink, which is what an application embedding it wants |
| `stream.batch` | how many records are taken at once. Default 100 |
| `stream.ackWait` | how long the stream waits for a batch to be taken before offering it again. Default 30s, and it must exceed the longest a write can honestly take: a batch is acknowledged only once its records are in the archive |
| `roll.interval` | how often an object is rolled and put |
| `database.url` or `database.existingSecret` | the index and the shared deduplication table, one database. Without it the writer indexes nothing and deduplicates in process |
| `database.migrate` | apply the schema from a pre-upgrade hook Job. The writer refuses to start on a version it does not know and never migrates itself |
| `keys.provider` | `local` (a root and a directory) or `transit` (OpenBAO; the one for several replicas — see [OpenBAO keys](../operations/openbao-keys.md)) |
| `openbao.address`, `.mount`, `.namespace` | the OpenBAO the transit key provider and the transit digest signer reach: the server, where transit is mounted (`transit`), and the namespace (empty is root) |
| `openbao.auth.mount`, `.audience`, `.expirationSeconds` | the JWT auth mount each component signs in on with its projected service-account token (e.g. `jwt-devel`), the token's audience (`openbao`) and lifetime (600) |
| `keys.transit.prefix` | what every key name starts with (`audit`) |
| `keys.transit.role`, or `.token.existingSecret`, or `.tokenFile` | how the writer signs in, exactly one: a role on `openbao.auth.mount` (nothing stored), a token Secret, or a token file |
| `trust.configMap`, `trust.key` | a CA bundle trusted beside the system roots, e.g. trust-manager's for a private chain; mounted by every pod that reaches OpenBAO or Postgres (`PGSSLROOTCERT`) |
| `keys.local.existingSecret` | the 32-byte root the data keys are wrapped under |
| `keys.local.persistence` | where the wrapped keys live. They are random, not derived, so this is the only copy: back it up, and use ReadWriteMany for more than one replica |
| `catalogues` | catalogue documents registered at start-up, by name |

The writer verifies who publishes with `--workloads`/`AUDIT_WORKLOADS`, a file
naming the issuers trusted to name a workload (see "Workload identity" below),
and stamps the caller's service account as each record's observer. Without it,
the writer refuses to start unless given `--anonymous-writes`, which is for a
trial install. Records that arrive over the stream carry no verified observer:
the stream's own authentication is what admits a publisher there.

The built `audit-writer` takes these as flags or environment variables:
`--database`/`AUDIT_DATABASE` is the index, `--replicas`/`AUDIT_REPLICAS` is how
many writers share the stream. A writer whose database is at a schema version
this build does not know refuses to start; run `audit migrate --database <url>`
first, from one place.

## Query service

In the chart, under `query` (`query.enabled`):

| value | meaning |
|---|---|
| `query.searcher` | `postgres` (the index) or `s3scan` (the archive, within a budget; no database) |
| `query.database.existingSecret` or `.url` | the query service's **own** role: `usage` on the schema, `select` on its tables, not the owner. Tenant row-level security binds only a non-owner, so the chart refuses the writer's credentials here |
| `query.database.role` | that role's name. When set, the migration job grants it usage and select, now and on tables created later (`audit migrate --reader`), and nothing else; the role must already exist |
| `query.grants` | the grants file below, inline |
| `query.exports.bucket`, `.expiry`, `.linkValid` | a separate unlocked bucket for exports; empty refuses export |
| `query.resolve.enabled` | give this service the keys to open sealed identifiers. `local` mounts the writer's key directory read-only (ReadWriteMany required); `transit` signs in its own way (`query.resolve.transit.role`, `.token.existingSecret` or `.tokenFile`), never as the writer |
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

The writer and the registry read one file, `--workloads`/`AUDIT_WORKLOADS`:

```yaml
issuers:
  - url: https://oidc.example.com/id/CLUSTER   # the cluster's service-account issuer
    audience: audit
workloads:                                     # the registry's map; the writer ignores it
  - subject: system:serviceaccount:wallet:wallet-api
    source: wallet
```

A caller presents its projected service-account token as a bearer. The tools
in this repository read it from the file named by `AUDIT_TOKEN_FILE` on every
request, because the kubelet replaces it before it expires. With more than one
issuer, every workload entry must name its issuer, since two clusters can both
have that namespace and service account.

The issuer's discovery document is fetched at start-up, so it must be reachable
over HTTPS from the pods. A managed cluster's public OIDC provider is. The API
server's own in-cluster issuer usually is not without its CA and a credential,
which this does not yet take.
