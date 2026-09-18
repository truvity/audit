# audit

The write path of the audit trail, deployable: the split writer, and the jobs
that seal, verify and prune what it writes. The read path is a separate chart
to come.

## What it deploys

- **`audit-writer`**, a Deployment serving the sink and, given `stream.url`,
  consuming the wide stream through a durable consumer every replica shares.
- **`audit migrate`**, a pre-install/pre-upgrade hook Job applying the index
  schema before the writer rolls. The writer refuses to start against a schema
  it does not know and never migrates itself.
- **CronJobs**: `audit digest` hourly, `audit verify` nightly per profile,
  `audit purge` daily, `audit clock-sync` daily. Each records what it did
  through the writer's own sink.

## What the deployment brings

The chart takes references; it creates none of these.

| thing | value |
|---|---|
| a bucket with Object Lock in compliance mode | `bucket` |
| a Secret with the 32-byte key root | `keys.local.existingSecret` |
| a Secret with the digest signing key (PEM, ed25519) | `jobs.digest.signingKey.existingSecret` |
| a Secret with its public half | `jobs.verify.publicKey.existingSecret` |
| a Postgres URL, in a Secret | `database.existingSecret` |
| the JetStream stream, already created | `stream.url`, `stream.name` |
| a `ReadWriteMany` storage class, for more than one replica | `keys.local.persistence` |
| egress to the NTP references | `jobs.clockSync.ntp` |
| the images | `image.writer`, `image.cli` — one per binary, built by ko from `.goreleaser.yaml`; distroless, no shell |

## What it refuses to render

Twelve configurations, each one the binaries reject at start-up or accept and
get quietly wrong. They are listed with their reasons in
`testdata/refusals.txt`, and `testdata/refuse.sh` holds each refusal to its
words. The two least obvious:

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
