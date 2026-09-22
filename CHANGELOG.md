# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). A section
describes the state of the repository at that version, not the history of
edits that got there.

## [0.2.2] - 2026-09-22

One fix, found the moment the first installation's writer started.

### A writer that pseudonymises nobody needs no keys

`keys.provider: none` is the chart's default and the shape
[decision 0013](docs/decisions/0013-no-pseudonymisation-keys-by-default.md)
recommends, and the writer refused to start in it: `a key provider is
required`. A leftover unconditional check sat in front of `GuardKeys`, the
guard that decides this properly, so the guard was unreachable and every
deployment without keys crash-looped whatever its profiles did.

The check is gone. Whether a provider is needed is `GuardKeys` and
`GuardHashes`' decision, from what the composed profiles actually ask for:
a deployment that pseudonymises is still refused by name, and one that
keeps everyone in clear now opens. Both call sites that would use a
provider already refused a nil one with their own message, so nothing
downstream changes.

The test that covered this asserted only the refusal, and passed for the
wrong reason -- its profile pseudonymises, so the message it wanted came
from either check. It now says which, and there is a second test for the
deployment that needs no keys at all.

## [0.2.1] - 2026-09-22

One fix, found installing 0.2.0 for the first time: a release with an index
never finished installing.

### The migration hook brings its own service account

`helm install` of a release with an index never completed. The migration Job
is a `pre-install` hook, and it ran as the writer's service account -- which
the chart creates as an ordinary resource, so it does not exist yet when the
hook runs. The Job was admitted and then never got a Pod (`serviceaccount
"audit" not found`), and the install waited for a hook that could not run.

The Job now has a service account of its own, created as a hook one weight
earlier. It is also the right identity: the migration reads a database URL
from a Secret and talks to Postgres, so it has no business holding the
credentials that write the archive. Nothing a deployment sets changes.

Two things were missing that would have caught it. `charts/audit/examples/`
showed the two shapes a deployment actually installs and nothing rendered
them, while the shapes that were rendered are trial installs with no index --
so the migration hook was never in a golden at all. Both examples are now
rendered into `testdata/golden/`, which also proves the documented files
work. And `testdata/hook-order.py` asserts that every hook that runs a Pod
brings its own account, applied at a lower weight: an ordering fault is not a
render error, so only an install finds it otherwise.

## [0.2.0] - 2026-09-22

One installation per application, and the code to match. The documentation was
rewritten first and is the specification the rest of this version was built
against: what each part holds and never holds, a page per deployment shape with
its diagrams, the decisions behind them, and which framework presets a
deployment actually composes.

Nothing outside this repository pins 0.1.x, which is why the shape could change
this much in one version. Adopters pin this one.

### The chart is instantiated per application

`mode` chooses the shape. In `direct` the chart renders one Deployment that
serves the sink and writes the archive. In `stream` it renders two: a receiver
that serves the sink and publishes, holding neither the bucket nor a key, and
`writer.consumers` writers that read the stream and put the objects. Both come
from one template parameterised by role, so the shapes cannot drift apart. The
Service keeps its name and the receiver keeps the `writer` component label in
both, because it is the address records are written to and that should not move
when a deployment changes shape.

New refusals, each for something the binaries reject or quietly get wrong:
`mode` that is neither shape, `mode: stream` without a stream or without a
database, `extensions.billing.enabled` with no metering profile, and
`extensions.quotas.enabled` without a stream. Both extension toggles exist and
render nothing: what fills them is designed and not yet built, and the toggles
are here so a deployment's values do not change when it lands.

The goldens are now `direct.yaml` and `stream.yaml` rather than `minimal` and
`full`, and `charts/audit/examples/` holds the values an application's chart
sets under its `audit:` key, one file per shape, rendered by the chart's own
tests. The NOTES print the four identities that need rights under the
installation's prefix and what each needs, since that is the part a deployer
has to build outside the chart.

**`profiles` defaults to `security` alone.** It defaulted to `security` and
`history`, and because Helm merges maps a values file naming one profile got
the other as well — a surprise in the setting that decides retention.

`examples/embed` is deleted, and the last comments describing a writer inside
an application are gone. `writer.Open` and `query.New` stay exported, because
the binaries are built on them, and say plainly that they are not a way to
deploy.

### The writer gathers from the stream before it writes

Fetching from a stream returns whatever is there, which under a light load is a
handful of records at a time. Writing each fetch straight through made an
object of each, and an archive of many small objects costs a request to put, a
line in every hour's digest and an entry in every listing, forever.

