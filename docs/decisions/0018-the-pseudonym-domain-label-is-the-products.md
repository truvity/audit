# 0018. The pseudonym domain label is the product's

Status: accepted, 2026-11-22. Partly supersedes [0011](0011-evidence-encryption-and-pseudonyms.md).

## Context

A pseudonym's input starts with a domain label, before the scope, the tenant, the kind of identity and its identifier ([0011](0011-evidence-encryption-and-pseudonyms.md)). 0011 left the label open: sluis's emitter binds `sluis-pseudonym-v1`, a neutral label would change every pseudonym that sluis's alpha wrote, and the choice was to be made when the emitter moved here.

The emitter has moved: the Go module `github.com/truvity/audit/emitter` and the Rust crate `audit-emitter`, in [emitter/](../../emitter/README.md). Both take the label as configuration, `Pseudonyms.Label` in Go and `Pseudonyms::label` in Rust. It is required and not empty, and neither has a default. sluis is the one product that names people in records today.

## Decision

- **The domain label is per product, and the product configures it.** The emitter keeps no default. A product that configures pseudonyms names its label, and an emitter without one refuses to start, as it does today.
- **sluis keeps `sluis-pseudonym-v1`.** Its pseudonyms stay as they are, so the records its alpha wrote still link to the records it writes from now on, and reversal by the tenant's audit role still finds them.
- **A label is a pseudonym space.** Two products with different labels make different pseudonyms for the same subject. A product that changes its label breaks the link between its records before and after the change, within the same epoch, so a change of label is a decision of that product and not a setting to tidy.
- **Revisit when a second product names people.** That is when a neutral, shared label, or a rule for linking one person across products, can be weighed against real records. Until then no audit-wide label is fixed.

### What this supersedes

[0011](0011-evidence-encryption-and-pseudonyms.md), in part: the point "**Open: the domain label.**" of its section Pseudonyms, and the sentence that the choice is made when the emitter moves here. Everything else in 0011 stands, including the form of the input, the epochs and reversal.

### Built and planned

Built: the label as required configuration, with no default, in both emitters. Planned: nothing. The revisit waits for a second product that names people.

## Consequences

- No pseudonym that sluis wrote changes, and no migration of its records is needed.
- The label is part of a product's configuration of audit, and a reviewer of that configuration checks it as carefully as the key.
- Records of one person from two products do not link by pseudonym. A tenant's audit role that needs both reverses each product's pseudonyms on its own.
- An audit-wide label stays possible: a later record can fix one for new products, without changing the labels already in use.

## Roads not taken

- **A neutral label for every product now,** such as `audit-pseudonym-v1`. It changes every pseudonym that sluis's alpha wrote, for a benefit that only appears when a second product names people.
- **A default label in the emitter.** A product that forgot to set it would silently share a pseudonym space with every other product that forgot, and a later change of the default would change its pseudonyms.
- **The label from the scope or the product name, derived by the emitter.** A rename of a product or a scope would then change its pseudonyms without anyone deciding it.
