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
- `audit validate`, `audit profile explain`, `audit check-emitters`,
  `audit verify`, `audit replay`.
- `audit-writer`, the writer as a service behind Connect.
- Decisions 0001 to 0009 accepted.
- Design, research, reference and operations documents.