So a writer consuming a stream accumulates across fetches and writes once a
roll condition is reached: `roll.maxRecords` (5000), the roller's byte limit
(8 MiB), or `roll.interval` (30 seconds). Nothing waits on this but the object.
The records are already durable on the stream, and they stay unacknowledged
until the put, so a writer that dies mid-window leaves them for the next one.

`stream.ackWait` must now exceed `roll.interval` plus the longest a put can
take, and both the chart and the consumer refuse otherwise: a stream that gives
up waiting sooner offers the same records to a second writer, and the day's
objects quietly double. The default rises to two minutes.

Direct mode is unchanged. There is no stream to gather from, every batch is put
before it is acknowledged, and the emitter's own batch size and flush interval
are what decide object count there.

### A keyless deployment is refused a catalogue that hashes

A property a schema annotates for hashing is a pseudonymised property, and it
needs the same keys an identifier does. Until now a deployment running without
a key provider took such a record, failed to hash it and dead-lettered it, one
record at a time, which is something a deployment discovers on the day it
matters rather than the day it was configured.

The writer refuses to start when the catalogues it holds ask for hashing and
no provider is configured, naming the property. The receiver refuses a
registration that arrives later with the same problem, as a validation
problem, so the application does not start against an installation that would
dead-letter its records. `audit validate` lists the hashed properties, so the
application's own CI says it first.

### The tail is asked the case it exists for

The conformance suite indexes a record that happened on an earlier day than
anything already there, and was recorded after the last page was taken, and
requires the tail cursor to deliver it. That is what a tail is for: what
arrives next need not have happened next, and a searcher that ordered the tail
by when things happened would hand a reader a cursor already past the record,
with an empty page and no sign of the gap. Memory and Postgres answer it; the
archive scan refuses `recorded_at` ordering and is held to the refusal.

`indextest.Run` takes the indexer as an explicit argument now rather than
type-asserting the searcher. The read-only Postgres role is an `index.Indexer`
by type and cannot write, so the assertion asked it to index and the suite
failed where nothing was wrong.

Written down with it: a deployment on `query.searcher: s3scan` can search the
trail but cannot follow it, because the archive is laid out by the day things
happened.

### A record the emitter gives up is a log line

Decision 0012 said every dropped record is still a log line. The emitter never
logged anything: it called `OnDropped`, and an application that wired no hook
lost the record silently. A drop with no hook is now written to the
application's log by the emitter itself, with the identifier, action and
reason, through `Options.Logger`.

Decision 0013 described a start-up refusal keyed on the registered catalogues.
What was built refuses on the composed profile instead, because a catalogue can
be registered after start-up and a check on what is registered would be walked
around by arriving late. The record now says so.

### A receiver mode, so stream mode has a front door

`audit-writer --mode receiver` (env `AUDIT_MODE`) serves the sink and
publishes to JetStream, and holds no bucket and no key provider: it refuses
`--bucket` and a key provider rather than quietly being a writer. `--mode
writer` stays the default and is what every installation ran until now.

Until this, nothing published to a stream but a test. An installation that
wanted one had to let the **application** publish, which meant the
application holding the stream's credentials — the thing
[0011](docs/decisions/0011-one-installation-per-service-or-product.md) exists
to prevent.

The receiver stamps each record with the caller its authenticator verified,
and with the moment it took responsibility, before publishing. It has to: a
writer consuming a stream is reading messages, not serving a request, so it
has no caller to verify, and an identity not attached at the front door is one
nothing downstream can recover. A writer told it consumes its own
installation's stream (`writer.Config.FromStream`) therefore keeps a stamp
whose origin hash still describes its record, and stamps afresh one that does
not. On the sink's own port it always stamps, because there a caller that
could keep its own stamp would be choosing the identity it is recorded under.

### Keys are off by default

`keys.provider: none` is the default. Most deployments want it: staff are kept
in clear because that is what accountability is for, and people outside arrive
as identifiers an application already minted, which name nobody without that
application's own database. Encrypting one of those a second time adds a key
to lose and tells a reader of the archive nothing new. `local` and OpenBAO
`transit` stay for a deployment obliged to be able to crypto-shred.

A deployment without keys has to say which it is. New
`external_identifiers_are_opaque` in the deployment document relaxes a
profile's `external: pseudonym` to `clear` — applied where profiles are
composed, so `audit profile explain` shows the treatment that will actually be
used, and says it was relaxed. The writer then holds the deployment to it and
refuses a record whose external identifier looks direct, an address say. A
writer that has neither a provider nor the declaration **refuses to start**,
naming the profile: arriving at clear identifiers in an archive nothing can
edit should take a decision, not an omission.

