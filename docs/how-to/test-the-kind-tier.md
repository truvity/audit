# Testing the kind tier

This repository's chart is proved end to end on the shared kind tier
truvity/policy's own example established: a disposable Kubernetes cluster,
stood up from nothing on a GitHub-hosted runner (or a laptop's own kind
cluster), carrying plain servers — Postgres, NATS with JetStream, an S3
stand-in, a local registry — and no operator. See truvity/policy's
`hack/kind/README.md` for what the box is and why it carries servers only,
and its `docs/decisions/0005-kind-is-the-gate.md` for the design this
follows.

## What is in this repository, and where

- `charts/audit/testdata/values/e2e.yaml` — the ONE values file both
  `e2e/fixture` and the chart install read. A name changed in one without
  the other is what `e2e/fixture/drift_test.go` exists to catch, by
  rendering the chart against this exact file.
- `e2e/fixture` — stands in for the platform: the database and its two
  roles (a writer role that owns the schema, and a query role the migrate
  job's `reader` grant lets read it and nothing else), the JetStream
  stream and the archive bucket.
  `e2e/fixture/apply.sh` provisions it; `e2e/fixture/names.go` is what
  reads the values file so nothing is named twice.
- `e2e/suite` — the Go suite that proves the chart works, through Service
  endpoints, using `github.com/truvity/gemaal/pkg/harness` the same way
  truvity/policy's own example suite does.
- `hack/e2e-snapshot.sh` — builds this repository's images and packages its
  chart exactly as a release does (`.goreleaser.yaml` itself, then
  `helmctl`), one architecture, into the box's own local registry.

Run the whole tier locally with `just e2e-all` against an existing
`kind-policy` cluster (this repository owns no cluster of its own — bring
truvity/policy's box up first with `just cluster` there). In CI, the shared
integration workflow stands the box up itself for every pull request: see
`.github/workflows/ci.yaml`'s `cluster` job.

## What this proves

- A record emitted over the writer's Service, in `mode: stream`, crosses
  JetStream and reaches the archive and the Postgres index — read back
  through the QUERY role, proving the migrate job's reader grant actually
  works (`e2e/suite/write_test.go`).
- The query role can read the index and cannot write to it
  (`e2e/suite/roles_test.go`).
- The chart still honours every name the fixture gave it — a chart-side
  rename would otherwise surface only as a failed install
  (`e2e/fixture/drift_test.go`).

## What this deliberately does not prove, and why

- **That the archive is verified or sealed.** The check of the v1 bucket
  contract lives in `internal/bucketcontract`, which is also the conformance
  suite: it runs against the memory store and against LocalStack S3 (the CI
  `s3` job, `just test-s3`), with no cluster at all; the seals' own suite is `internal/bucketcontract/seals_test.go`.
- **That the archive is tamper-evident under Object Lock.** This tier
  installs with `lockMode: none`: LocalStack Community's Object Lock
  support is partial, and the `security` profile this tier composes is one
  of the framework profiles whose framework does not demand a lock
  (`presets/security.yaml`: `integrity.object_lock_mode: none`). A
  deployment composing a profile that DOES demand one (`pci-dss`,
  `nen-7513`, `dora`, `evidence-etsi`) is proved on a real bucket with
  Object Lock enabled — this tier cannot stand in for that, and does not
  try to.
- **The query service's own HTTP API** (search, facets, get, export,
  resolve). It authorises a caller by a token from a trusted OIDC issuer
  (`query.grants.issuers`), and this box carries no issuer to mint one
  against. `e2e/suite/write_test.go` instead proves the write and index
  path directly against the query role's own Postgres view — the same
  data the query service would answer from — the way truvity/policy's own
  suite reaches its box's Postgres directly for what its chart install
  cannot otherwise prove.
