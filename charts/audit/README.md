# audit

One installation of the audit trail: the receiver, the writer, the jobs that
seal, verify and prune what it writes, and the query service that reads it
back. The Audit page lives in the application's console and is not in here.

**This chart is instantiated, not deployed.** An installation belongs to one
application and runs in that application's namespace, rendered by the
application's own chart with this one as a dependency
([0011](../../docs/decisions/0011-one-installation-per-service-or-product.md)).
There is no central installation and no shape that puts the writer inside
the application. The deployment pages show the values each shape takes:
[direct](../../docs/deployment/direct.md),
[stream](../../docs/deployment/stream.md).

## What it deploys

- **`audit-writer`**, a Deployment serving the sink and
  `RegistryService` — the application registers its catalogue with the same
  address it writes to. With `mode: direct` it is also the writer: it puts
  the objects and acknowledges once they are stored. With `mode: stream` it
  publishes to JetStream, and the same image runs again in consumer mode as
  the writer.
- **`audit migrate`**, a pre-install/pre-upgrade hook Job applying the index
  schema before the writer rolls. The writer refuses to start against a schema
  it does not know and never migrates itself.
- **`audit-query`** (`query.enabled`), search, facets, get, export, tail and —
  given the keys — resolve, behind the grants in `query.grants`. Every read is
  recorded through the writer. It reads the index as **its own database
  role**, which must not own the tables (`query.config.database`).
- **Four CronJobs**: `audit digest` hourly, `audit verify` nightly (one job
  for the profiles it lists, or every profile), `audit purge` daily,
  `audit clock-sync` daily. Each runs `--config` against its own file and
  records what it did through the writer's own sink. `clock-sync` needs at
  least one reference clock (`jobs.clockSync.config.ntp`): every preset with a
  compliance obligation asks for a daily record of the offset, and its
  configuration is refused without one.

`mode` chooses between them. In `direct` the chart renders one Deployment that
serves the sink and writes the archive. In `stream` it renders two: a receiver
that serves the sink and publishes, holding neither the bucket nor a key, and
`writer.consumers` writers that read the stream and put the objects. The
Service keeps its name and the receiver keeps the `writer` component label in
both, because it is the address records are written to and that should not move
when a deployment changes shape.

Three images, one per binary: `image.writer`, `image.query`, `image.cli`. The
receiver serves `RegisterCatalogue`, so there is no fourth.

**Whose catalogue is whose.** The writer takes it from the caller's verified
service account, never from the document, and `workloadIdentity.workloads` is
the mapping: one entry per workload that may register, naming the source it
speaks for. A workload missing from it cannot register at all. An installation
that keeps an index and verifies callers must fill it in, and the chart refuses
to render when it is empty — with no mapping every registration would be
refused at run time instead.

## How it is configured

Each component has a `config:` block: the binary's own configuration file,
rendered as it stands into a ConfigMap `<fullname>-<component>-config` and
mounted at `/etc/audit/config.yaml`. The components are `writer`, `receiver`
(stream mode), `query`, `migrate` and `jobs.digest`, `jobs.verify`,
`jobs.purge` and `jobs.clockSync`. The chart translates none of it: a key
under `config:` is the binary's key, and it is validated by
`values.schema.json`, which embeds the schemas in `schemas/config/`, and again
by the binary at start-up. The
[configuration reference](../../docs/reference/configuration.md) lists every
key.

What is not configuration is the platform's, and each component has the same
three of those:

- `secretEnv`: environment variables from a Secret's keys, which the config
  names as the holder of a secret (`passwordEnv`, `credentialsEnv`,
  `tokenEnv`). A secret is never in `config:`;
- `secretMounts`: a Secret mounted as a directory, for a key or a root a
  config names by path;
- `tokens`: a projected service-account token of an audience, a file `token`
  in the `mountPath`, for `tokenFile` and `jwtFile` to name.

Telemetry is the `OTEL_*` environment, which the platform sets on the pods;
the chart has no telemetry value. The documents a config names by path are
rendered from the chart's own values: `profiles` into
`/etc/audit/deployment.yaml`, `workloadIdentity` into
`/etc/audit/workloads.yaml`, `query.grants` into `/etc/audit/grants.yaml`,
`catalogues` into `/etc/audit/catalogues/`. `trust` mounts a CA bundle at
`/etc/audit/trust/<key>` and `keysVolume` the local key directory at
`/var/lib/audit/keys`.

