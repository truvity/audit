# Configuration reference

The Go emitter's options, the configuration file of each binary, and the
chart's values for each component. Every name here exists in the code, in
`schemas/config/` or in `charts/audit/values.yaml`, and the chart's file has a
comment on each value. Where something is designed and not built,
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

## The configuration file

`audit-writer`, `audit-observe` and `audit-query` take one flag, `--config <file>` (and
`--version`, `--help`). `audit verify`, `audit purge`,
`audit clock-sync` and `audit migrate` take `--config <file>` in place of every
other flag of the command; only `--json`, which changes how the report is
printed, may accompany it. The interactive flags of `audit` stay for a person
at a keyboard, and a command line that names both a file and another flag is
refused. Nothing else configures a process: there are no flags with an
environment fallback
([0021](../decisions/0021-one-validated-configuration-file.md)).

Each binary's file is YAML, and is validated against that binary's JSON Schema
before anything starts. The schemas are in `schemas/config/`, one per binary
or command, and ship in the release:

| binary or command | schema |
|---|---|
| `audit-writer` | `audit-writer.schema.json` |
| `audit-observe` | `audit-observe.schema.json` |
| `audit-query` | `audit-query.schema.json` |
| `audit verify`, `purge`, `clock-sync`, `migrate` | `audit-verify.schema.json`, `audit-purge.schema.json`, `audit-clock-sync.schema.json`, `audit-migrate.schema.json` |

