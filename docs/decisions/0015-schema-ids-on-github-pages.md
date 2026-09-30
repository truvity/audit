# 0015. Schema identifiers on GitHub Pages, the old ones kept as aliases

- Status: accepted
- Date: 2026-09-30

## Context

Every schema this project publishes names itself by `$id`: the record's
JSON Schema, the catalogue, preset and extension meta-schemas, and the
common catalogue's payload schemas. Those identifiers were under
`https://schemas.truvity.com/audit/v1/`, a name that was never served, and
that belongs to the publisher's organisation rather than to this
repository. An identifier that does not resolve makes the contract
unreadable to anyone outside, and one under a domain the project does not
control ties every archived record to it.

Two facts constrain the change. Archived catalogues and schemas are locked
for years and cannot be rewritten ([0009](0009-versioning-policy.md):
decoders are kept for everything ever written), and they carry the old
identifiers, both as a schema's own `$id` and as the `data_schema` and
`*_schema` references in a catalogue document.

## Decision

- The base is `https://truvity.github.io/audit/schemas/v1/`. The record
  schema, the three meta-schemas and the common payload schemas
  (`common/*.json`) are generated or written under it, and the generator,
  the committed catalogue and the docs use it.
- GitHub Pages serves those files at exactly those URLs.
  `.github/workflows/pages.yaml` deploys them on a change to a schema;
  `hack/pages-site.sh` assembles the site and fails when a file's `$id` is
  not the URL it would be served at, and `just check` runs it.
- The old base is an alias. `catalogue.CanonicalID` maps an id under
  `https://schemas.truvity.com/audit/v1/` onto the same path under the new
  base; the loader keys every schema by the canonical id and canonicalises
  every reference before looking it up. A catalogue and schemas from an
  archive, written entirely under the old identifiers, or a mixture of the
  two forms, load as before. Nothing is rewritten: an archived document is
  read and re-archived byte for byte, still saying what it said.
- Only the exact legacy prefix is aliased. An adopter's own schemas keep
  whatever identifiers they chose.
- The alias is kept for as long as any archive written before this change
  can be read, which by 0009 is the retention period. It is removed only
  when no object under the old identifiers remains.

## Consequences

- An `$id` resolves, and a validator outside Go can fetch the schema by the
  name a record's catalogue gives it.
- The site's URL depends on the repository's name and owner. Moving the
  repository moves the identifiers; that would be a new decision and
  another alias, not a silent change.
- A catalogue's bytes are part of what its version means, so the common
  catalogue's changed identifiers are a new version, `1.0.1`; one archived
  as `1.0.0` keeps its bytes and still loads through the alias.
- Pages must be enabled on the repository, a setting owned by whoever
  manages the repository's configuration.

## Alternatives considered

- **Keep the organisation domain and serve it.** Needs a hosting
  arrangement outside this repository and ties the identifiers to it.
- **Rewrite archived documents.** Impossible under the lock, and it would
  change the bytes an auditor verifies.
- **Keep the old identifiers forever and add a redirect.** Leaves the
  unserved name as the canonical one, which is the fault being removed.
