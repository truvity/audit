# 0015. Configuration kinds, declared through atoom's `state` registry

Status: accepted, 2026-11-21

## Context

The audit pieces need documents: which projects exist, with which bucket, lock, retention and tiers; which witnesses cosign which log; which catalogue of actions is in force; which meters exist. These documents must be reviewed, signed and verified before a piece uses them, and the pieces must not write them.

In sluis these documents ride on the kind registry of its store actor. audit must not need sluis to have them. atoom's [`state` block](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md) is the core of that actor: addresses, kinds, signed-config checks, layered reads, watch and the bindings per storage class. It is built from the `store` and `source` ports.

## Decision

- **The `state` block has a kind registry that any product extends.** A product declares its own kinds to it. audit declares four. sluis's kinds are not audit's, and audit's are not sluis's. The registry is planned work in atoom ([plan](https://github.com/truvity/atoom/blob/master/docs/plan.md#11-after-020-the-blocks-phase), step B7).
- **The audit pieces read their documents through the block,** which loads and verifies them like every other signed configuration. The pieces write nothing through it. The blob, the transport and the index are ports of their own.
- **A document is reviewed and signed before use.** The block verifies the publisher's signature against the pins of the installation, and keeps the last good set when a reload fails.

### The four kinds

| Kind | Items | What it declares | Written by |
|---|---|---|---|
| `AuditLog` | one per project | The project's bucket, its logs, their lock mode, lifecycle tiers, compliance profiles and retention ([0010](0010-lock-modes-retention-and-compliance-floors.md)) | The operator, by reviewed change |
| `AuditWitnesses` | one per log | The witnesses of the log, their driver (`none`, `peer`, `external`) and the cosignatures required ([0009](0009-witnesses-and-verification.md)) | The operator |
| `AuditCatalogue` | one per project, named `default` | The action table of the newest release with its generated schemas, the release it covers, and the version of the schema policy ([0004](0004-the-record-format.md)) | The publishing pipeline of the product |
| `Meter` | one per meter | A name, a unit and a kind ([0007](0007-meters.md)) | The product, by reviewed change |

- **`AuditCatalogue` is a singleton for each project.** One item, `default`, covers every release of a major version, because the table only grows within a major version. The publishing pipeline signs the table and its schemas as one item, so they move together in one version.
- **`Meter` is a fixed document.** A new meter is a reviewed, signed addition, and a meter is never changed, only retired.
- **A kind's schema is part of the registry,** so the block refuses a document that does not fit it.

Reference: [reference/config-kinds.md](../reference/config-kinds.md).

### Built and planned

Built: the four kinds as kinds of sluis's store actor, with the rules above (in sluis). Planned: the registry in atoom's block, and the move of the four kinds to it.

## Consequences

- audit has no coupling to sluis's registry. A product extends the same registry with its own kinds.
- The `state` block's design must include extensible kinds. That is the work that this decision asks of atoom.
- Documents that the pieces read are always signed. A piece starts only with a valid set.

## Roads not taken

- **The kinds stay in sluis's registry.** audit would need sluis to run.
- **Each piece reads a plain file or an environment variable.** No signature, no review, and no common loading rules. atoom has no environment-variable configuration driver.
- **The pieces write their own documents through the store.** The store would become a second home for evidence-adjacent state.
- **One item per release in the catalogue.** It keeps every old table forever and needs a keyed kind and a lookup by release for no record that the newest does not already cover.
