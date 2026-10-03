# Changelog

All notable changes to this project are documented here, one `## vX.Y.Z`
heading per released tag, newest first. A section describes the state of the
repository at that version, not the history of edits that got there.

## Unreleased

- Traces. The writer, the receiver and the query service serve HTTP server and Connect spans, `sink.Client` and the NATS and SQS publishers add client and producer spans, and the W3C `traceparent` crosses a queue in the message (NATS headers, SQS message attributes) so a consumer continues the trace and links the other messages of its batch. A tracer provider exists only when `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set; without it nothing is exported and nothing fails. The sampler is the standard `OTEL_TRACES_SAMPLER` (a parent-based ratio of 0.1 when unset). Spans leave the process through an allowlist: only action, outcome, tenant id, durability, delivery, counts, transport and the shape of the request survive, never an actor, a subject, a client address or record data, and events, status text and link attributes are removed. `audit-query` now starts telemetry too.
- Metrics: `audit.sink.records.acknowledged` (by durability and transport), `audit.sink.records.rejected`, `audit.sink.write.duration` (by transport and outcome), `audit.sink.consume.failures`, `audit.writer.index.lag`, and `audit.digest.age`, observed by the writer from the archive rather than pushed by the digest job. No series is labelled by tenant.
- The chart renders alert rules and dashboards. `renders: alerts` renders only a `VMRule` (or `PrometheusRule`) of seven rules, with `alerts.ruleLabels` for routing labels such as `k8s_cluster_name`, for a second install beside the write path; `renders: dashboards` renders only the Grafana sidecar ConfigMaps of one audit overview that passes truvity/observability's dashboard lint. The default, `renders: app`, renders exactly what it did. See [telemetry](docs/operations/telemetry.md), which has each alert's threshold and runbook entry. `just telemetry`, a CI recipe, runs the rule unit tests on `vmalert-tool` and the dashboard lint, both at pinned releases.
- The acknowledgement of `SinkService.Write` says how durable the batch is. `WriteResponse` gains `durability` (`DURABILITY_LOGGED`, `QUEUED`, `ARCHIVED`, ordered, `UNSPECIFIED` for a hop that did not say), added without renumbering; `sink.Result.Durability` carries it and the Connect client and handler pass it through. The writer reports `Archived`, `natssink.Publisher` and `sqssink.Publisher` report `Queued` after every acknowledgement, `sink.Memory` and `logsink` report `Logged`, `sink.Discard` reports nothing, and `sink.Receiver` reports what its next hop does. See [ADR 0017](docs/decisions/0017-sink-durability-and-transports.md).
- `sink.Require(s, min)` refuses, at start-up, a chain whose strongest durability (`Guarantees()`) is below `min`, and `sink.Guard(s, min)` also fails any write whose acknowledgement is weaker at run time. `sink.ParseDurability` reads the spelling a configuration will use; `sink.Client.Expecting` says what a remote service is configured to give.
- A `log` sink, `sink/logsink`, writes one JSON line per record, `MESSAGE` and `AUDIT_RECORD`, and reports `Logged`.
- An `sqs` sink, `sink/sqssink`: a publisher that sends records with `SendMessageBatch`, deduplicates by record id on a FIFO queue and carries it as an attribute otherwise, reports `Queued` only once SQS has taken every message, refuses what SQS refuses for the message's own sake and sends again what it failed for its own; and a consumer that writes each receive to a target and deletes only what the target took. `just test-s3` and the `s3` CI job now run it against LocalStack's SQS.
- `sink/sinktest.Run` is a conformance suite for any sink: declared and reported durability, a batch taken whole, an empty request, a cancelled context, a refusal surfaced, a repeated record kept once where the sink claims it. It runs against `sink.Memory`, the Connect client and handler, `natssink`, `logsink` and `sqssink`.

- `require:` is read from the configuration. `audit-writer` takes `require` (`logged`, `queued` or `archived`), wraps its chain in `sink.Guard` and refuses to start when the chain can never give it; the default is the strongest the mode can give, `archived` for a writer and `queued` for a receiver, which holds no archive and so cannot promise `archived` (the schema refuses it there). `audit-query` and the `digest`, `verify` and `clock-sync` jobs take `require` and `sink.expect` (what the writer they record through is configured to give, passed to `sink.Client.Expecting`); `require` without an `expect` at least as strong is a start-up error, and unset checks nothing, as before.
- The receiver's onward transport is selectable: `forward: {nats: ...}`, `{sqs: {queueUrl, region, fifo}}` or `{log: {}}`, exactly one. A writer's optional consumer is `consume: {nats: ...}` or `{sqs: {queueUrl, region, fifo, batch, visibility}}`. `stream` stays as the NATS shorthand (`forward.nats` in a receiver, `consume.nats` in a writer), so an existing NATS file and its rendered manifests are unchanged. The `log` sink is allowed only with `require: logged`. SQS takes no credentials from the file: it uses the SDK's ambient ones, which on Kubernetes is the pod's workload identity. The chart's stream-mode check accepts `writer.config.consume` as well as `stream`; `charts/audit/examples/sqs.yaml` is an SQS install with its golden, and `tests/invalid/audit/` refuses `log` with `require: queued`, two transports in `forward`, and `require: archived` on a receiver.

### Breaking: one validated configuration file, in place of flags and environment

`audit-writer` and `audit-query` are configured by one YAML file, `--config <file>`, and by nothing else. The file is validated against a JSON Schema before anything starts, so an unknown key, a missing required key or a value of the wrong type is a start-up error that names the path to it, and a flag misspelt in a chart is a failed `helm template` rather than a container that ignores it. The schemas are committed as `schemas/config/audit-writer.schema.json` and `audit-query.schema.json` (generated by `just config-schemas`, checked by `just drift`, served with the other schemas on GitHub Pages), and use the shared shapes of [truvity/policy](https://github.com/truvity/policy) and its loader. See [ADR 0021](docs/decisions/0021-one-validated-configuration-file.md) and [the configuration reference](docs/reference/configuration.md), which lists every key.

- **Secrets are the one thing the environment adds, and only the ones the file names.** A field ending in `Env` (`database.passwordEnv`, `bucket.credentialsEnv`, `openbao.tokenEnv`) holds the name of the variable; the process reads exactly those. A password inside a database URL, or a key such as `password`, is refused. A file's tokens, roots and keys are named by path, never held.
- **Telemetry is only the standard `OTEL_*` variables.** `telemetry.otlpEndpoint` is gone from the chart; nothing about telemetry is in a configuration file.
- **The flags and `AUDIT_*` variables of `audit-writer` and `audit-query` are removed**, with no deprecation period: `--bucket`, `--database`, `--listen`, `--mode`, `--stream-url`, `--key-provider`, `--workloads`, `--anonymous-writes`, `--grants`, `--sink`, `--exports`, `AUDIT_BUCKET`, `AUDIT_DATABASE`, `AUDIT_REGION`, `AUDIT_EXPORTS_*`, `BAO_TOKEN` as a fallback, and the rest. Only `--config`, `--version` and `--help` remain. The build's version comes from the release, not from `--version`/`AUDIT_VERSION`.
- **The scheduled jobs of `audit`** (`digest`, `verify`, `purge`, `clock-sync`, `migrate`) take `--config <file>` in place of every other flag (only `--json` may accompany it), each validated against its own schema (`audit-digest`, `audit-verify`, `audit-purge`, `audit-clock-sync`, `audit-migrate`). The interactive flags of `audit` stay, for a person at a keyboard. `verify` in a file takes `profiles` (default: every profile) and runs them in one job.
- **Exports are explicit.** The exports bucket no longer inherits the archive's endpoint and path style, and carries its own `credentialsEnv`; `ca` points a bucket at a private certificate authority; Postgres pool size is `database.maxConnections`.
- **The chart passes `config` through.** Each component (`writer`, `receiver`, `query`, `migrate`, `jobs.digest`, `jobs.verify`, `jobs.purge`, `jobs.clockSync`) has a `config:` block that is rendered as it stands into a ConfigMap, mounted at `/etc/audit/config.yaml`, and validated by `values.schema.json` against the schema its binary uses (the chart's schema is generated and embeds them). Platform values stay values: `replicas`, `mode`, images, service accounts, `resources`, and, per component, `secretEnv` (a Secret's key into the variable a config names), `secretMounts` and `tokens` (a projected service-account token). The flag-shaped values are removed, not aliased. A Go test holds every rendered ConfigMap to the values' `config` and to its binary's schema, and `tests/invalid/audit/` holds the refused configurations as overlay files. The verify job is one CronJob, `<release>-verify`, where it was one per profile.
- **Where the chart's old refusals live.** What spans two components' configs stays a chart refusal (the digest job or resolve signing in as the writer, the query service connecting as the writer's database role, the local key directory with no volume unless `keysVolume.ephemeralIsAcceptable`, replica counts, workload identity, stream mode's shape); what is local to one config is in its schema or the binary's load (exactly one digest signer, exactly one way to sign in to OpenBAO, `ntp` with at least one reference, the lock mode, the stream's `ackWait`). `tests/invalid/audit/` holds a fixture for each.
- **The packaged chart pins its images by digest.** `values.yaml` declares `images.audit-writer`, `images.audit-query` and `images.audit` (registry, repository, tag, digest), which `helmctl package --manifest` fills in from what the build published, as the shared pipeline expects; a pinned image wins over `image.*`, and a chart installed from a directory, with none, uses `image.*` as before. Until now the kind tier installed the released images beside the checkout's chart, so it could not tell a chart that disagreed with the binaries it was about to ship.

#### Migrating values

The snippet shows the shape of the change for a direct-mode installation with an index; `docs/reference/configuration.md` has the full mapping, for the flags as well.

```yaml
# before
bucket: audit-archive
prefix: app
region: eu-example-1
kmsKey: alias/audit
lockMode: governance
anonymousWrites: true
database:
  existingSecret: audit-db        # a Secret whose key `url` held the whole URL
