# Changelog

One version for the whole repository ([record 0016](docs/decisions/0016-distribution-and-the-lambda-shape.md)): every release tags every Go library at the new version and carries the Rust crates at the same version, and each section says which libraries changed. `just release-prepare X.Y.Z` writes the section; [docs/release.md](docs/release.md) is the checklist. The versions start at 0.17.0, above the tags of the archived module.

<!-- release-prepare: new sections go below this line -->

## 0.17.0 - 2026-11-22

One version for the whole repository: every library is tagged 0.17.0, changed or not.

First release. Every library is new:
- `emitter`

What is in it:
- The audit emitter, moved from sluis: the Rust crate `audit-emitter` and the Go module `github.com/truvity/audit/emitter`. Both take the product, release, scope and component as configuration, build the pseudonym through a `Mac` the host supplies, and send records over atoom's transport port.
- The pseudonym domain label is the product's configuration, with no default ([record 0018](docs/decisions/0018-the-pseudonym-domain-label-is-the-products.md)).
- The organisation of a usage record is optional ([record 0017](docs/decisions/0017-the-organisation-of-a-usage-record-is-optional.md)).
- The release flow: a candidate check of the source archive on amd64 and arm64, signed tags, an attested and immutable GitHub release.

