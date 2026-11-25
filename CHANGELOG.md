# Changelog

One version for the whole repository ([record 0016](docs/decisions/0016-distribution-and-the-lambda-shape.md)): every release tags every Go library at the new version and carries the Rust crates at the same version, and each section says which libraries changed. `just release-prepare X.Y.Z` writes the section; [docs/release.md](docs/release.md) is the checklist. The versions start at 0.17.0, above the tags of the archived module.

<!-- release-prepare: new sections go below this line -->

## Unreleased

- atoom 0.7.0: the emitter moves from atoom 0.5.0 to 0.7.0 (the Rust crates `atoom-common`, `atoom-socket` and `atoom-transport`, and the Go modules `github.com/truvity/atoom/common` and `github.com/truvity/atoom/transport`), one copy of atoom in each build. atoom's transport port and wire are the same, so records do not change, and no code changes: the breaking changes of 0.7.0 are in libraries the emitter does not use (`key/software`, `key/yubikeypiv`, `key/pinrelease`, `key/slotplan`). The correlation header is now `Atoom-Correlation-Id` (a 0.7.x server still reads the old `Sluis-Correlation-Id` after it); the emitter names neither and reads the id through atoom's call context (`call.CorrelationID`, `atoom_socket::call::correlation`), so a product on atoom 0.7.0 gets the id its server read.
- [Record 0019](docs/decisions/0019-floors-are-a-declared-switch.md), partly superseding 0010 and 0006: each log declares `floors: enforce | report` in `AuditLog`, `enforce` when absent, and the configuration check takes no scope, installation or environment name as a switch; "in a production scope" is gone. REQ-AUDIT-058, 059, 060, 136, 137 and 172 are withdrawn and replaced by REQ-AUDIT-196 to 201, with REQ-AUDIT-202 (no `floors` is `enforce`) and REQ-AUDIT-203 (no name is a switch).

## 0.19.0 - 2026-11-24

One version for the whole repository: every library is tagged 0.19.0, changed or not.

Libraries that changed since v0.18.0:
- `emitter`

What is in it:
- atoom 0.5.0: both libraries move from atoom 0.4.0 to 0.5.0 (the Rust crates `atoom-common`, `atoom-socket` and `atoom-transport`, and the Go modules `github.com/truvity/atoom/common` and `github.com/truvity/atoom/transport`). atoom's transport port and wire are the same in both releases, so records do not change. The emitter names no variant of `atoom_socket::ResponseBody`, which atoom 0.5.0 makes opaque, so no code changes. A product on atoom 0.5.0 now gets one copy of atoom in its Rust build, and the emitter's default reader of the correlation id reads that product's call context.

## 0.18.0 - 2026-11-24

One version for the whole repository: every library is tagged 0.18.0, changed or not.

Libraries that changed since v0.17.0:
- `emitter`

What is in it:
- atoom 0.4.0: both libraries move from atoom 0.3.0 to 0.4.0 (the Rust crates `atoom-common`, `atoom-socket` and `atoom-transport`, and the Go modules `github.com/truvity/atoom/common` and `github.com/truvity/atoom/transport`). The transport port is the same in both releases, so records and the wire do not change. A product on atoom 0.4.0 now gets one copy of atoom in its Rust build: with two, the emitter's default reader of the correlation id read the other copy's call context and records lost `metadata.correlation_uid`. A new test emits inside a handler of an `atoom-socket` server and checks that the id the caller sent reaches the record.

## 0.17.0 - 2026-11-22

One version for the whole repository: every library is tagged 0.17.0, changed or not.

First release. Every library is new:
- `emitter`

What is in it:
- The audit emitter, moved from sluis: the Rust crate `audit-emitter` and the Go module `github.com/truvity/audit/emitter`. Both take the product, release, scope and component as configuration, build the pseudonym through a `Mac` the host supplies, and send records over atoom's transport port.
- The pseudonym domain label is the product's configuration, with no default ([record 0018](docs/decisions/0018-the-pseudonym-domain-label-is-the-products.md)).
- The organisation of a usage record is optional ([record 0017](docs/decisions/0017-the-organisation-of-a-usage-record-is-optional.md)).
- The release flow: a candidate check of the source archive on amd64 and arm64, signed tags, an attested and immutable GitHub release.