keys:
  provider: none
jobs:
  digest:
    kmsKey: alias/audit-digest
```

```yaml
# after
writer:
  config:
    deployment: /etc/audit/deployment.yaml   # the chart renders it from `profiles`
    anonymousWrites: true
    archive:
      bucket:
        name: audit-archive
        region: eu-example-1
      prefix: app
      lockMode: governance
      kmsKey: alias/audit
    database:
      url: postgres://audit@db.example.com:5432/audit?sslmode=verify-full   # no password
      passwordEnv: AUDIT_DATABASE_PASSWORD
  secretEnv:
    - name: AUDIT_DATABASE_PASSWORD
      secretName: audit-db        # now holding the password under `password`
      key: password
migrate:
  enabled: true                   # was implied by `database`
  config:
    database: { url: postgres://audit@db.example.com:5432/audit?sslmode=verify-full, passwordEnv: AUDIT_DATABASE_PASSWORD }
  secretEnv:
    - { name: AUDIT_DATABASE_PASSWORD, secretName: audit-db, key: password }
jobs:
  digest:
    config:
      deployment: /etc/audit/deployment.yaml
      archive: { bucket: { name: audit-archive, region: eu-example-1 }, prefix: app, lockMode: governance }
      sink: { url: http://audit:8080 }       # the writer's Service
      signer: { kmsKey: alias/audit-digest }
```

## v0.5.3

- The chart deletes the migrate hook's resources once they succeed. Its Job, ServiceAccount and ConfigMap kept only `before-hook-creation`, so after a successful sync they lingered in the cluster and showed in ArgoCD as resources requiring pruning. They now carry `hook-succeeded` as well; a hook that failed is kept for debugging.

## v0.5.2

- A writer whose stream consumer has stopped no longer goes on answering its health check. `natssink.Consumer.Run` returns only when the connection is closed for good or the request is invalid, and `audit-writer` logged "the stream consumer stopped" and carried on with no consumer, so Kubernetes never restarted the pod and records piled up in the stream unread. `/healthz` now answers 503 once the consumer has stopped without being asked to, and the chart's liveness probe, which already reads `/healthz`, restarts the container. A stop asked for by SIGTERM or a cancelled context is an orderly shutdown and leaves the check passing.

## v0.5.1

- The writer no longer warns `no configured profile keeps these` for an action whose catalogue names a profile this deployment does not configure, when another profile it names does keep the records. `audit.catalogue.registered` and `audit.digest.written` name `evidence` beside `security` for trust-service deployments, so every other deployment logged a warning about records it was in fact keeping. That case is now a debug line naming the profiles that keep the action and the ones not configured; the warning is for an action none of whose profiles is configured, whose records are dead-lettered, and it says so.

## v0.5.0

- Schema `$id`s move from `https://schemas.truvity.com/audit/v1/...` to `https://truvity.github.io/audit/schemas/v1/...`, and `.github/workflows/pages.yaml` serves the files there so an `$id` resolves. The catalogue loader accepts the old identifiers as aliases of the new ones (`catalogue.CanonicalID`) and the common catalogue keeps version 1.0.0, so archived catalogues and schemas keep validating; everything newly generated uses the new base. See [ADR 0015](docs/decisions/0015-schema-ids-on-github-pages.md).
- A reconnect to the stream no longer fails a publish. The client fails every acknowledgement outstanding when its connection drops, and `natssink.Publisher.Write` returned that `nats: server is disconnected` to its caller — for a `block` record, the action it recorded. Records whose publish failed to a reconnect are now sent again once the connection is back, within the same timeout, under the same `Nats-Msg-Id`, so the stream's duplicate window absorbs any that had landed. A refusal from the stream is still returned at once.
- `natssink.Consumer.Run` backs off between failed pulls, from 100ms doubling to 5s and back to 100ms after a pull that succeeds, where it used to ask again at once: a stream electing a leader answered thousands of pulls a second with `no responders`, each a log line. A failed fetch is now reported and retried rather than ending the run; only a closed connection or an invalid request ends it.
- The receiver and the writers reopen their stream connection 30 seconds before the projected token they presented expires, reading the `exp` claim unverified, instead of waiting for the broker to expire the session. Every message the NATS client has goes through slog: `authentication expired` is INFO, and the disconnect that follows it or a planned reconnect is INFO; the client's bare stderr line for asynchronous errors is gone. See [the stream](docs/deployment/stream.md#authenticating-to-the-stream).
- `.github/policy-conformance.yaml` exempts `internal/s3test` from the contract's C13 `region` check: it is a test-only helper.

## v0.4.0

- `charts/audit` gains `values.schema.json`; the schema surfaced and fixed a mis-indented test fixture that had silently dropped a NetworkPolicy from a golden render.
- `vuln` is no longer part of `check`; `.github/workflows/security.yaml` runs it on its own so a new CVE cannot turn the gate red. `renovate.json` extends the shared preset.
- `Chart.yaml` commits `0.0.0`; the release stamps the version. Examples use `eu-example-1`; the live-S3 test helpers keep the CI account's real region.
- README gains `Consumers`, `Neighbours` and `Releasing`; CHANGELOG headings follow `## vX.Y.Z`; ci-workflows pins unified at v3.13.1.

## v0.3.1

### The archive writes to a store that is not AWS, whatever the object carries

Every record object is zstd-encoded, and on Cloudflare R2 not one of them
could be written: `403 SignatureDoesNotMatch`, while the uncompressed
schema and profile objects beside them wrote fine, which made it look like
anything but a signing problem. The cause is that the store asked the SDK
for a checksum ALGORITHM, leaving the SDK to choose how to send it, and for
an object that also carries a Content-Encoding it chooses the aws-chunked
trailer -- which AWS accepts and R2 signs differently. The store now
computes the SHA-256 itself and sends the VALUE, so the choice is no longer
the SDK's to make per store. The object carries the same checksum it always
did, and nothing changes on AWS.

Measured against a real bucket: algorithm plus encoding fails, either alone
succeeds, the precomputed value succeeds with both.
`internal/s3test` gains the check, against a bucket the environment names
(`AUDIT_S3_COMPATIBLE_BUCKET`, `AUDIT_S3_COMPATIBLE_ENDPOINT`) because no
emulator reproduces it -- an emulator accepts what the SDK sends.

## v0.3.0

### The receiver and the writers identify themselves to the stream

`audit-writer` connected to the broker with no credentials and had no way
to carry any, so a deployment whose broker verifies who connects could not
run stream mode at all. It now takes `--stream-token-file`
(`AUDIT_STREAM_TOKEN_FILE`): a file whose contents both ends of the stream
present as their NATS token, read afresh on every connect because a
projected service-account token rotates, and a refusal no longer ends the
client's reconnecting -- the next attempt reads the file again. The option
list is one helper shared by the receiver and the writer, so the two cannot
drift. Empty, the flag changes nothing.

The chart projects that token under `stream.token`: `enabled` (off, so an
existing values file renders exactly as it did), `audience` (`nats`) and
`expirationSeconds` (3600), mounted into every pod that reaches the stream
and only when one is configured. The broker's auth callout -- the thing
that reviews the token and maps the namespace to an account -- is the
deployment's, and the stream page says what it must accept. The chart's
network policy is ingress-only and is unchanged: it never governed what
the writer may reach.

### The archive runs on any S3-compatible store

The store was AWS by omission: the binaries built an S3 client with no
endpoint, and the chart had no way to hand one credentials that were not
the pod's own identity. Every command that opens the archive now takes
`--endpoint` (`AUDIT_S3_ENDPOINT`; the SDK's `AWS_ENDPOINT_URL_S3` works
too) and `--path-style` (`AUDIT_S3_PATH_STYLE`), and with an endpoint set
the SDK's default CRC32 request checksum -- which AWS answers and other
stores may refuse -- is sent only where an operation requires one. The
SHA-256 the archive names on every put is unchanged. With no endpoint the
client is exactly what it was.

The chart gains `endpoint`, `pathStyle` and `existingSecret` beside
`bucket`, all inert by default, and the same three under `query.exports`
for an exports bucket on a store of its own. `existingSecret` reaches every
container that touches the archive through `envFrom`; the exports' Secret
reaches the one query process as `AUDIT_EXPORTS_*`, a second identity
beside the archive's. With the defaults the chart renders what it rendered
before, apart from what the next section adds.

### The lock is demanded where a framework demands it

[0014](docs/decisions/0014-lock-modes-and-store-tiers.md). Object Lock was
mandatory, which made two things one: the Object Lock API, which most
S3-compatible stores lack, and the reading that every framework demands
WORM storage, which the presets' own citations do not support. Now:

- A preset's `object_lock_mode` is the LEAST lock its framework demands,
  and may be `none`. `pci-dss`, `nen-7513`, `dora` and `evidence-etsi`
  demand `compliance`; `security`, `history` and `billing-nl` demand
  `none`, and each carries a one-line `note` saying why. Composition takes
  the strictest: none < governance < compliance.
- The writer's `--governance` is deprecated in favour of `--lock-mode
  compliance|governance|none` (`AUDIT_LOCK_MODE`), which every command that
  writes the archive takes: the digest and verify jobs write into the same
  store. The chart's `governance` is deprecated in favour of `lockMode`.
- The writer, the digest job and the verify job REFUSE TO START when a
  composed profile demands a stricter lock than the deployment writes with,
  naming the profile and both modes. The chart cannot make that refusal --
  the presets' readings live in the binaries -- and says so.
- On a store with no lock, `ExtendRetention` and `SetLegalHold` answer
  `store.ErrNotLockable`. An addendum that could not lengthen a lock is
  recorded in the trail as before; `audit hold place` is refused and records
  the attempt.
- `audit verify --deployment <file>` reports an object with no lock as
  `unlocked` under a profile that demands none, and `INVALID` under one
  that does. The chart's verify job now passes the deployment. Without it,
  nothing is said about locks.
- The exports store and the S3 harness use the same lock-mode option, and
  the harness runs the archive round trip on a locked and an unlocked
  bucket.

The `record` tier of [0003](docs/decisions/0003-s3-object-lock-as-the-record.md)
is unchanged. The `attested` tier -- the chain under a managed key, no lock --
lists its compensating controls in the decision and the
[S3 guide](docs/operations/s3-guide.md#the-attested-tier): a managed
signing key required rather than recommended, a shorter digest interval, no
delete permission anywhere, and an administrative no-delete rule where the
store has one.

`s3store.Options` loses `Governance` and `Unlocked` for `Lock`, and gains
`Endpoint` and `PathStyle`; `cli.Archive` and `cli.ExportStore` are replaced
by `cli.ArchiveFlags` and `cli.ExportFlags`.

## v0.2.6

### The writer takes no caller's word for the shape of a record, its own included

The first adopter's registration produced an object keyed under
`year=1970`: the writer's own `audit.catalogue.registered` record, built
by hand, carried no `occurred_at`, and the write path never ran
`record.Check` -- only emitters did. The object is locked under its
profile's retention like any other, outside every digest window, and stays.

The registration record now carries the time it happened, and `Write`
checks every record it takes and dead-letters one that fails, before it
is keyed. A test holds the writer's own record to the check every
emitter's record passes, and a record with no time is dead-lettered with
the reason, never written.

## v0.2.5

### An instrumented emitter no longer drops in silence

`emit.Instrument` always returned a non-nil drop hook -- it counts, then
calls the application's hook if there is one. `emit.New` installs its own
"a drop is never silent" logger only when the hook is nil, so the wrapper
defeated it: every deployment that followed the guide and called
`Instrument` without a hook of its own dropped records with nothing but a
metric to show for it. Found reviewing the first adopter, which wires no
hook and whose runbook promised a log line.

The wrapper now carries the default itself: counted, then told, on
`slog.Default()` when the application wired nothing. A test provokes a drop
through an instrumented emitter with no hook and reads the line back; it
fails on 0.2.4.

### Documentation that still said built things were not built

The deploy guide said `keys.provider: none` was not built and today's
default was `local`; the emit guide and the runbook said
`audit.emit.queue.pending` was not built, on the same page the runbook
relies on it; the configuration reference said `writer.consumers` and the
extension toggles arrive with the rewrite; the architecture page's status
table said the chart's modes and goldens were still to come and the
TypeScript package is on a registry; the read guide said the same; the
presets policy said `history` was not ready. All of it predates 0.2.0.
Corrected against the code. Two chart comments and the audit-page design
note said things the chart does not enforce or the first adopter
contradicts; reworded.

### Documentation, from commissioning the first installation

- The deploy guide told an adopter to vendor the chart at 0.1.0 because there
  was no release. There are releases, and no document named where the chart
  actually lives; it now points at `oci://ghcr.io/truvity/charts` and says
  that one tag stamps the chart and all three images.
- The runbook gains **a scheduled job is not running**. A failing CronJob says
  nothing, and its pods are deleted with the Job, so there is no log by the
  time anybody looks. The tell is a `lastScheduleTime` with no
  `lastSuccessfulTime`, and the way to get the error back is to re-run the job
  from the CronJob and read that pod. Alert on the CronJob, not on the
  archive: an hour with no digest is only visible from the chain, a day late.
- The runbook's **digest chain has a gap** says that catching up works only
  once something has been sealed. With no digest at all a run seals the hour
  that just closed, not every hour since the archive began — right for a new
  installation, and a silent gap if the job was broken over its own first
  runs. What to compare, and how to backfill by range, are spelled out.
- The status table claimed the chart's modes and goldens were still to come,
  and that `@truvity/audit` is published with a release. The first is done;
  the second is not true — it is consumed from a release tag, not a registry.

## v0.2.4

### A put carries the legal-hold header only when it places a hold

The digest job could not write its own digest: `AccessDenied ...
s3:PutObjectLegalHold`. Every put into a locked archive sent the header,
as OFF when there was no hold, and S3 charges the permission for the
header's presence whatever its value. So writing the archive at all
required the right to place a legal hold -- which the digest and verify
jobs, whose policies follow the guide's least privilege, do not have and
should not.

The header is now sent only to place a hold. An absent header and OFF
leave the object in the same state, because a bucket has no default legal
hold the way it has a default retention, so nothing about an object
changes. The guide says so too, with what the refusal looks like for
anyone who meets it on an older version.

## v0.2.3

Two fixes and a correction, all found by running a real installation.

### The scheduled jobs read the archive from the environment

Every CronJob failed, hourly and silently: `audit: name the archive's bucket
with --bucket`. The chart gives each job the archive in `AUDIT_BUCKET`,
`AUDIT_PREFIX` and `AWS_REGION`, which `audit-writer` and `audit-query`
already read as flag defaults and which `audit` did not read at all. So the
jobs ran with no bucket, said an argument was missing, and looked like a
deployment that forgot one rather than a binary that ignored one.

All three flags now default from the environment in every subcommand that
takes them -- `verify`, `replay`, `reindex`, `digest`, `hold` and `key` --
and a flag given explicitly still wins. `cmd/audit` had no tests at all; it
now has one that sets `AUDIT_BUCKET` and asserts that each of those six gets
past the check, verified to fail on all six without the fix.

### The S3 guide names `schema/` among the writer's reads

Documentation only, and the reason a real installation's writer crash-looped
on a 403. The guide's IAM table listed `holds/`, `profile=` and `identity/`
as what the writer reads, and left out `schema/` -- where it records each
profile's composition and from which it reads the last one back on every
start. A policy written from that table lets the writer put the composition
and not get it, so it writes one object, takes an AccessDenied and dies, on a
loop, which reads as a broken archive rather than a missing verb.

## v0.2.2

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

## v0.2.1

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

## v0.2.0

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

## v0.1.1

The images publish where the chart looks for them. 0.1.0's release
failed: the four ko images carried a `repositories:` list each, and the
release workflow's `KO_DOCKER_REPO` wins over it, so ko tried to publish
`ghcr.io/truvity` itself and the registry answered 400. They now take
their name from the command's import path under one repository path, as
access-roster's two images do, and the chart's defaults name the same
four: `ghcr.io/truvity/audit/{audit,audit-writer,audit-query,audit-registry}`.

## v0.1.0

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
