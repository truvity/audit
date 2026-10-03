# Deploying an installation

An installation belongs to one application and runs in that application's
namespace, rendered by the application's own chart with this repository's
chart as a dependency
([0011](../decisions/0011-one-installation-per-service-or-product.md)).

This page is the **procedure**: what to prepare before anything is installed,
how to install it, and how to tell that it works. It does not repeat the
shapes. Pick one first — [direct](../deployment/direct.md) or
[stream](../deployment/stream.md) — and take that page's values as the body of
your values file; [what to prepare](../deployment/README.md) is the same for
both. For what each part is and what it holds, read
[the architecture](../architecture.md). For what the application does at its
end, [integrating](integrate.md).

## 1. Pick the shape

| shape | for | needs |
|---|---|---|
| [direct](../deployment/direct.md) | an internal service, or any cluster without a stream | a bucket, a database |
| [stream](../deployment/stream.md) | a product: many pods, metering, quotas | a bucket, a database, a NATS account |

Switching later is a change to the receiver's configuration and to no record,
so a first installation that is unsure should start direct.

## 2. Prepare

Four things, none of which the chart creates. It takes references to all of
them and refuses to render when one is missing.

### A bucket, with or without Object Lock, and a prefix

The bucket belongs to the **environment**, not to the installation:
versioning, a policy that denies deletes to everyone, replication and
lifecycle, configured once. Which of two tiers it is
([0014](../decisions/0014-lock-modes-and-store-tiers.md)) depends on the
profiles the installation composes:

- **record** — Object Lock in compliance mode. Required by a profile
  composed from `pci-dss`, `nen-7513`, `dora` or `evidence-etsi`. Object
  Lock can only be turned on when a bucket is created:

  ```sh
  aws s3api create-bucket --bucket example-audit \
    --create-bucket-configuration LocationConstraint=eu-example-1 \
    --object-lock-enabled-for-bucket
  ```