The `history` preset no longer needs keys either. It omits internal actors
instead of pseudonymising them, so a tenant's administrator sees what was done
and by what kind of person, never by whom — which is what that view should
show anyway. Composed with `security`, the stricter reading still wins.

### Two deliveries, and no file outbox

An action declares `block` or `async`. `block` is unchanged: the call returns
when the receiver has acknowledged durability, and the action fails when it
cannot. `async` is the default, and now keeps what it is given: a bounded
in-memory queue, retried with backoff until the sink acknowledges the batch. A
batch the sink *answers* is never retried — a refusal is recorded where it
happened and repeating it would only repeat the refusal — and a queue that
overflows drops its **oldest** record, counts it and reports it, on the
reasoning that the newest is the one somebody can still act on.

`outbox` and `best_effort` are retired, and refused by name where a catalogue
is loaded, with the replacement in the message. `emit.FileOutbox` and the file
itself are gone, with `Options.Outbox`, `Options.Publish` and the volume that
carried them. New `Options.Retry` paces the retries.
`audit.emit.queue.pending` replaces `audit.emit.outbox.pending`: it counts
everything not yet acknowledged, which is exactly what a process would lose if
it stopped now.

On the wire, `DELIVERY_ASYNC` joins the enum. `DELIVERY_OUTBOX` and
`DELIVERY_BEST_EFFORT` stay in it, deprecated: this package is `v1`, a value
removed is a record nobody can read, and nothing produces them any more.

Two statements are now tested by killing a process outright: a record the
application was told was kept survives, and what was still queued is what is
lost.

A record the queue gives up is written to the application's log by the
emitter itself, with its identifier, action and the reason, when the
application wires no `OnDropped` hook (`Options.Logger`, default
`slog.Default()`). Until now "every dropped record is still a log line" was a
promise the emitter made on the application's behalf.

**Corrected while doing it.** The documentation said an `async` batch was
acknowledged after "the roll that holds it" and that `roll.interval` was the
loss window. It never was: the receiver puts every batch it takes before it
answers, whatever the delivery. The loss window is one flush interval of
records plus the batch in flight, and the pages now say so.

### The registry service is gone

The writer serves `RegisterCatalogue` beside the sink, so an installation is
one Deployment smaller and an application registers its catalogue with the
same address it writes to. `cmd/audit-registry`, its image, the chart's
`registry.*` values, its Deployment, Service, ServiceAccount and network
policy are all removed, and a release now carries three images instead of
four. Nothing about registration itself changed: the same validation, the
same Postgres store, the same copy into the archive, the same coverage report
that warns and never refuses.

Whose catalogue a registration is still comes from the caller's verified
service account and never from the document, and `workloadIdentity.workloads`
is still the only thing that says so: one entry per workload that may
register, naming the source it speaks for. An installation that keeps an index
and verifies callers must fill it in, and the chart now refuses to render when
it is empty rather than letting every registration be refused at run time.

- **Three decisions.**
  [0011](docs/decisions/0011-one-installation-per-service-or-product.md): one
  installation per service or product, in that application's namespace,
  rendered by its own chart, with the receiver serving `RegisterCatalogue`
  and no registry service.
  [0012](docs/decisions/0012-two-deliveries-and-a-durable-ack.md): two
  deliveries, `block` and `async`, the file outbox removed, and the
  receiver's acknowledgement always meaning durable.
  [0013](docs/decisions/0013-no-pseudonymisation-keys-by-default.md):
  `keys.provider: none` by default, with `external_identifiers_are_opaque`
  declared by the deployment. 0004's delivery modes and 0010's preference
  for a managed provider are superseded.
- **A deployment page per shape**, each with a deployment diagram and the
  sequence of one record: [direct](docs/deployment/direct.md) for an
  internal service, [stream](docs/deployment/stream.md) for a product, and
  the two extensions, [billing](docs/deployment/extensions/billing.md) and
  [usage quotas](docs/deployment/extensions/quotas.md), with the five slots
  each. There is no embedded shape: a writer inside the application is no
  longer a way to deploy this, and `docs/guides/embed.md` is gone.
- **The README is a map**: what it is, the two shapes, the two extensions,
  a row per kind of reader, and the status table.
