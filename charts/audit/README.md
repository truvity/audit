# audit

The audit trail, deployable: the split writer, the jobs that seal, verify and
prune what it writes, the catalogue registry, and the query service that reads
it back. The console (the viewer) is not in it yet.

## What it deploys

- **`audit-writer`**, a Deployment serving the sink and, given `stream.url`,
  consuming the wide stream through a durable consumer every replica shares.
- **`audit migrate`**, a pre-install/pre-upgrade hook Job applying the index
  schema before the writer rolls. The writer refuses to start against a schema
  it does not know and never migrates itself.
- **`audit-registry`** (`registry.enabled`), where applications register their
  catalogues at deploy and are refused if the deployment will not have them.
- **`audit-query`** (`query.enabled`), search, facets, get, export, tail and —
  given the keys — resolve, behind the grants in `query.grants`. Every read is
  recorded through the writer. It reads the index as **its own database
  role**, which must not own the tables (`query.database`).
- **CronJobs**: `audit digest` hourly, `audit verify` nightly per profile,
  `audit purge` daily, `audit clock-sync` daily. Each records what it did
  through the writer's own sink.

## What the deployment brings

The chart takes references; it creates none of these.

| thing | value |
|---|---|
| a bucket with Object Lock in compliance mode | `bucket` |
| a writer role that may put objects with a legal hold on (`s3:PutObjectLegalHold`), read and lengthen their retention (`s3:GetObjectRetention`, `s3:PutObjectRetention`), and read `holds/` | the writer's ServiceAccount annotation |
| the pseudonymisation keys: a Secret with the 32-byte root (`local`), or an OpenBAO transit engine and a token whose policy grants the writer's purposes ([policies](../../docs/operations/openbao-keys.md)) | `keys.local.existingSecret`, or `keys.provider: transit` with `keys.transit` |
| the digest signing key: a Secret (PEM, ed25519), an AWS KMS ECC_NIST_P256 key, or an OpenBAO transit ed25519 key | `jobs.digest.signingKey.existingSecret`, `jobs.digest.kmsKey` or `jobs.digest.transit` |
| a Secret with its public half | `jobs.verify.publicKey.existingSecret` |
| a Postgres URL, in a Secret | `database.existingSecret` |
| the JetStream stream, already created | `stream.url`, `stream.name` |
| a `ReadWriteMany` storage class, for more than one replica on `local` keys (transit needs none) | `keys.local.persistence` |
| egress to the NTP references | `jobs.clockSync.ntp` |
| the images | `image.writer`, `image.cli`, `image.registry` — one per binary, built by ko from `.goreleaser.yaml`; distroless, no shell |
| the cluster's service-account issuer, reachable over HTTPS from the pods | `workloadIdentity.issuers` |
| which service account speaks for which source, if the registry is on | `workloadIdentity.workloads` |
| for the query service: a Postgres role with `usage` on the schema and `select` on its tables and nothing else, in a Secret | `query.database.existingSecret` |
| the issuers callers sign in with, and who may read what | `query.grants` ([access](../../docs/guides/read.md#access)) |
| an exports bucket with no Object Lock, if exports are wanted | `query.exports.bucket` |
| a role per component — writer, registry, query, digest, verify — bound through its ServiceAccount's annotations | `serviceAccount`, `registry.serviceAccount`, `query.serviceAccount`, `jobs.*.serviceAccount` |

## Who may write

The writer and the registry verify every caller's projected service-account
token against the cluster's own OIDC issuer. The token's subject is the service
account, which the kubelet vouches for and the workload cannot choose. The
writer stamps it on each record as the observer. The registry maps it to the
source whose catalogue that workload may register, and refuses a workload it
does not list. The chart's own jobs are given projected tokens
(`workloadIdentity.audience`, default `audit`) and present them the same way.

A workload of your own that writes to the writer mounts a projected token with
the same audience and points `AUDIT_TOKEN_FILE` at it, or sets the bearer itself.
`anonymousWrites: true` turns verification off, for a trial install only. The
chart refuses to render with neither issuers nor that flag set.

## What it refuses to render

Thirty configurations, each one the binaries reject at start-up or accept and
get quietly wrong. They are listed with their reasons in
`testdata/refusals.txt`, and `testdata/refuse.sh` holds each refusal to its
words. The two least obvious:

- the `transit` key provider needs an address and exactly one way to the
  token (`keys.transit.token.existingSecret` or `keys.transit.tokenFile`);
- more than one replica with the `local` key provider needs a key directory
  every replica can write, because data keys are random rather than derived —
  separate directories mean a different pseudonym for the same person on each
  replica;
- a key directory that does not persist re-keys every tenant on every restart,
  so turning persistence off takes `keys.local.ephemeralIsAcceptable: true`,
  not a flag.

## Checking it

```
just chart          # lint, every refusal, golden renders
audit verify --profile <p> --last 24h --bucket <b> --public-key <file>
```

The second needs read access to the archive and the public key, and nothing
that has to be trusted.