## What the deployment brings

The chart takes references; it creates none of these.

| thing | value |
|---|---|
| a bucket belonging to the environment: with Object Lock in compliance mode for a profile that demands it, or without a lock where none does ([0014](../../docs/decisions/0014-lock-modes-and-store-tiers.md)) | `writer.config.archive.bucket`, `.lockMode` |
| **only on an S3-compatible store that is not AWS**: its endpoint, whether its certificate covers a bucket subdomain, and the names of the variables holding static keys if it has no pod identity | `archive.bucket.endpoint`, `.pathStyle`, `.credentialsEnv` with `secretEnv` |
| **a prefix of its own within it**, required wherever the bucket is shared: it is what keeps two applications' archives apart, and what each role's IAM is scoped to | `archive.prefix` |
| a writer role that may put objects with a legal hold on (`s3:PutObjectLegalHold`), read and lengthen their retention (`s3:GetObjectRetention`, `s3:PutObjectRetention`), and read `holds/` | the writer's ServiceAccount annotation |
| the digest signing key: a Secret (PEM, ed25519), an AWS KMS ECC_NIST_P256 key, or an OpenBAO transit ed25519 key | `jobs.digest.config.signer`: `keyFile` (with `secretMounts`), `kmsKey` or `transit` |
| a Secret with its public half | `jobs.verify.config.publicKeyFile`, with `secretMounts` |
| a reference clock the clock-synchronisation job can reach | `jobs.clockSync.config.ntp` |
| **a database in the application's existing Postgres**, owned by the writer, in a Secret. It holds the index, the dedupe table and the rollups, all rebuildable with `audit reindex`, so it needs no backup | `writer.config.database` and `passwordEnv`, with `secretEnv` |
| **a separate read-only role** for the query service: `usage` on the schema, `select` on its tables and nothing else. Tenant row-level security binds only a role that does not own the tables | `query.config.database` and `passwordEnv`, with `query.secretEnv`; `migrate.config.reader` names the role |
| the JetStream stream, already created, with `mode: stream` | `writer.config.stream`, `receiver.config.stream` |
| **if the broker verifies who connects**: an auth callout that reviews a projected service-account token and maps this namespace to an account, accepting the audience the chart projects | `stream.nats.tokenFile` and a `tokens` entry of the broker's audience |
| the issuers callers sign in with, and who may read what | `query.grants` ([access](../../docs/guides/read.md#access)) |
| an exports bucket with no Object Lock, if exports are wanted; on a store of its own if need be | `query.config.exports.bucket`, with its own `endpoint`, `pathStyle` and `credentialsEnv` |
| the cluster's service-account issuer, reachable over HTTPS from the pods | `workloadIdentity.issuers` |
| the images | `image.writer`, `image.query`, `image.cli` — one per binary, built by ko from `.goreleaser.yaml`; distroless, no shell |
| a role per component — writer, query, digest, and verify — bound through its ServiceAccount's annotations. The receiver, purge and clock-sync have accounts and no roles; the chart refuses the receiver sharing the writer's | `serviceAccount`, `receiver.serviceAccount`, `query.serviceAccount`, `jobs.*.serviceAccount` |
| **only if the deployment chooses a key provider**: a Secret with the 32-byte root (`local`), or an OpenBAO transit engine with a JWT role per component ([what the engine needs](../../docs/operations/openbao-keys.md#what-the-engine-needs)) | `keys.local.rootFile` with `secretMounts`, or `keys.provider: transit` with `keys.transit.openbao.login` and a `tokens` entry |
| a CA bundle, if OpenBAO or Postgres serve from a private chain (e.g. trust-manager's) | `trust.configMap` |
| a `ReadWriteMany` storage class, for more than one replica on `local` keys (transit needs none) | `keysVolume` |

## Keys are off

`keys.provider: none` is the default, which is no `keys` block: no key directory, no login to a secret
manager, no `identity/` prefix in the archive, and resolve refused as
`unimplemented`. A deployment instead declares
`externalIdentifiersAreOpaque`, which relaxes a profile's `external:
pseudonym` to `clear` and makes the writer refuse a record whose external
actor or subject carries something that looks like a direct identifier
([0013](../../docs/decisions/0013-no-pseudonymisation-keys-by-default.md)).

`local` and `transit` stay, for a deployment that must be able to
crypto-shred. An installation that runs neither must set
`externalIdentifiersAreOpaque`, or compose only profiles that keep nobody:
the writer refuses to start otherwise, naming the profile, rather than
writing whatever arrives into an archive nothing can edit.

## Extensions

Two projections of the same records, both off, both switched on per
installation, and neither adds anything to the request path:
`extensions.billing.enabled` adds rollups at index time and a monthly
statement CronJob; `extensions.quotas.enabled` adds a usage consumer, a
counter cache and an hourly reconciler, and needs `mode: stream`. Both toggles
exist and render nothing: what fills them is designed and not yet built, so
the toggles are here to keep a deployment's values from changing when it
lands. Billing refuses without a metering profile, and quotas without a
stream, because neither could work.

## Who may write

The receiver verifies every caller's projected service-account token against
the cluster's own OIDC issuer. The token's subject is the service account,
which the kubelet vouches for and the workload cannot choose, and the writer
stamps it on each record as the observer. The chart's own jobs and the query
service are given projected tokens (their `tokens`, audience `audit`) and
present them through `sink.tokenFile` the same way.

The application's pods mount a projected token with the same audience and
point `AUDIT_TOKEN_FILE` at it, or set the bearer themselves.
`anonymousWrites: true` in the writer's config turns verification off, for a
trial install only. The binary refuses to start with neither `workloads` nor
that key set.

The stream is reached the same way. With a `tokens` entry of the broker's
audience and `stream.nats.tokenFile` naming it, the receiver and the writers
present the token to the broker as their NATS token, read afresh on every
connect; the broker's auth callout, which is the deployment's, reviews it and
maps the namespace to an account. Without them, they connect with no
credentials, for a broker that verifies nobody
([stream](../../docs/deployment/stream.md#authenticating-to-the-stream)).

## What it refuses to render

Every configuration in `tests/invalid/audit/` is something the binaries
reject at start-up or, worse, accept and get quietly wrong, and
`testdata/refuse.sh` holds each refusal to its words; each file names the
reason on its first line. A `config:` that does not match its schema is
refused first, naming the path: a misspelt key, a missing `deployment`, a
password in a database URL, a value from before the file such as a top-level
`bucket`. What is left is what only the platform can see
(`templates/_checks.tpl`):

- `mode` is `direct` or `stream` and nothing else, and `profiles` is not
  empty;
- `writer.config.replicas` is the number of writer pods the chart renders
  (`replicas` in direct mode, `writer.consumers` in stream mode), because the
  writer cannot count them itself, and more than one needs a `database` in
  `writer.config`, since deduplication in one process cannot absorb a
  redelivery that lands on another;
- `mode: stream` needs `receiver.config` with `mode: receiver`,
  `writer.config.stream` and a `database`;
- more than one writer pod with a `keysVolume` that is not ReadWriteMany:
  data keys are random rather than derived, so separate directories mean a
  different pseudonym for the same person on each replica. `query.keysVolume`
  needs the volume enabled and ReadWriteMany;
- `workloads` in the writer's config without `workloadIdentity.issuers`,
  issuers without `workloads`, an index with verified callers and no
  `workloadIdentity.workloads` (with no mapping every registration would be
  refused at run time), and a workload that names no issuer when more than one
  is trusted;
- the query service enabled with no `query.grants.issuers`, or with the
  writer's `database.url`: an owner bypasses the tenant policies;
- an extension enabled with no profile it can read: `extensions.billing`
  without a metering profile, `extensions.quotas` without `mode: stream`.

The binaries refuse the rest at start-up, naming the key: `stream.ackWait` not
longer than `roll.interval`, a profile that demands a stricter lock than
`archive.lockMode` (`pci-dss` composed on `lockMode: none`, refused by the
writer, the digest job and the verify job), OpenBAO configured with none or
more than one way to sign in, and a digest `signer` with none or more than one
of its three.

## Checking it

```
just chart          # lint, every refusal, golden renders
audit verify --profile <p> --last 24h --bucket <b> --public-key <file>
```

The second needs read access to the archive and the public key, and nothing
that has to be trusted.