- **A presets policy**
  ([docs/operations/presets-policy.md](docs/operations/presets-policy.md)):
  compose `security` always, `billing-nl` where the installation meters,
  and leave `dora`, `pci-dss`, `evidence-etsi` and `nen-7513` as files until
  a contract asks. `history` is reworked before anyone composes it.
- **`docs/research/` is removed** from the tree: it read as design and was
  not. The decisions that used it quote what they needed, and the surveys
  remain in the repository's history. `docs/design/pipeline.md` is folded
  into the architecture page, `docs/design/extension-points.md` moves to
  `docs/reference/`, and `docs/design/viewer.md` becomes
  `docs/design/audit-page.md` with the standalone console dropped.

## [0.1.1] - 2026-09-21

The images publish where the chart looks for them. 0.1.0's release
failed: the four ko images carried a `repositories:` list each, and the
release workflow's `KO_DOCKER_REPO` wins over it, so ko tried to publish
`ghcr.io/truvity` itself and the registry answered 400. They now take
their name from the command's import path under one repository path, as
access-roster's two images do, and the chart's defaults name the same
four: `ghcr.io/truvity/audit/{audit,audit-writer,audit-query,audit-registry}`.

## [0.1.0] - 2026-09-21

The foundation, released so that consumers have something to pin. Every
contract, binary and chart below is at its first published version, and
nothing outside this repository depends on it yet — which is the point of
cutting it now rather than later: a version that exists can be adopted a
piece at a time.

- Contracts in `proto/audit/v1/`: record, sink, registry, query. Generated
  Go and TypeScript committed under `gen/` and `ts/src/gen`.
- `record`: the canonical form (RFC 8785 over the protobuf JSON mapping, no
  floating point), identifiers, bounds with a published truncation order,
  and the negative list.
- `preset`: the seven framework presets, loaded and composed into profiles
  with default-deny field lists.
- `catalogue`: catalogue loading, extension-schema annotations, composed
  validation of a record, message-template argument checks, and the category
  cross-check against a deployment's profiles.
- `emit`: the emitter, with block, outbox and best-effort delivery, request
  provenance middleware and a durable file outbox.
- `sink`: the write contract with memory, Connect and JetStream transports.
- `keys`: pseudonyms per tenant and purpose, never rotated; a local signer.
- `store`: the object store interface, the S3 bucket, and a memory store for
  tests that can be tampered with on purpose.
- `internal/writer`: the split into per-profile copies, rolling into locked
  objects, deduplication, the dead letter, and the writer's own account of
  itself, emitted into itself over the in-process sink.
- `internal/digest`: the signed digest chain and its verifier.
- `index`: the index contract and the rows it holds, the facet deltas a record
  produces, and an in-memory implementation. Indexing is idempotent by
  `(profile, id)` and counting has no call of its own, because only the
  transaction that inserted a row can tell a re-delivery from a new record.
- Export: `audit.export.requested` before anything is read, `audit.export.completed`
  with the count and the form. An export is a copy of records made to be taken
  away, so it lives outside every profile's prefix, expires, carries that expiry
  as the file's own retention, and is collected only by whoever asked for it.
  `store.Presigner` is an optional capability rather than part of `store.Store`:
  most of what an archive holds must not be reachable by a URL anybody can hold.
- `audit-query`, the read service behind Connect, over the Postgres index or
  the object-storage scan. Cursors are opaque and bound to the question they
  came from — narrowing included — so one replayed against a different filter
  or a wider grant is refused rather than resumed from an ordering that no
  longer exists. A denial reaches the client as a denial and a bad cursor as a
  bad argument, because a client told "server fault" retries forever.
- `internal/query`: the read service. It compiles a closed request, narrows it
  to the caller's grant as one more filter term, asks a searcher, and records
  the read — `audit.search`, `audit.facets`, `audit.get`, naming the caller and
  the rule that allowed them. A refused read is recorded too, and a record the
  grant does not cover is reported as absent rather than as forbidden, because
  the two are the same answer to someone who should not know it exists.
- `auth`: the `Authenticator` and `Authorizer` seams a deployment plugs into,
  with a declarative authorizer. A grant's zero value grants nothing, every
  tenant is said out loud rather than meant by a nil list, a refusal names
  which of profile, operation or tenant it failed, and `resolve` — undoing a
  pseudonym — is never implied by permission to read.
- `index/s3scan`: a searcher with no index at all, for a deployment too small to
  run a database — and the implementation that cannot cheat, since one backed by
  a table can quietly grow a capability the interface never promised. It refuses
  facets and every ordering but occurred time, with the reason, rather than
  answering something narrower than was asked. A scan is bounded by a budget and
  by a horizon.
