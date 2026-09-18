# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The Unreleased
section describes the state of the repository, not the history of edits.

## [Unreleased]

Foundation. No release.

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
- `index/postgres`: the default index and the shared deduplication table, with
  a checked-in schema, monthly partitions created on demand, and row-level
  security by tenant.
- Deduplication asks before the write and marks after it, so that a crash
  between the two costs a duplicate object rather than a lost record.
- `audit validate`, `audit profile explain`, `audit check-emitters`,
  `audit verify`, `audit replay`, `audit migrate`, `audit reindex`,
  `audit digest`, `audit purge`, `audit clock-sync`.
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
- `audit-writer`, the writer as a service behind Connect.
- Decisions 0001 to 0009 accepted.
- Design, research, reference and operations documents.