An unknown key, a missing required key or a value of the wrong type is a
start-up error that names the path to it. A few rules a schema cannot say run
after it has accepted the file; they are listed under
[Refusals](#refusals). The chart validates the same files when it renders, and
`just config-schemas` regenerates the schemas from
`internal/config/schema/schema.go`.

Durations are strings in Go's notation: `30s`, `2m`, `168h`.

### Secrets

A secret is never in the file. A field that holds one is named `...Env` and
holds the **name** of an environment variable; the process reads exactly the
variables the file names, and an unset or empty one is an error naming the
variable. These are all of them:

| key | holds |
|---|---|
| `database.passwordEnv` | the Postgres password |
| `bucket.credentialsEnv.accessKeyID`, `.secretAccessKey` | the static credentials of an S3-compatible store |
| `openbao.tokenEnv` | an OpenBAO token |

A password inside a database URL is refused. Token, key and root **files** are
referenced by path, not by name: `tokenFile`, `jwtFile`, `rootFile`,
`keyFile.path`, `caFile`. On a platform the file is a mounted
Secret or a projected token.

### Telemetry

Telemetry is the OpenTelemetry SDK's own environment, and the file has nothing
about it: `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME` and the rest of
`OTEL_*`. The platform decides where signals go, so the same file runs in
every environment. The chart has no telemetry value; set the variables on the
pods from the application's chart or a collector configuration.

### Documents the file points at

Four things a file names by path are separate documents, each with its own
contract, and not part of the file:

| key | document | in the chart |
|---|---|---|
| `deployment` | the profile configuration: which presets each profile is composed from, and `externalIdentifiersAreOpaque` | `/etc/audit/deployment.yaml`, from `profiles` and `externalIdentifiersAreOpaque` |
| `workloads` | the issuers trusted to name a workload, and which service account speaks for which source ([Workload identity](#workload-identity)) | `/etc/audit/workloads.yaml`, from `workloadIdentity` |
| `grants` | the query service's issuers, presets and rules ([Query service](#query-service)) | `/etc/audit/grants.yaml`, from `query.grants` |
| `catalogues` | a directory of catalogue documents registered at start-up | `/etc/audit/catalogues`, from `catalogues` |

## Shared blocks

These blocks recur under several keys. They follow the shared fragments of the
component contract (`postgres.json`, `bucket.json`, `listen.json`,
`nats.json`).

**`database`** (a PostgreSQL connection)

| key | type | default | meaning |
|---|---|---|---|
| `url` | string, required | | a `postgres://` or `postgresql://` URL without a password. A URL with one is refused, and one that does not parse is refused |
| `passwordEnv` | string | none: the connection needs no password | secret by reference: the name of the variable holding the password |
| `maxConnections` | integer, at least 1 | 10 | the pool size of this process. Size it against the server's limit divided by the number of processes |

**`bucket`** (an object store addressed by the S3 API)

| key | type | default | meaning |
|---|---|---|---|
| `name` | string, required | | the bucket, which exists already |
| `region` | string | the SDK's own resolution | the region the bucket is in |
| `endpoint` | URI | the SDK's own resolution for the region | override the API endpoint, for a store that is not AWS |
| `ca` | path | the system trust store | a CA bundle trusted for that endpoint, mounted by the platform |
| `pathStyle` | boolean | false | address the bucket as a path rather than a host, for a certificate that does not cover a bucket subdomain |
| `credentialsEnv.accessKeyID`, `.secretAccessKey` | strings, both required if the block is present | none: the SDK's ambient credentials, which is what a workload identity provides | secret by reference: the names of the variables holding static credentials |

**`archive`** (where the archive is, and how it is written)

| key | type | default | meaning |
|---|---|---|---|
| `bucket` | `bucket`, required | | the archive's bucket |
| `prefix` | string | none | the prefix within the bucket. Required in a bucket shared with other installations: it is what keeps two apart |
| `lockMode` | `compliance`, `governance` or `none` | `compliance` | the Object Lock mode every object is written in; `none` is for a store without Object Lock or profiles that demand none ([0014](../decisions/0014-lock-modes-and-store-tiers.md)). A process refuses to start when a profile demands a stricter mode. `audit-query` and `audit verify` read; `audit-query` has no `lockMode` |
| `kmsKey` | string | the bucket's default encryption | the key objects are encrypted with. Only `audit-writer` has it |

**`listen`**: `address` (string, `host:port` such as `:8080`, required when the
block is present). Both servers default it to `:8080`.

**`sink`** (the writer a process records through)

| key | type | default | meaning |
|---|---|---|---|
| `url` | string, required | | the writer's base URL |
| `tokenFile` | path | none: no token, which only an anonymous trial install accepts | the file holding the bearer token, read afresh on every request; in a cluster the pod's projected service-account token |
| `expect` | `logged`, `queued` or `archived` | none: the client claims nothing | what the writer at `url` is configured to give. A client cannot learn that until it writes, so the file says (`sink.Client.Expecting`); the process's `require` is checked against it at start-up and against every acknowledgement afterwards |

**`openbao`** (how a process reaches an OpenBAO transit engine)

| key | type | default | meaning |
|---|---|---|---|
| `address` | string, required | | the server, for example `https://openbao.example.com:8200` |
| `mount` | string | `transit` | where the transit engine is mounted |
| `namespace` | string | the root namespace | the namespace the engine and the auth mount are in |
| `caFile` | path | the system roots | a PEM bundle trusted beside them |
| `login.mount`, `.role`, `.jwtFile` | strings, all required | | sign in with a JWT: the auth mount, the role on it, and the file holding the pod's projected token, read at every login. Nothing is stored |
| `tokenFile` | path | | sign in with a token read from a file on every call |
| `tokenEnv` | string | | secret by reference: the name of the variable holding a token |

Exactly one of `login`, `tokenFile` and `tokenEnv`.

**`keys`** (where pseudonymisation keys live)

| key | type | default | meaning |
|---|---|---|---|
| `provider` | `none`, `local` or `transit`, required | | `none` means no pseudonyms, no key material and no resolve ([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)). `local` requires `local`, `transit` requires `transit`, and `none` allows neither |
| `local.rootFile` | path, required | | a file holding the 32-byte root the data keys are wrapped under |
| `local.dir` | path | in memory | where the wrapped data keys are kept. They are random, not derived, so this directory is the only copy. Unset keeps them in memory, which only a trial install should |
| `transit.prefix` | string | `audit` | what every key's name starts with: `<prefix>.<purpose>.<tenant>` |
| `transit.openbao` | `openbao`, required | | the engine and how to sign in |

## audit-writer

One binary in two roles, chosen by `mode`. As a `writer`, the default, it
serves the sink, writes the archive and consumes a stream when `stream` is
set. As a `receiver` it serves the sink and publishes to the stream, and holds
neither an archive nor a key provider: a receiver holding either would be a
writer. In direct mode one process in `writer` mode is both. In stream mode
the receiver publishes to JetStream and acknowledges the replicated publish,
and the same image runs again as the writer. Both serve `RegistryService`, so
the application registers its catalogue with the address it writes to.

| key | type | default | meaning |
|---|---|---|---|
| `mode` | `writer` or `receiver` | `writer` | the role above |
| `listen` | `listen` | `:8080` | the address the sink is served on |
| `deployment` | path, required | | the profile configuration |
| `workloads` | path | | the file naming the issuers trusted to say which workload is publishing ([Workload identity](#workload-identity)). Exactly one of `workloads` and `anonymousWrites` |
| `anonymousWrites` | `true` | | accept writes from callers nobody verified, stamped with no observer. For a trial install only |
| `catalogues` | path | none | a directory of catalogues registered at start-up. An application that registers its own over `RegisterCatalogue` needs none. Not in a receiver |
| `archive` | `archive` | | required in `writer` mode, refused in `receiver` mode |
| `database` | `database` | none | the shared deduplication table and the catalogue registry, in the application's Postgres, as the writer's **own** role (`audit migrate --writer`): it holds those tables and none of the index, which `audit-observe` writes. Without it the writer deduplicates in process and may run one replica only. The writer refuses to start on a schema version it does not know and never migrates itself |
| `replicas` | integer, at least 1 | 1 | how many writers share this stream: the number of writer pods, which the writer cannot see for itself |
| `keys` | `keys` | none, which is provider `none` | pseudonymisation keys. Not in a receiver |
| `forgetIdentities` | boolean | false | do not keep the identity behind each pseudonym, sealed under its key. By default it is kept, so that resolve can find it |
| `require` | `logged`, `queued` or `archived` | `archived` for a writer, `queued` for a receiver | the weakest durability the chain may give ([0017](../decisions/0017-sink-durability-and-transports.md)). At start-up the process wraps its chain in `sink.Guard` and refuses to run if the chain can never give it; a write acknowledged weaker fails. See [Durability](#durability-require-forward-consume) |
| `forward.nats`, `.sqs`, `.log` | exactly one; `nats` and `sqs` as below | | a receiver's onward transport. Required in `receiver` mode (or `stream`, its NATS shorthand); refused in `writer` mode. `forward.nats` takes the keys of `stream` |
| `forward.sqs.queueUrl` | string, required with `forward.sqs` | | the queue the receiver publishes to |
| `forward.sqs.region` | string | the SDK's (`AWS_REGION`) | the queue's region |
| `forward.sqs.fifo` | boolean | derived from the URL | the queue is FIFO. It must agree with the URL, which ends in `.fifo` for one |
| `forward.log` | `{}` | | the `log` sink: one JSON line per record on standard output, which survives nothing but the log pipeline. Allowed only with `require: logged` |
| `consume.nats`, `.sqs` | exactly one | | what a writer reads its records from, beside its own sink. Not in a receiver. `consume.nats` takes the keys of `stream` |
| `consume.sqs.queueUrl`, `.region`, `.fifo` | as `forward.sqs` | | the queue the writer consumes |
| `consume.sqs.batch` | integer, 1 to 10 | 10 | how many messages are received at once |
| `consume.sqs.visibility` | duration | `1m` | how long a received message is hidden from other consumers while the writer writes it. It must outlast a write to the bucket, or the message is delivered twice |
| `stream.nats.url` | string, required with `stream` | | the JetStream server, for example `nats://nats:4222` |
| `stream.nats.tokenFile` | path | none: no credentials | the token presented to a broker that verifies who connects, read afresh on every connect ([stream](../deployment/stream.md#authenticating-to-the-stream)) |
| `stream.name` | string | `AUDIT` | the stream |
| `stream.consumer` | string | `audit-writer` | the durable consumer this installation's writers share |
| `stream.batch` | integer, at least 1 | 100 | how many records are taken at once |
| `stream.ackWait` | duration | `2m` | how long the stream waits for a batch to be taken before offering it again. Records are acknowledged only once they are in the archive, so it must exceed `roll.interval` plus the longest a put can take |
| `roll.interval` | duration | `30s` | how long gathered records wait before they are written. In direct mode there is no stream to gather from, and it is only how long an object may stay open inside one write |
| `roll.maxRecords` | integer, at least 1 | 5000 | how many gathered records are written at once. The roll ends at whichever of the two is reached first, or at the roller's byte limit |

`stream` is the NATS shorthand and is kept as it was: in a receiver it is
`forward.nats`, in a writer `consume.nats`, with the same defaults. Give it or
the longhand, not both. A `receiver` requires `stream` or `forward`. A writer
with neither `stream` nor `consume` only serves its own sink; with one, it
also consumes.

### Durability: `require`, `forward`, `consume`

Every acknowledgement carries a durability ([0017](../decisions/0017-sink-durability-and-transports.md)):
`archived` (the object is in the bucket), `queued` (a replicated queue holds it
and will deliver it) or `logged` (a log line). `require` is the floor a process
holds its own chain to, and its default is the strongest the mode can give, so
that anything weaker is something a person wrote down:

- a **writer** defaults to `archived`: it puts the object itself, and a
  deployment that wants a weaker promise says so;
- a **receiver** defaults to `queued`, not `archived`: it holds no archive (a
  receiver holding one would be a writer), so `archived` is not its to promise,
  and it is refused there. Its promise is what its onward transport gives, and
  the writers behind it are what reach `archived`.

At start-up the process builds its chain (the receiver, and the transport
`forward` names; or the writer) and computes the best it can ever give. A chain
below `require` is a start-up error, not a surprise on the first privileged
action. `forward.log` can give `logged` at most, so it is allowed only with
`require: logged`, which the schema, the loader and the guard each refuse
otherwise: it is for a deployment that has chosen its log pipeline as its
record.

The queue's credentials are never in the file. `sqs` uses the AWS SDK's ambient
credentials, which on Kubernetes is the pod's workload identity (EKS Pod
Identity, or IRSA through a service-account annotation), the same way the
archive's bucket does. The role needs `sqs:SendMessage` for the receiver,
`sqs:ReceiveMessage`, `sqs:DeleteMessage` and `sqs:ChangeMessageVisibility`
for the writers. `charts/audit/examples/sqs.yaml` is a full SQS install; its
transport is not yet tested against live AWS, only against a fake and
LocalStack.

A process that records through a writer of its own (`audit-query` and every
job with a `sink`) takes `require` too, with `sink.expect` saying what that
writer gives: `require` unset checks nothing, and set it needs `expect` at
least as strong, checked at start-up, with every acknowledgement checked
afterwards.

The receiver verifies who writes with `workloads` and stamps the caller's
service account as each record's observer. Without it, it refuses to start
unless given `anonymousWrites: true`. Records that arrive over the stream
carry no verified observer: the stream's own authentication is what admits a
publisher there. The observer version stamped on records is the build's
version and is not configurable.

## Indexer

`audit-observe` follows the archive and writes the index
([0020](../decisions/0020-observe-follows-the-bucket.md),
[0024](../decisions/0024-indexer-and-query-are-separate-processes.md)). It
lists `records/<profile>/<tenant>/` from a cursor kept in Postgres, indexes the
objects older than the settle window, and moves the cursor in the transaction
that writes their rows. It reads the archive and never writes it, and it serves
only `/healthz`: the query service is `audit-query`, a process of its own under
a role that can only read.

| key | type | default | meaning |
|---|---|---|---|
| `listen` | `listen` | `:8080` | the address `/healthz` is served on |
| `archive` | `archive`, required | | the archive to follow (`bucket`, `prefix`). It has no `lockMode`: this process reads. The catalogues and their extension schemas are read from it too |
| `database` | `database`, required | | the index, as the indexer's **own** role (`audit migrate --observe`): read and write on the index and its cursors, nothing of the deduplication table, not the owner, and not the writer's or the query service's |
| `settle` | duration | `2m` | how far behind now the cursor stays. An object's key is fixed when its put starts and it is visible when it ends, so it must be longer than a put can take and than the clocks of the writers and of this process can disagree. It is the least time between a record's acknowledgement and its appearance in search |
| `interval` | duration | `30s` | the poll: how often a pass runs when nothing woke it. A lost wake-up costs at most this |
| `batch` | integer, at least 1 | `500` | rows written in one transaction; a transaction ends at an object's end |
| `profiles` | list of strings, at least one, unique | every profile the archive has | the profiles to follow. Profiles and tenants are discovered by listing |
| `wake.nats.nats`, `wake.nats.subject` | `nats` (url, `tokenFile`), string | | a subject carrying the bucket's notifications. Their content is never read |
| `wake.sqs` | `sqs` | | a queue of the bucket's notifications that is the indexer's own: each message wakes a pass and is deleted. Credentials are the SDK's ambient ones |

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

| key | type | default | meaning |
|---|---|---|---|
| `listen` | `listen` | `:8080` | the address it is served on |
| `grants` | path, required | | the grants file, below |
| `sink` | `sink`, required | | the writer every read is recorded through |
| `require` | `logged`, `queued` or `archived` | none: checks nothing | the weakest durability the writer's acknowledgements may carry. Needs `sink.expect` at least as strong ([durability](#durability-require-forward-consume)) |
| `deployment` | path | none | the profile configuration. A grant preset needs it, because a preset turns roles into the deployment's own profiles |
| `searcher` | `postgres` or `s3scan` | `postgres` | `postgres` is the index; `s3scan` is the archive, within a budget, for a deployment with no database. The scan orders by `occurred_at` only and refuses `recorded_at`, so a deployment on it can search the trail but cannot follow it: there is no live tail ([search](../design/search.md#tail)) |
| `database` | `database` | | the index, as the query service's **own** role (`audit migrate --reader`): `usage` on the schema, `select` on the index's tables, not the owner. Tenant row-level security binds only a non-owner. Required unless `searcher` is `s3scan` |
| `archive.bucket`, `archive.prefix` | `bucket`, string | | what `s3scan` reads, and where Get finds a record's object. Required with `s3scan`. Without it Get still answers, with where the copy is and nothing about whether it has been verified (nothing yet sets that: it is for seals). The service's region is `archive.bucket.region` |
| `exports.bucket` | `bucket`, required with `exports` | | a separate bucket with no Object Lock, which clears it. Without `exports` the export operation is refused. It inherits nothing from the archive: name its endpoint, path style and `credentialsEnv` here |
| `exports.expiry` | duration | `168h` | how long an export is kept before the bucket clears it |
| `exports.linkValid` | duration | `1h` | how long a download link works |
| `keys` | `keys` | none | the writer's key provider, which turns resolve on. With provider `none`, or no `keys`, there is nothing to resolve and the RPC is `unimplemented`. `local` reads the writer's key directory, which must be shared; `transit` signs in as the query service's own identity, never the writer's |

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
needs `deployment` in the query service's configuration:

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
- a preset is named and the configuration has no `deployment`, or a preset that does not exist.

A bearer token in `Authorization` is accepted, and so is the access token the
fleet gateway forwards. Verification is
[gateway-auth](https://github.com/truvity/gateway-auth)'s, one verifier per
issuer: discovery, a key set refreshed in the background, signature, issuer,
audience and expiry. That library allows no clock skew.

## Scheduled jobs

Each job is `audit <command> --config <file>`, runs to completion and records
what it did through the writer's own sink, so each needs `sink` and, where
the writer verifies callers, a `tokenFile`. Each has its own identity.

### audit verify

Checks every record object of a range of ingest time against the
[bucket contract](bucket-contract.md) and reports what it finds
([verification](../operations/verify.md)). It reads the archive only, so its
`archive` has no `lockMode`, and it needs no key. Nightly in the chart, over the
last 24 hours. One job checks every profile it names, or every profile the
deployment composes.

| key | type | default | meaning |
|---|---|---|---|
| `deployment` | path, required | | the profile configuration; each object's lock is held to what its profile demands |
| `archive` | `archive`, required | | the archive to read (`bucket`, `prefix`) |
| `profiles` | list of strings, at least one, unique | every profile the deployment composes | the profiles whose objects to check |
| `last` | duration | `24h` | check the objects ingested in the last this long, ending at the hour that has closed |
| `sink` | `sink` | none | the writer the job records what it checked through (`audit.digest.verified` or `audit.digest.failed` per ingest hour) |
| `require` | `logged`, `queued` or `archived` | none | the weakest durability the writer's acknowledgements may carry; needs `sink` and `sink.expect` ([durability](#durability-require-forward-consume)) |

The keys `publicKeyFile`, `lookback` and `record` of the v0 job are gone: the
check needs no key, the range is of ingest time so there is nothing to look
back over, and there is no `verified/` prefix to write.

### audit purge

Brings the index and the deduplication table within the profiles. Daily in the
chart. It never touches the archive.

| key | type | default | meaning |
|---|---|---|---|
| `deployment` | path, required | | the profile configuration |
| `database` | `database`, required | | the index and the deduplication table, as the purge job's **own** role (`audit migrate --purge`): delete from both, and add nothing to either |
| `identifyingAfter` | duration | unset: nothing is forgotten early | how long the index keeps who an event happened to. No shipped preset states one, so it is the deployment's own policy |
| `dedupeWindow` | duration | the widest window the profiles ask for | how long a written identifier is remembered |

### audit clock-sync

Compares the clock with UTC and records the answer. Daily in the chart.

| key | type | default | meaning |
|---|---|---|---|
| `ntp` | list of strings, at least one, required | | time references, host or host:port; the quickest to answer is believed, and one being unreachable is survivable |
| `sink` | `sink` | none | the writer the reading is recorded through |
| `require` | `logged`, `queued` or `archived` | none | the weakest durability the writer's acknowledgements may carry; needs `sink` and `sink.expect` ([durability](#durability-require-forward-consume)) |
| `maxOffset` | duration | `1s` | the offset beyond which the run fails. `0s` records any offset and never fails |
| `timeout` | duration | `5s` | how long to wait for a reference |

### audit migrate

Applies the schema and grants each part's database role what the part needs
and takes back the rest. In the chart it is a pre-install and pre-upgrade hook
Job; run it by hand from one place otherwise.

| key | type | default | meaning |
|---|---|---|---|
| `database` | `database`, required | | the database, as the **owner** of the tables. No part connects as the owner: an owner is bound by no grant and no row-level security, so the migration refuses to grant a role that is the owner |
| `writer` | string | none | the write path's role: the deduplication table, the catalogue registry and the key directory, and none of the index |
| `observe` | string | none | the indexer's role: read and write on the index and its cursors, and `execute` on `audit_ensure_month(date)`, the function that creates a month's partition as the owner |
| `reader` | string | none | the query service's role: `select` on the index's tables, bound by row-level security to the tenants of each request, and nothing else |
| `purge` | string | none | the purge job's role: delete from the index and the deduplication table |

Each role must already exist, and no role may be named for two parts: the
separation is that they are different. A part left unnamed is not granted
anything, and one that connects as the owner has no separation at all.

## Refusals

What the schemas say, and what is checked after them. Every one is a start-up
error that names the key.

Beyond types, required keys and unknown keys, the schemas refuse:

- a `workloads` and an `anonymousWrites` together, or neither;
- `mode: receiver` with `archive`, `catalogues`, `keys` or `consume`, or with
  neither `stream` nor `forward`;
- `forward` with none, or more than one, of `nats`, `sqs` and `log`; `consume`
  with none, or both, of `nats` and `sqs`; `forward` in a writer;
- `require: archived` in a receiver, and `forward.log` without `require: logged`;
- `require` on an emitter with no `sink`;
- a `writer` without `archive`;
- `keys.provider` of `local` without `local`, of `transit` without `transit`,
  or either block beside a provider that is not its own;
- an `openbao` with none, or more than one, of `login`, `tokenFile` and
  `tokenEnv`;
- a `signer` with none, or more than one, of `keyFile`, `kmsKey` and `transit`;
- a database URL that carries a password;
- a query service with the `postgres` searcher and no `database`, or with
  `s3scan` and no `archive`.

After the schema, the binaries refuse:

- a `require` the chain can never give (the guard, at start-up), and an
  emitter's `require` with no `sink.expect`, or one weaker than it;
- `forward.sqs.fifo: true` on a URL that does not end in `.fifo`;

- `stream.ackWait` not longer than `roll.interval`: a writer gathers records
  for one interval before it writes them and leaves them unacknowledged
  meanwhile, and a stream that gives up waiting sooner offers the same records
  to another writer;
- `replicas` above 1 without `database`: deduplication in one process only
  absorbs a repeat on the replica that saw the original, so a redelivery
  landing on another would be written twice;
- `replicas` above 1 with local keys and no `keys.local.dir`: each replica
  would mint its own keys and the same person would get a different pseudonym
  on each;
- an `exports.bucket` that is the archive's bucket on the same endpoint: an
  export is an unlocked copy meant to be cleared, and the archive's policy
  denies every delete;
- `keys` on the query service without `archive`, or with the local provider and
  no `keys.local.dir`: resolve opens what the writer sealed in the archive;
- a `database.url` that does not parse;
- a profile that demands a stricter `lockMode` than the archive's: the writer
  refuses at start-up, naming the profile and both modes;
- a profile whose name contains `/`: it is a key component.

The grants file has refusals of its own, listed with it under [Query service](#query-service).

## Workload identity

The receiver reads one file, named by `workloads` (in the chart,
`/etc/audit/workloads.yaml`, rendered from `workloadIdentity`):

```yaml
issuers:
  - url: https://oidc.example.com/id/CLUSTER   # the cluster's service-account issuer
    audience: audit
```

A caller presents its projected service-account token as a bearer. The jobs and the
query service read it from the file their `sink.tokenFile` names on every
request, because the kubelet replaces it before it expires; the interactive
`audit` commands read `--token-file` or `AUDIT_TOKEN_FILE` the same way.

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

## Chart values

The chart passes configuration through. Each component has a `config:` block,
rendered as it stands (`toYaml`) into a ConfigMap `<fullname>-<component>-config`
and mounted at `/etc/audit/config.yaml`. A key under `config:` is the
binary's key, validated twice: by `values.schema.json`, which is generated and
embeds the same schemas, and by the binary at start-up. The chart translates
none of it.

| component | `config:` is the configuration of | notes |
|---|---|---|
| `writer` | `audit-writer` | direct mode: the one pod. Stream mode: the consumers, `writer.consumers` of them |
| `receiver` | `audit-writer` with `mode: receiver` | stream mode only |
| `query` | `audit-query` | `query.enabled` |
| `observe` | `audit-observe` | `observe.enabled`: one Deployment, `<fullname>-observe`, with a ServiceAccount of its own and no Service |
| `migrate` | `audit migrate` | `migrate.enabled`, a pre-install and pre-upgrade hook Job |
| `jobs.verify`, `jobs.purge`, `jobs.clockSync` | `audit verify`, `purge`, `clock-sync` | one CronJob each. The verify job is one CronJob (`<fullname>-verify`) covering the `profiles` its config lists, or every profile |

### What is a value and what is configuration

Everything else under a component is the platform's, not the binary's:

| value | meaning |
|---|---|
| `secretEnv` | a list of `{name, secretName, key}`: an environment variable taken from a Secret's key. The config names `name` as the holder of a secret (`passwordEnv`, `credentialsEnv`, `tokenEnv`). `optional: true` allows a missing key |
| `secretMounts` | a list of `{secretName, mountPath}`: a Secret mounted read-only as a directory, for a key or a root the config names by path (`local.rootFile`, a key file) |
| `tokens` | a list of `{audience, mountPath, expirationSeconds, path}`: a projected service-account token of that audience (lifetime 3600 by default), a file named `token` (or `path`) in the directory `mountPath`, which the config names (`tokenFile`, `jwtFile`). It is read afresh by whatever names it, because the kubelet replaces it before it expires |
| `serviceAccount` | the identity of the component, for Pod Identity or IRSA annotations. Every component has its own, `{create, name, annotations}`: `receiver.serviceAccount`, `query.serviceAccount`, `jobs.*.serviceAccount`. Created, it is `<fullname>-<component>` unless `name` says otherwise; with `create: false` the component runs as `name`, or as the release's own top-level `serviceAccount` (the writer's) when `name` is empty. In stream mode the chart refuses a receiver and a writer with the same name |
| `replicas`, `writer.consumers`, `query.replicas` | pod counts. `replicas` is the front door's: the writer in direct mode, the receiver in stream mode |
| `schedule`, `enabled` | for each job |
| `image`, `resources`, `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`, security contexts | the pods |

The values that are not configuration of a binary:

| value | meaning |
|---|---|
| `mode` | `direct` (one process: the front door and the write path) or `stream` (a receiver in front, `writer.consumers` writers behind). It decides which Deployments are rendered; the binaries' own `mode` is in `receiver.config` |
| `profiles`, `externalIdentifiersAreOpaque` | rendered as the profile document, `/etc/audit/deployment.yaml`, which every config's `deployment` names. `externalIdentifiersAreOpaque` declares that the identifiers the installation receives for external people mean nothing outside its own database, which relaxes a profile's `external: pseudonym` to `clear` ([presets](presets.md#what-a-deployment-can-relax)) |
| `workloadIdentity.issuers`, `.audience`, `.workloads` | `audience` is the audience an issuer entry takes when it names none. Rendered as `/etc/audit/workloads.yaml`, which the writer's `workloads` names. The chart refuses an installation that keeps an index and verifies callers with no `workloads` mapping |
| `query.grants` | rendered as `/etc/audit/grants.yaml`, which the query service's `grants` names |
| `catalogues` | catalogue documents by name, mounted at `/etc/audit/catalogues` for the writer's `catalogues` |
| `keysVolume.enabled`, `.size`, `.storageClass`, `.accessModes`, `.existingClaim` | the volume for the `local` key provider, mounted at `/var/lib/audit/keys` on every pod that writes. The keys are random, not derived, so this is the only copy: back it up, and use ReadWriteMany for more than one replica. `query.keysVolume: true` mounts it read-only on the query service for resolve |
| `trust.configMap`, `trust.key` | a CA bundle trusted beside the system roots, e.g. trust-manager's for a private chain, mounted at `/etc/audit/trust/<key>` on every pod, for the `ca` and `caFile` keys and a Postgres URL's `sslrootcert` to name |
| `extensions.billing.enabled`, `extensions.quotas.enabled` | the two projections, both off. The toggles are here so that a deployment's values need not change when the work behind them lands; today each renders nothing |
| `networkPolicy.enabled`, `.ingressFrom`, `.queryIngressFrom` | who may reach the writer's sink and the query service. Empty `queryIngressFrom` leaves the query service open in the cluster |

### Mount points

A config names these by path. The chart provides:

| path | what | from |
|---|---|---|
| `/etc/audit/config.yaml` | the component's own configuration | `<component>.config` |
| `/etc/audit/deployment.yaml` | the profile configuration | `profiles`, `externalIdentifiersAreOpaque` |
| `/etc/audit/workloads.yaml` | the issuers and workloads | `workloadIdentity` |
| `/etc/audit/grants.yaml` | the query service's grants | `query.grants` |
| `/etc/audit/catalogues/` | the catalogues | `catalogues` |
| `/etc/audit/trust/<key>` | the CA bundle | `trust` |
| `/var/lib/audit/keys` | the local key directory | `keysVolume` |

Anything else a config names, such as a token or a key file, is mounted by the
component's `secretMounts` or `tokens` at the path you give them.

### What the chart refuses

The chart checks what only the platform can see, in
`charts/audit/templates/_checks.tpl`:

- `mode` is `direct` or `stream`, and `profiles` is not empty;
- `writer.config.replicas` equals the number of writer pods the chart renders
  (`replicas` in direct mode, `writer.consumers` in stream mode): the writer
  refuses to run several without a database and refuses in-memory keys with
  several, and can only do that if it is told the truth;
- more than one writer pod needs `database` in `writer.config`;
- stream mode needs `receiver.config` with `mode: receiver`,
  `writer.config.stream` (or `consume`) and a `database` in `writer.config`, and
  `writer.config.mode` must not be `receiver`; direct mode refuses a receiver
  writer;
- more than one writer pod with a `keysVolume` that lacks `ReadWriteMany`, and
  `query.keysVolume` without an enabled `keysVolume` or without
  `ReadWriteMany`;
- `workloads` in the writer's config with no `workloadIdentity.issuers`, and
  issuers with no `workloads` in the config; an installation with an index and
  issuers but no `workloadIdentity.workloads`; a workload that names no issuer
  while more than one is trusted;
- `query.grants.issuers` empty when the query service is enabled, and a query
  `database.url` equal to the writer's, because an owner bypasses the tenant
  policies;
- the indexer (`observe.enabled`) running as the writer's, the query service's
  or the receiver's ServiceAccount, or connecting to the database as the
  writer's, the query service's or the migration's role (an owner), and a
  writer that connects as the migration's role: each part's identity is its
  own, at the cloud role and at the database role;
- `extensions.billing` without a profile composed from a metering preset, and
  `extensions.quotas` without `mode: stream`.

A value from before the file, such as `bucket` or `lockMode` at the top level,
is not accepted: it fails against `values.schema.json`, naming the key.

### Example

An internal service's installation, abridged from
`charts/audit/examples/direct.yaml`: the front door and the write path in one
process, no stream, and the index in a small Postgres of its own. The
application's chart sets this under its `audit:` key.

```yaml
mode: direct
replicas: 2                              # writer.config.replicas must say the same
profiles:
  security:
    presets: [security]
externalIdentifiersAreOpaque: true

workloadIdentity:
  issuers:
    - url: https://oidc.example.com/id/CLUSTER
  workloads:
    - subject: system:serviceaccount:app:api
      source: app

migrate:
  enabled: true
  config:
    database:                            # the OWNER of the tables; no part connects as it
      url: postgres://audit_owner@db.example.com:5432/audit?sslmode=verify-full
      passwordEnv: AUDIT_DATABASE_PASSWORD
    writer: audit_writer                 # each role is granted what its part needs
    observe: audit_observe
    reader: audit_query
    purge: audit_purge
  secretEnv:
    - {name: AUDIT_DATABASE_PASSWORD, secretName: audit-db-owner, key: password}

writer:
  config:
    deployment: /etc/audit/deployment.yaml
    workloads: /etc/audit/workloads.yaml
    replicas: 2
    archive:
      bucket: {name: audit-eu-example-1, region: eu-example-1}
      prefix: audit/app
      kmsKey: alias/audit-archive
    database:                            # the deduplication table and the registry only
      url: postgres://audit_writer@db.example.com:5432/audit?sslmode=verify-full
      passwordEnv: AUDIT_DATABASE_PASSWORD
  secretEnv:
    - {name: AUDIT_DATABASE_PASSWORD, secretName: audit-db, key: password}

observe:                                 # follows the archive and writes the index
  enabled: true
  config:
    archive:
      bucket: {name: audit-eu-example-1, region: eu-example-1}
      prefix: audit/app
    database:
      url: postgres://audit_observe@db.example.com:5432/audit?sslmode=verify-full
      passwordEnv: AUDIT_OBSERVE_DATABASE_PASSWORD
    settle: 2m
  secretEnv:
    - {name: AUDIT_OBSERVE_DATABASE_PASSWORD, secretName: audit-db-observe, key: password}

query:
  enabled: true
  grants:
    issuers:
      - {url: https://console.example.com, audience: audit}
  config:
    deployment: /etc/audit/deployment.yaml
    grants: /etc/audit/grants.yaml
    sink: {url: "http://audit:8080", tokenFile: /var/run/audit/token}
    database:                            # its own role, not the owner
      url: postgres://audit_query@db.example.com:5432/audit?sslmode=verify-full
      passwordEnv: AUDIT_QUERY_DATABASE_PASSWORD
    archive:
      bucket: {name: audit-eu-example-1, region: eu-example-1}
      prefix: audit/app
  secretEnv:
    - {name: AUDIT_QUERY_DATABASE_PASSWORD, secretName: audit-db-reader, key: password}
  tokens:
    - {audience: audit, mountPath: /var/run/audit}   # the file is .../token

jobs:
  clockSync:
    config:
      ntp: ["169.254.169.123"]
      sink: {url: "http://audit:8080", tokenFile: /var/run/audit/token}
    tokens:
      - {audience: audit, mountPath: /var/run/audit}
```

`verify` and `purge` follow the same pattern: `jobs.verify` names the
archive to read, and `jobs.purge` names the purge role's `database` and
`passwordEnv`. The full file, and a stream-mode one
that adds `receiver` and the stream, are `charts/audit/examples/direct.yaml`
and `charts/audit/examples/stream.yaml`. The sink URL names the writer's
Service, which is the release's full name: `audit` here, and
`<release>-audit` under an application's own.

## Migrating from flags and values

The release that introduces the file removes the flags and variables, with no
deprecation period and no aliases. Every one of them is below. A flag with no
variable of its own is listed with its flag only.

### audit-writer

| was | now |
|---|---|
| `--mode`, `AUDIT_MODE` | `mode` |
| `--listen`, `AUDIT_LISTEN` | `listen.address` |
| `--deployment`, `AUDIT_DEPLOYMENT` | `deployment` |
| `--workloads`, `AUDIT_WORKLOADS` | `workloads` |
| `--anonymous-writes` | `anonymousWrites: true` |
| `--catalogues`, `AUDIT_CATALOGUES` | `catalogues` |
| `--replicas`, `AUDIT_REPLICAS` | `replicas` |
| `--database`, `AUDIT_DATABASE` | `database.url`, with the password in `database.passwordEnv` rather than the URL |
| `--bucket`, `AUDIT_BUCKET` | `archive.bucket.name` |
| `--prefix`, `AUDIT_PREFIX` | `archive.prefix` |
| `--region`, `AWS_REGION` | `archive.bucket.region` |
| `--endpoint`, `AUDIT_S3_ENDPOINT`, `AWS_ENDPOINT_URL_S3` | `archive.bucket.endpoint` |
| `--path-style`, `AUDIT_S3_PATH_STYLE` | `archive.bucket.pathStyle` |
| `--lock-mode`, `AUDIT_LOCK_MODE`; `--governance` | `archive.lockMode` (`governance` for the flag) |
| `--kms-key`, `AUDIT_KMS_KEY` | `archive.kmsKey` |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | `archive.bucket.credentialsEnv`, naming the variables. Unset still means the SDK's ambient credentials |
| `--key-provider`, `AUDIT_KEY_PROVIDER` | `keys.provider` |
| `--key-root`, `AUDIT_KEY_ROOT` | `keys.local.rootFile` |
| `--key-dir`, `AUDIT_KEY_DIR` | `keys.local.dir` |
| `--keep-identities` (default true) | `forgetIdentities` (default false): the sense is reversed |
| `--transit-prefix`, `AUDIT_TRANSIT_PREFIX` | `keys.transit.prefix` |
| `--transit-address`, `AUDIT_TRANSIT_ADDRESS`, `BAO_ADDR`, `VAULT_ADDR` | `keys.transit.openbao.address` |
| `--transit-mount`, `AUDIT_TRANSIT_MOUNT` | `keys.transit.openbao.mount` |
| `--transit-namespace`, `AUDIT_TRANSIT_NAMESPACE`, `BAO_NAMESPACE`, `VAULT_NAMESPACE` | `keys.transit.openbao.namespace` |
| `--transit-ca-file`, `AUDIT_TRANSIT_CA_FILE`, `BAO_CACERT`, `VAULT_CACERT` | `keys.transit.openbao.caFile` |
| `--transit-auth-mount`, `--transit-auth-role`, `--transit-jwt-file` (and their `AUDIT_TRANSIT_*`) | `keys.transit.openbao.login.mount`, `.role`, `.jwtFile` |
| `--transit-token-file`, `AUDIT_TRANSIT_TOKEN_FILE` | `keys.transit.openbao.tokenFile` |
| `BAO_TOKEN`, `VAULT_TOKEN` | `keys.transit.openbao.tokenEnv`, naming a variable of your choosing. The two names are no longer read |
| `--stream-url`, `AUDIT_STREAM_URL` | `stream.nats.url` |
| `--stream`, `AUDIT_STREAM` | `stream.name` |
| `--stream-token-file`, `AUDIT_STREAM_TOKEN_FILE` | `stream.nats.tokenFile` |
| `--consumer`, `AUDIT_CONSUMER` | `stream.consumer` |
| `--stream-batch`, `AUDIT_STREAM_BATCH` | `stream.batch` |
| `--stream-ack-wait` | `stream.ackWait` |
| `--roll-interval` | `roll.interval` |
| `--roll-max-records`, `AUDIT_ROLL_MAX_RECORDS` | `roll.maxRecords` |
| `--version`, `AUDIT_VERSION` | removed: the observer version is the build's own. `--version` still prints it |

### audit-query

| was | now |
|---|---|
| `--listen`, `AUDIT_LISTEN` | `listen.address` |
| `--searcher`, `AUDIT_SEARCHER` | `searcher` |
| `--database`, `AUDIT_DATABASE` | `database.url` and `database.passwordEnv` |
| `--grants`, `AUDIT_GRANTS` | `grants` |
| `--deployment`, `AUDIT_DEPLOYMENT` | `deployment` |
| `--sink`, `AUDIT_SINK` | `sink.url` |
| `AUDIT_TOKEN_FILE` | `sink.tokenFile` |
| `--bucket`, `--prefix`, `--endpoint`, `--path-style` and their variables | `archive.bucket.name`, `archive.prefix`, `archive.bucket.endpoint`, `archive.bucket.pathStyle` |
| `AUDIT_REGION`, `AWS_REGION` | `archive.bucket.region` |
| `--exports`, `AUDIT_EXPORTS` | `exports.bucket.name` |
| `--exports-endpoint`, `AUDIT_EXPORTS_ENDPOINT`; `--exports-path-style`, `AUDIT_EXPORTS_PATH_STYLE` | `exports.bucket.endpoint`, `.pathStyle`. The exports bucket no longer inherits the archive's: name them |
| `AUDIT_EXPORTS_ACCESS_KEY_ID`, `AUDIT_EXPORTS_SECRET_ACCESS_KEY`, `AUDIT_EXPORTS_SESSION_TOKEN` | `exports.bucket.credentialsEnv.accessKeyID`, `.secretAccessKey`, naming the variables. A session token is not carried: use ambient credentials |
| `--export-expiry`, `--export-link-valid` | `exports.expiry`, `exports.linkValid` |
| `--key-provider` and the other key flags | `keys`, as for the writer. The provider turns resolve on |
| `--version`, `AUDIT_VERSION` | removed |

### The jobs

| was | now |
|---|---|
| `--deployment`, `AUDIT_DEPLOYMENT` | `deployment` |
| `--bucket`, `--prefix`, `--region`, `--endpoint`, `--path-style`, `--lock-mode` | `archive.bucket.name`, `archive.prefix`, `archive.bucket.region`, `.endpoint`, `.pathStyle`, `archive.lockMode` |
| `--sink`, `AUDIT_SINK` | `sink.url`; `AUDIT_TOKEN_FILE` becomes `sink.tokenFile` |
| `--instance` | removed: the job names itself |
| verify `--profile` (repeatable) | `profiles` |
| verify `--last` | `last` |
| verify `--public-key`, `--lookback`, `--record` | removed with the v1 layout: verify needs no key |
| purge `--database` | `database.url` and `database.passwordEnv` |
| purge `--identifying-after`, `--dedupe-window` | `identifyingAfter`, `dedupeWindow` |
| clock-sync `--ntp` (repeatable), `--max-offset`, `--timeout` | `ntp`, `maxOffset`, `timeout` |
| migrate `--database`, `--reader` | `database`, `reader` (and `writer`, `observe`, `purge`, new) |

`--dry-run` of `audit purge`, and `--from` and `--to` of `audit verify`, remain
on the command line for a person: they are not scheduled work and have no key.

### Chart values

| was | now |
|---|---|
| `bucket`, `prefix`, `region`, `kmsKey`, `lockMode` | `writer.config.archive.bucket.name`, `.prefix`, `.bucket.region`, `.kmsKey`, `.lockMode`; the same under the query service's and each job's `archive` |
| `governance: true` | `archive.lockMode: governance` |
| `endpoint`, `pathStyle` | `archive.bucket.endpoint`, `.pathStyle` |
| `existingSecret` (S3 keys) | `archive.bucket.credentialsEnv` and a `secretEnv` entry per variable |
| `anonymousWrites` | `writer.config.anonymousWrites` |
| `database.url`, `database.existingSecret` | `writer.config.database.url` and `passwordEnv`, with the password in `writer.secretEnv`; the same for `receiver`, `migrate` and `jobs.purge` |
| `database.migrate` | `migrate.enabled`, with `migrate.config.database` |
| `query.database.*` | `query.config.database` with `query.secretEnv`; `query.database.role` is `migrate.config.reader` |
| `query.exports.bucket`, `.expiry`, `.linkValid`, `.endpoint`, `.pathStyle`, `.existingSecret` | `query.config.exports.bucket.name`, `.expiry`, `.linkValid`, `.bucket.endpoint`, `.pathStyle`, `.bucket.credentialsEnv` |
| `query.searcher` | `query.config.searcher` |
| `query.resolve.*` | `query.config.keys`, and `query.keysVolume` for the local provider |
| `keys.provider` | `writer.config.keys.provider` |
| `keys.local.existingSecret` | `writer.secretMounts` and `keys.local.rootFile` |
| `keys.local.persistence` | `keysVolume`, and `keys.local.dir: /var/lib/audit/keys` |
| `keys.transit.prefix` | `keys.transit.prefix` |
| `keys.transit.role`, `openbao.auth.mount`, `.audience`, `.expirationSeconds` | `keys.transit.openbao.login.mount`, `.role`, `.jwtFile`, with a `tokens` entry of that audience and `expirationSeconds` |
| `keys.transit.token.existingSecret`, `.tokenFile` | `openbao.tokenEnv` with `secretEnv`, or `openbao.tokenFile` with `secretMounts` |
| `openbao.address`, `.mount`, `.namespace` | `keys.transit.openbao.address`, `.mount`, `.namespace` |
| `stream.url`, `.name`, `.consumer`, `.batch`, `.ackWait` | `stream.nats.url`, `.name`, `.consumer`, `.batch`, `.ackWait` in `writer.config` and `receiver.config` |
| `stream.token.enabled`, `.audience`, `.expirationSeconds` | a `tokens` entry on `receiver` and `writer`, and `stream.nats.tokenFile` |
| `roll.interval`, `roll.maxRecords` | `writer.config.roll.interval`, `.maxRecords` |
| `workloadIdentity.expirationSeconds` | `expirationSeconds` of each component's `tokens` entry |
| `jobs.verify.publicKey.*`, `.window` | `jobs.verify.config.last` (no key: verify reads the archive only) |
| `jobs.digest.*` | removed with the v1 layout: the digest job read the v0 layout and seals replace it |
| `jobs.purge.identifyingAfter`, `.dedupeWindow` | `jobs.purge.config.identifyingAfter`, `.dedupeWindow` |
| `jobs.clockSync.ntp`, `.maxOffset` | `jobs.clockSync.config.ntp`, `.maxOffset` |
| `telemetry.*` | `OTEL_*` environment, set on the pods by the platform |
| `trust`, `catalogues`, `profiles`, `workloadIdentity`, `extensions`, `networkPolicy`, `mode`, `replicas`, `writer.consumers` | unchanged |

The verify job was one CronJob per profile; it is now one, `<fullname>-verify`,
covering the profiles in its `profiles` or every profile. A query service's
region used to come from `AUDIT_REGION`; it is now `archive.bucket.region`.
S3 static credentials used to arrive as `AWS_ACCESS_KEY_ID` through
`envFrom`; a store that needs them now names the variables in
`credentialsEnv`, and the chart puts only those into the pod.