- `index`: a memory `Searcher` beside the Postgres one, asked the same
  questions — one implementation is a description of its own habits with an
  interface drawn around it.
- `index`: the `Searcher` contract — a closed query, keyset cursors, facets,
  provenance on a single record — and the Postgres implementation of it. The
  grant is one more term in the query rather than a layer above it, so there is
  no path to a row outside it. Paging is keyset, so a deep page costs what a
  shallow one does and a record appended meanwhile cannot shift a page already
  handed out. The last page still carries its boundary, because a tail keeps
  polling it.
- `index/postgres`: the default index and the shared deduplication table, with
  a checked-in schema, monthly partitions created on demand, and row-level
  security by tenant.
- Deduplication asks before the write and marks after it, so that a crash
  between the two costs a duplicate object rather than a lost record.
- `audit validate`, `audit profile explain`, `audit check-emitters`,
  `audit verify`, `audit replay`, `audit migrate`, `audit reindex`,
  `audit digest`, `audit purge`, `audit clock-sync`, `audit hold`,
  `audit key destroy`.
- `audit key destroy` is erasure: the copies stay and their pseudonyms can
  never be recomputed. It refuses while a legal hold covers the tenant, and
  refuses without a writer, because an erasure the trail does not record is one
  nobody can prove was lawful.
- Chart: the query service (`query.enabled`), with its own database role —
  refused when it names the writer's credentials, since an owner bypasses the
  tenant policies — the grants file, exports, and resolve through the
  writer's key directory or its own transit token. The registry and the query
  service get their own service accounts. The writer's pods now carry
  `app.kubernetes.io/component: writer`: its Service selected on the release's
  labels alone and so also routed sink traffic to the registry's pods. That
  selector is immutable, so an install from an earlier commit deletes the
  writer Deployment before upgrading.