- **attested** — no lock; the per-object and per-record
  hashes that `audit verify` checks are the integrity control until seals
  ([0019](../decisions/0019-seals.md)) arrive. Enough for `security`, `history`
  and `billing-nl`, and the only tier a store without the Object Lock API can
  offer. The same command without `--object-lock-enabled-for-bucket`, and
  `archive.lockMode: none` in the writer's configuration. On any S3-compatible
  store that is not AWS — a service from another provider, MinIO, Ceph — add
  `bucket.endpoint`, `bucket.pathStyle` if its certificate does not cover a
  bucket subdomain, and `bucket.credentialsEnv` with static keys if it has no
  pod identity; the [S3 guide](../operations/s3-guide.md#s3-compatible-stores)
  has the recipe.

The bucket needs no default retention: on the record tier the writer sets
each object's from its profile, and on the attested tier retention is the
bucket's lifecycle rule. Each application then writes under a **prefix of its own**
(`audit/<application>/…`), which is what keeps two installations apart in one
bucket. [Sharing a bucket](../operations/s3-guide.md#sharing-a-bucket) has the
policy, and [IAM per component](../operations/s3-guide.md#iam-per-component)
has the statements for each of the three roles, each scoped to its own part of
the prefix and none of them with a delete:

| role | on the prefix |
|---|---|
| writer | put objects under `records/`, `catalogue/` and the other prefixes it writes, put and read their retention, put a legal hold, read `holds/` |
| verify job | read; it puts nothing |
| query service | read, and write on the exports bucket if exports are wanted |

Bind each through its ServiceAccount's annotations — `serviceAccount` (the
writer; the consumers in stream mode), `receiver.serviceAccount`,
`query.serviceAccount`,
`jobs.verify.serviceAccount`, `jobs.purge.serviceAccount`,
`jobs.clockSync.serviceAccount` — with Pod Identity or IRSA.

Every component runs as a ServiceAccount of its own, named
`<fullname>-<component>` (`audit-receiver`, `audit-query`,
`audit-verify`, `audit-purge`, `audit-clock-sync`); only the writer keeps the
release's name (`audit`). Each takes `create`, `name` and `annotations`. In
stream mode the chart refuses a receiver and a writer that share one
ServiceAccount name: a receiver must not hold the archive's write identity.
The purge job works on the index database only and needs no role; clock sync
needs none.

### A database, and a read-only role for the query service

One Postgres database, in the application's existing cluster if it has one.
The writer owns it. The query service reads it as a **separate role**, because
the tenant row-level policies bind a role that does not own the tables, and it
is what still holds if a query forgets its tenant term. The chart refuses the
writer's URL under the query service's `database`.

Create the role — the chart creates none — and let the migration grant it:

```sh
psql "$OWNER_URL" -c "create role audit_query login password '…'"
audit migrate --database "$OWNER_URL" --reader audit_query
```

`--reader` grants that role usage on the schema and select on every table, now
and later, and nothing else. The chart runs the same migration as a hook Job
before the writer rolls when `migrate.enabled` is true, with
`migrate.config.reader` as the reader; the password is named by
`passwordEnv` and supplied by `secretEnv`.

The index is a projection: `audit reindex` rebuilds it from the archive. It
needs no backup and no replica, and losing it costs search until the rebuild
finishes, not evidence.

### A reference clock

Every preset with a compliance obligation asks for a daily record of the
clock's offset from UTC, and the job's configuration refuses to load without
a reference:

```yaml
jobs:
  clockSync:
    enabled: true
    config:
      ntp: ["169.254.169.123"]
      maxOffset: 1s        # beyond this the run fails, so the job going red is the alert
```

The job does not set the clock. Whatever runs the machine does that, and
recording the time of things is a separate job from setting it. The pods need
egress to the reference.

### The images

One image per binary, built by ko from `.goreleaser.yaml` under
`ghcr.io/truvity/audit/`: `audit-writer` (the receiver and the writer),
`audit` (the toolchain the jobs run) and `audit-query`. Three, because the
receiver serves `RegisterCatalogue` itself. A release publishes all three
under the tag that also stamps the chart, so a deployment pins one version.
`just snapshot` builds them for a development cluster that cannot pull from
the registry; then set `image.*.repository` and `image.*.tag`.

All of them are distroless and have no shell, which is why every job in the
chart is a command with arguments.

### What you do not have to prepare

**Pseudonymisation keys.** `keys.provider: none` is the default (no `keys`
block), so there
is no key directory, no secret manager to log in to, no `identity/` prefix,
and resolve is refused as unimplemented. The deployment declares instead that the external
identifiers it receives are opaque. A deployment that must be able to
crypto-shred configures a provider deliberately
([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)).

**A signing key.** Nothing in v1 signs yet: the digest job that used one was
removed with the v0 layout, and seals ([0019](../decisions/0019-seals.md)) will
take its place. The signers (an AWS KMS ECC_NIST_P256 key, an OpenBAO transit
ed25519 key, or a key file) and `audit key public` are kept for them. `audit
verify` needs no key.

## 3. Install

The application's chart takes this one as a dependency:

```yaml
# the application's Chart.yaml
dependencies:
  - name: audit
    version: 0.2.4
    repository: oci://ghcr.io/truvity/charts
```

One tag stamps the chart and all three images, so the version above is the
whole of what a deployment pins. The images default to the chart's
`appVersion`; naming a tag under `image.*` pins one of them somewhere else,
which is a lag to close rather than a thing to configure.

and its values file carries an `audit:` block. Take the body of that block
from the shape you picked — [direct](../deployment/direct.md#values) or
[stream](../deployment/stream.md#values) — which is where every value and its
reason lives. What every installation sets, whichever shape:

| value | what it is |
|---|---|
| `writer.config.archive.bucket`, `.prefix`, `.kmsKey` | the archive, and this application's part of it |
| `writer.config.archive.lockMode` | `compliance` (the default), `governance` or `none`: which tier the bucket is. The writer refuses to start if a profile demands more |
| `bucket.endpoint`, `.pathStyle`, `.credentialsEnv` | only on an S3-compatible store that is not AWS: where it is, how the bucket is addressed, and the names of the variables holding static keys if it has no pod identity |
| `profiles` | what copies are kept, each composed from presets |
| `writer.config.database`, `writer.secretEnv` | the index: its URL, and the Secret holding the password |
| `query.enabled`, `query.config`, `query.grants` | the read path, its own database role, and who may read what |
| `jobs.*.config` | verify, purge and clock-sync |

Which presets to compose is a policy question, not a values question:
[which presets a deployment composes](../operations/presets-policy.md).
Compose `security` always, `billing-nl` where the installation meters, and the
rest only where an obligation is real — retention cannot be shortened later.

Then, from the application's chart:

```sh
helm dependency build ./charts/<application>
helm upgrade --install <application> ./charts/<application> -n <app> -f values.yaml
```

The chart **refuses to render** a configuration the binaries would reject, or
accept and get quietly wrong: a configuration that does not match its
binary's schema (a misspelt key, a password in a
URL), the writer's database given to the query service, `mode: stream` with no
`writer.config.stream` or no database, a `replicas` that is not the number of
writer pods, or an extension whose profile the deployment does not compose.
Each refusal says why, and they are listed in
[the chart README](../../charts/audit/README.md) with
[`values.yaml`](../../charts/audit/values.yaml) commenting every setting.

One refusal the chart cannot make is the binaries': a profile whose presets
demand a lock stricter than `archive.lockMode` — `pci-dss` on `lockMode: none`, say.
The presets' readings live in the binaries, not the chart, so the writer
refuses to **start** instead, naming the profile
and both modes, and the first rollout is where it shows.

## 4. Check that it works

1. **The receiver is up.** The chart names its Deployment after the release
   and the dependency — `kubectl -n <app> rollout status deploy/<release>-audit`.
   It refuses to start, with the reason in its log, if the database is at a
   schema version it does not know, the stream is missing, the holds cannot
   be read, or a profile demands a lock the store is not written with.
2. **The application registered its catalogue.** It logs the registration at
   start-up, and refuses to start if the receiver refused the catalogue.
3. **A record goes through.** Perform an action the catalogue declares, or run
   [the example application](../../examples/emit/main.go) against the
   receiver's Service.
4. **It is in the archive.**
   `aws s3 ls s3://example-audit/audit/app/records/security/ --recursive`
   lists an object per profile, tenant and ingest batch, under the hour it
   was ingested in.
5. **It verifies.** The following night the verify job checks the previous
   day's objects and records the result. An auditor checks the same thing
   with read access to the archive and nothing else:

   ```sh
   audit verify --profile security --last 24h \
     --bucket example-audit --prefix audit/app
   ```

6. **The query service keeps its contract.** With a token that may read:

   ```sh
   audit conformance --query https://audit-query.<app>.svc:8080 \
     --profile security --token-file token
   ```

   Run it after the first records land.

7. **Alerts.** With the `OTEL_EXPORTER_OTLP_ENDPOINT` environment set on the
   pods by the platform, page on
   `audit.writer.index.deferred` (the index is behind the archive) and the
   dead-letter counter (records the writer could not take), and — in the
   application — on `audit.emit.records.dropped`. The
   [runbook](../operations/runbook.md) says what to do about each.

Each shape has one more thing to watch, and its page says which: the roll
interval in [direct](../deployment/direct.md#checking-it-works), the
consumer's pending count in [stream](../deployment/stream.md#checking-it-works).

## 5. Day two

- [Runbook](../operations/runbook.md): the index is behind, a dead letter, a
  clock out of tolerance, a gap in the chain.
- [Legal holds](../operations/s3-guide.md#legal-hold):
  `audit hold place|release|list`.
- Rebuilding the index:
  `audit reindex --profile <p> --from <day> --to <day>`.
- [Verification](../operations/verify.md), which an auditor performs against
  the archive and nothing else.
- Extensions, switched on per installation and neither in the request path:
  [billing](../deployment/extensions/billing.md),
  [usage quotas](../deployment/extensions/quotas.md).
