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
  role**, which must not own the tables (`query.database`).
- **Four CronJobs**: `audit digest` hourly, `audit verify` nightly per
  profile, `audit purge` daily, `audit clock-sync` daily. Each records what
  it did through the writer's own sink. `clock-sync` needs at least one
  reference clock (`jobs.clockSync.ntp`): every preset with a compliance
  obligation asks for a daily record of the offset, and the chart refuses to
  render without one.

`mode` is not built yet: it arrives with the rewrite and replaces inferring
the shape from whether `stream.url` is set.

Three images, one per binary: `image.writer`, `image.query`, `image.cli`. The
receiver serves `RegisterCatalogue`, so there is no fourth.

**Whose catalogue is whose.** The writer takes it from the caller's verified
service account, never from the document, and `workloadIdentity.workloads` is
the mapping: one entry per workload that may register, naming the source it
speaks for. A workload missing from it cannot register at all. An installation
that keeps an index and verifies callers must fill it in, and the chart refuses
to render when it is empty — with no mapping every registration would be
refused at run time instead.

## What the deployment brings

The chart takes references; it creates none of these.

| thing | value |
|---|---|
| a bucket with Object Lock in compliance mode, belonging to the environment | `bucket` |
| **a prefix of its own within it**, required wherever the bucket is shared: it is what keeps two applications' archives apart, and what each role's IAM is scoped to | `prefix` |
| a writer role that may put objects with a legal hold on (`s3:PutObjectLegalHold`), read and lengthen their retention (`s3:GetObjectRetention`, `s3:PutObjectRetention`), and read `holds/` | the writer's ServiceAccount annotation |
| the digest signing key: a Secret (PEM, ed25519), an AWS KMS ECC_NIST_P256 key, or an OpenBAO transit ed25519 key | `jobs.digest.signingKey.existingSecret`, `jobs.digest.kmsKey` or `jobs.digest.transit` |
| a Secret with its public half | `jobs.verify.publicKey.existingSecret` |
| a reference clock the clock-synchronisation job can reach | `jobs.clockSync.ntp` |
| **a database in the application's existing Postgres**, owned by the writer, in a Secret. It holds the index, the dedupe table and the rollups, all rebuildable with `audit reindex`, so it needs no backup | `database.existingSecret` |
| **a separate read-only role** for the query service: `usage` on the schema, `select` on its tables and nothing else. Tenant row-level security binds only a role that does not own the tables | `query.database.existingSecret`, `query.database.role` |
| the JetStream stream, already created, with `mode: stream` | `stream.url`, `stream.name` |
| the issuers callers sign in with, and who may read what | `query.grants` ([access](../../docs/guides/read.md#access)) |
| an exports bucket with no Object Lock, if exports are wanted | `query.exports.bucket` |
| the cluster's service-account issuer, reachable over HTTPS from the pods | `workloadIdentity.issuers` |
| the images | `image.writer`, `image.query`, `image.cli` — one per binary, built by ko from `.goreleaser.yaml`; distroless, no shell |
| a role per component — writer, query, digest, verify — bound through its ServiceAccount's annotations | `serviceAccount`, `query.serviceAccount`, `jobs.*.serviceAccount` |
| **only if the deployment chooses a key provider**: a Secret with the 32-byte root (`local`), or an OpenBAO transit engine with a JWT role per component ([what the engine needs](../../docs/operations/openbao-keys.md#what-the-engine-needs)) | `keys.local.existingSecret`, or `keys.provider: transit` with `openbao` and `keys.transit.role` |
| a CA bundle, if OpenBAO or Postgres serve from a private chain (e.g. trust-manager's) | `trust.configMap` |
| a `ReadWriteMany` storage class, for more than one replica on `local` keys (transit needs none) | `keys.local.persistence` |

## Keys are off

`keys.provider: none` is the default: no key directory, no login to a secret
manager, no `identity/` prefix in the archive, and resolve refused as
`unimplemented`. A deployment instead declares
`externalIdentifiersAreOpaque`, which relaxes a profile's `external:
pseudonym` to `clear` and makes the writer refuse a record whose external
actor or subject carries something that looks like a direct identifier
([0013](../../docs/decisions/0013-no-pseudonymisation-keys-by-default.md)).

`local` and `transit` stay, for a deployment that must be able to
crypto-shred. Both are built; the `none` default and
`externalIdentifiersAreOpaque` are not built yet and arrive with the
rewrite, where today's default is `local`.

## Extensions

Two projections of the same records, both off, both switched on per
installation, and neither adds anything to the request path:
`extensions.billing.enabled` adds rollups at index time and a monthly
statement CronJob; `extensions.quotas.enabled` adds a usage consumer, a
counter cache and an hourly reconciler, and needs `mode: stream`. Both are
not built yet: they arrive with the rewrite.

## Who may write

The receiver verifies every caller's projected service-account token against
the cluster's own OIDC issuer. The token's subject is the service account,
which the kubelet vouches for and the workload cannot choose, and the writer
stamps it on each record as the observer. The chart's own jobs are given
projected tokens (`workloadIdentity.audience`, default `audit`) and present
them the same way.

The application's pods mount a projected token with the same audience and
point `AUDIT_TOKEN_FILE` at it, or set the bearer themselves.
`anonymousWrites: true` turns verification off, for a trial install only. The
chart refuses to render with neither issuers nor that flag set.

## What it refuses to render

Every configuration listed in `testdata/refusals.txt` is something the
binaries reject at start-up or, worse, accept and get quietly wrong, and
`testdata/refuse.sh` holds each refusal to its words. The least obvious:

- `mode: stream` with no `stream.url`: a receiver told to publish with
  nowhere to publish to acknowledges nothing, and the application's `block`
  actions all fail;
- an extension enabled with no profile it can read — `extensions.billing`
  without a metering profile, `extensions.quotas` without `mode: stream` —
  because a projection of records that are never kept is a values mistake
  worth catching at render time;
- `security` composed with no reference clock for `jobs.clockSync`: an
  integrity chain whose timestamps nobody vouches for proves less than it
  appears to;
- OpenBAO (the `transit` key provider, the transit digest signer) needs
  `openbao.address` and exactly one way to sign in for each component that
  uses it — a role on `openbao.auth.mount`, a token Secret or a token
  file — and no two components signing in as the same one;
- more than one replica with the `local` key provider needs a key directory
  every replica can write, because data keys are random rather than derived —
  separate directories mean a different pseudonym for the same person on each
  replica;
- a key directory that does not persist re-keys every tenant on every restart,
  so turning persistence off takes `keys.local.ephemeralIsAcceptable: true`,
  not a flag.

There is one more, which the writer serving `RegisterCatalogue` brought with
it: an installation that verifies callers and keeps an index must map the
workloads that may register in `workloadIdentity.workloads`. The first two of
the list above are not built yet and arrive with the rest of the rewrite.

## Checking it

```
just chart          # lint, every refusal, golden renders
audit verify --profile <p> --last 24h --bucket <b> --public-key <file>
```

The second needs read access to the archive and the public key, and nothing
that has to be trusted.