- `keys.Transit`: pseudonymisation keys in an OpenBAO (or Vault) transit
  engine, one key per purpose and tenant named `<prefix>.<purpose>.<tenant>`
  so the engine's policy scopes each role to its purposes. A pseudonym is the
  engine's HMAC and a sealed identifier its encryption, both pinned to the
  key's first version, so the key never leaves the engine and every replica
  agrees without a shared directory. Destroy trims the first version and
  leaves the key as its own erasure marker. It and the transit digest signer
  sign in with the pod's projected service-account token on a JWT auth mount
  (`keys.JWTLogin`), signing in again as the lease runs out; they address an
  OpenBAO namespace and trust a private chain's bundle. `--key-provider
  local|transit` and the shared `--transit-*` flags on the writer, the query
  service, `audit key destroy` and `audit digest`; the chart's `openbao`,
  `keys.provider: transit`, a role per component, and `trust.configMap` for
  OpenBAO and Postgres alike.
- Template arguments use underscores: `{targets_0_id}`, `{data_items}`,
  `{actor_id}`. Dotted names (`{targets.0.id}`) are not valid ICU, so no
  standard renderer could fill them; the validator now refuses them with the
  underscore spelling, and refuses data properties that would collide as
  arguments. The viewer renders templates as written.
- `audit conformance --query <url> --profile <p>`: holds a running query
  service to the search contract from outside — paging, order, get against
  search, filters, refusals, and optionally digest coverage — over the records
  it already holds, reading only. `just conformance` runs the whole suite with
  Postgres, LocalStack and an OpenBAO dev server.
- A record that is not there is `not_found` from every searcher
  (`index.ErrNotFound`, now in the searchers' conformance suite). Before, the
  searchers returned a plain error, which the service reports as `unavailable`
  — telling a client to retry for a record that does not exist. Found by the
  first conformance run. `id` predicates take whole UUIDs; the Postgres index
  failed on anything else.
- `@truvity/audit` (built from `ts/`):
  - the query client and the typed contract;
  - the qualifier box compiled to the typed filter;
  - records rendered as their catalogues' sentences, through FormatJS;
  - `@truvity/audit/react`: `AuditProvider`, `useSearch`, `useTail`,
    `useFacets`, `useRecord` and a default MUI `AuditView` for an
    application's console.

  Catalogue templates name arguments by path (`{targets.0.id}`), which ICU
  refuses as written. The renderer finds them with a port of the validator's
  scanner, and both are held to `testdata/messages.json`. `audit messages`
  prints a catalogue's sentences as JSON for the viewer. The generated
  TypeScript now imports with `.js`, which Node's ESM resolution needs.
- `writer` and `query`: the writer and the query service as public libraries,
  so an application can embed its own trail — its emitter writing into the
  writer in process, its console reading through the query API behind its own
  sign-in (`auth.AuthenticatorFunc`). Both binaries are built on them. The
  deployment document is `preset.ParseDeployment`. `examples/embed` does the
  whole round trip with public imports only, and a test holds it to that.
- Retention addenda: an action that `extends` the records a data property
  names lengthens the lock on the objects holding them — a renewal on the
  issuance, a credential on the identity proofing it relied on — to its own
  expiry plus the profile's years, after it is durable and never shorter.
  `store.Store.ExtendRetention` (S3 `PutObjectRetention`; the memory store
  refuses a shorter date as a compliance bucket does). Each extension and each
  failure is an `audit.retention.extended` record; a failure never fails the
  batch and is counted as `audit.writer.retention.not_extended`.
- Legal holds: `audit hold place|release|list`, hold records in the archive
  under the same lock and append-only like everything else, and the writer
  setting the hold on objects written under a held prefix — an object held only
  by a later sweep was deletable in between.
- The digest and verify jobs keep an account of themselves, as the writer
  does: `audit.digest.written` per sealed window, `audit.digest.verified` and
  `audit.digest.failed` per window checked. A chain never sealed and one sealed
  over a quiet hour are otherwise identical in the archive, and a verification
  that never ran looks exactly like one that found nothing wrong.
- An uncovered object now names the window that should have covered it, so a
  failure points at an hour rather than at the whole profile.
- `internal/clock`: an SNTP client with no dependencies, so that the daily
  check ETSI EN 319 401 §7.10 asks for is recorded as an audit event rather
  than assumed. It measures and records; it never sets the clock.
- A digest covers every tenant of its profile. The chain is keyed by profile
  and the archive puts the tenant between the profile and the date, so a
  builder given the profile prefix covered nothing and one given a tenant's
  prefix covered one tenant. `store.Store` gained `Prefixes` to ask which
  tenants exist without walking every object.
- `s3store.List` pages to the end when asked for everything. It took S3's
  first thousand keys for the whole, which the verifier, the reindex, the
  replay and the digest's own chain-linking all relied on; every walk of the
  archive now goes tenant by tenant and day by day through `store.WalkDays`,
  and the S3 test double pages, sorts and groups as S3 does so that the next
  listing that stops early is caught here.
- `internal/s3test`: the archive walks run against a real S3 in CI, which is
  what would have caught the two bugs a memory store hid — a digest covering one
  tenant, and a listing stopping at the first thousand keys. It takes an
  endpoint rather than a product, so which S3 answers it is a variable. The
  image is pinned by digest to the community line: LocalStack's `latest` and
  `stable` now resolve to a licensed build that exits without a token, which in
  a public repository would fail every fork's CI.
- CI, as the estate's other public repositories have it: each `just` recipe is
  its own job, with the race detector, the TypeScript drift check and a
  Postgres-backed run as jobs of their own, and a `leak-canary` recipe that
  enforces mechanically what a public repository may not contain.
- `audit-registry`, the catalogue registry as a service: it validates a
  document with the same toolchain that validates it in the application's own
  tests, refuses a source registering another's catalogue, and refuses a
  catalogue that would leave a profile's required categories uncovered.
  Registering the same version twice is how a deployment rolls; registering a
  different document under the same version is refused, because a version says
  what records already written under it mean.
- `emit.Register` for an application to register at start-up and not start if
  the deployment refuses its catalogue.
- `audit-writer`, the writer as a service behind Connect and, given
  `--stream-url`, behind a durable JetStream consumer shared by every replica.
  A batch is acknowledged only once its records are in the archive, so a writer
  that cannot write leaves them for the redelivery; `MaxDeliver` is unlimited,
  because a record must not fall out of the stream for having been offered a
  few times, and nothing loops forever on a bad record — one the writer cannot
  process is accepted and dead-lettered.
- `charts/audit`, write side: the writer, the catalogue registry, a pre-upgrade
  hook applying the index schema, and the digest, verify, purge and clock-sync
  jobs. It refuses to
  render twelve configurations the binaries reject at start-up or accept and
  get quietly wrong — several replicas without a shared deduplication table,
  several replicas sharing a key directory none of them can both write,
  a disposable key directory, governance mode.
- Decisions 0001 to 0009 accepted.
- Design, research, reference and operations documents.
