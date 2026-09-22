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

Five things, none of which the chart creates. It takes references to all of
them and refuses to render when one is missing.

### A bucket with Object Lock, and a prefix

The bucket belongs to the **environment**, not to the installation: Object
Lock in compliance mode, versioning, a policy that denies deletes to everyone,
replication and lifecycle, configured once. Object Lock can only be turned on
when a bucket is created:

```sh
aws s3api create-bucket --bucket example-audit \
  --create-bucket-configuration LocationConstraint=eu-central-1 \
  --object-lock-enabled-for-bucket
```

The bucket needs no default retention: the writer sets each object's from its
profile. Each application then writes under a **prefix of its own**
(`audit/<application>/…`), which is what keeps two installations apart in one
bucket. [Sharing a bucket](../operations/s3-guide.md#sharing-a-bucket) has the
policy, and [IAM per component](../operations/s3-guide.md#iam-per-component)
has the statements for each of the four roles, each scoped to its own part of
the prefix and none of them with a delete:

| role | on the prefix |
|---|---|
| writer | put objects, put and read their retention, put a legal hold, read `holds/` |
| digest job | put under `digest/`, and `kms:Sign` on the signing key |
| verify job | read, and put under `verified/` |
| query service | read, and write on the exports bucket if exports are wanted |

Bind each through its ServiceAccount's annotations — `serviceAccount`,
`query.serviceAccount`, `jobs.digest.serviceAccount`,
`jobs.verify.serviceAccount` — with Pod Identity or IRSA.

### A database, and a read-only role for the query service

One Postgres database, in the application's existing cluster if it has one.
The writer owns it. The query service reads it as a **separate role**, because
the tenant row-level policies bind a role that does not own the tables, and it
is what still holds if a query forgets its tenant term. The chart refuses the
writer's Secret or URL under `query.database`.

Create the role — the chart creates none — and let the migration grant it:

```sh
psql "$OWNER_URL" -c "create role audit_query login password '…'"
audit migrate --database "$OWNER_URL" --reader audit_query
```

`--reader` grants that role usage on the schema and select on every table, now
and later, and nothing else. The chart runs the same migration as a hook Job
before the writer rolls when `database.migrate` is true, with
`query.database.role` as the reader.

The index is a projection: `audit reindex` rebuilds it from the archive. It
needs no backup and no replica, and losing it costs search until the rebuild
finishes, not evidence.

### A signing key for the digest chain

Writing the archive and vouching for it must stay different privileges, so the
digest job signs with a key the writer's role cannot use. Three ways, in order
of preference:

```sh
# AWS KMS (jobs.digest.kmsKey): an ECC_NIST_P256 key. The private half never
# leaves KMS, and only the digest job's role has kms:Sign on it.
# OpenBAO transit (jobs.digest.transit.key): an ed25519 key, the same
# separation for a deployment whose secrets live in OpenBAO.

# Or a key file, when there is neither:
openssl genpkey -algorithm ed25519 -out key.pem
openssl pkey -in key.pem -pubout -out public.pem
kubectl create secret generic audit-signing-key -n <app> --from-file=key.pem=key.pem
kubectl create secret generic audit-signing-public -n <app> --from-file=public.pem=public.pem
```

The verify job and every auditor need only the public half:
`audit key public --kms-key <id>` (or `--transit-key`, or `--key`) prints it.
Keep a key file somewhere other than the cluster —
[key custody](../operations/key-custody.md).

### A reference clock

Every preset with a compliance obligation asks for a daily record of the
clock's offset from UTC, and the chart refuses to render an installation that
composes one without a reference configured:

```yaml
jobs:
  clockSync:
    enabled: true
    ntp: ["169.254.169.123"]
    maxOffset: 1s          # beyond this the run fails, so the job going red is the alert
```

The job does not set the clock. Whatever runs the machine does that, and
recording the time of things is a separate job from setting it. The pods need
egress to the reference.

### The images

One image per binary, built by ko from `.goreleaser.yaml` under
`ghcr.io/truvity/audit/`: `audit-writer` (the receiver and the writer),
`audit` (the toolchain the jobs run) and `audit-query`. Three, because the
receiver serves `RegisterCatalogue` itself. No release has been published yet;
until one is, build them with `just snapshot`, push them to a registry the
cluster can pull from, and set `image.*.repository` and `image.*.tag`.

All of them are distroless and have no shell, which is why every job in the
chart is a command with arguments.

### What you do not have to prepare

**Pseudonymisation keys.** `keys.provider: none` is the default — *not built
yet: it arrives with the rewrite, and today's default is `local`* — so there
is no key directory, no secret manager to log in to, no `identity/` prefix,
and resolve is refused as unimplemented. The deployment declares instead that the external
identifiers it receives are opaque. A deployment that must be able to
crypto-shred configures a provider deliberately
([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)).

## 3. Install

The application's chart takes this one as a dependency:

```yaml
# the application's Chart.yaml
dependencies:
  - name: audit
    version: 0.1.0
    repository: file://./vendor/audit   # no release yet: vendor the chart until one is published
```

and its values file carries an `audit:` block. Take the body of that block
from the shape you picked — [direct](../deployment/direct.md#values) or
[stream](../deployment/stream.md#values) — which is where every value and its
reason lives. What every installation sets, whichever shape:

| value | what it is |
|---|---|
| `bucket`, `prefix`, `region`, `kmsKey` | the archive, and this application's part of it |
| `profiles` | what copies are kept, each composed from presets |
| `database` | the index, as a Secret holding the URL |
| `query.enabled`, `query.database`, `query.grants` | the read path, its own role, and who may read what |
| `jobs.*` | digest, verify, purge and clock-sync |

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
accept and get quietly wrong: a digest job with no signer, a compliance preset
with no reference clock, the writer's credentials given to the query service,
`mode: stream` with no `stream.url` or no database, an ack wait that does not
outlast the roll, or an extension whose profile the deployment does not
compose.
Each refusal says why, and they are listed in
[the chart README](../../charts/audit/README.md) with
[`values.yaml`](../../charts/audit/values.yaml) commenting every setting.

## 4. Check that it works

1. **The receiver is up.** The chart names its Deployment after the release
   and the dependency — `kubectl -n <app> rollout status deploy/<release>-audit`.
   It refuses to start, with the reason in its log, if the database is at a
   schema version it does not know, the stream is missing, or the holds cannot
   be read.
2. **The application registered its catalogue.** It logs the registration at
   start-up, and refuses to start if the receiver refused the catalogue.
3. **A record goes through.** Perform an action the catalogue declares, or run
   [the example application](../../examples/emit/main.go) against the
   receiver's Service.
4. **It is in the archive.**
   `aws s3 ls s3://example-audit/audit/app/profile=security/ --recursive`
   lists an object per profile, tenant and batch.
5. **The chain seals and verifies.** After the next hour the digest job writes
   under `digest/`, and the following night the verify job records a result
   under `verified/`. An auditor checks the same thing with read access and
   the public key:

   ```sh
   audit verify --profile security --last 24h \
     --bucket example-audit --prefix audit/app --public-key public.pem
   ```

6. **The query service keeps its contract.** With a token that may read:

   ```sh
   audit conformance --query https://audit-query.<app>.svc:8080 \
     --profile security --token-file token
   ```

   Run it after the first records land, and `audit verify` an hour later, so
   that there is a sealed hour to walk.

7. **Alerts.** With `telemetry.otlpEndpoint` set, page on
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
