# Decisions

Architecture decision records in the MADR format. A decision here is one that any deployer of this
component would also face. Deployment-specific choices are recorded by the deployer. The body of a
decision is never edited after acceptance; a superseded or refined decision has **its own Status line
changed** to say so, and this index repeats it.

| id | title | Status |
|---|---|---|
| [0001](0001-record-schema-proto-with-json-schema-slots.md) | Record schema in Protocol Buffers with JSON Schema extension slots | accepted |
| [0002](0002-profiles-and-framework-presets.md) | Profiles composed from framework presets, one copy per profile | accepted; the compliance bundle this record calls a framework preset is now a *framework profile* (a *preset* is a named bundle of adapter or deployment choices, per truvity/policy decision 0012); the `presets/` directory and key keep the old name until a code change renames them |
| [0003](0003-s3-object-lock-as-the-record.md) | S3 Object Lock in compliance mode is the record; everything else is a projection | accepted; the object layout is superseded by [0018](0018-v1-bucket-layout.md), and retention is extended by [0023](0023-archive-retention-and-lifecycle.md) |
| [0004](0004-sink-interface-and-transports.md) | One sink interface at every hop; the queue is invisible | accepted; the delivery modes are superseded by [0012](0012-two-deliveries-and-a-durable-ack.md), itself superseded by [0017](0017-sink-durability-and-transports.md), which extends this |
| [0005](0005-identity-tiers-and-pseudonymisation.md) | Identity tiers and per-purpose pseudonymisation in the split writer | accepted |
| [0006](0006-search-contract.md) | Search contract: DNF typed predicates, cursor page object, tail cursor | accepted |
| [0007](0007-authentication-and-authorization-plug-points.md) | Pluggable authentication and authorization with declarative defaults | accepted |
| [0008](0008-digest-chain-and-verification.md) | Hourly signed digest chain and an auditor-run verify command | superseded by [0019](0019-seals.md) |
| [0009](0009-versioning-policy.md) | Versioning: package per major, major.minor on the record, decoders forever | accepted; refined by [0025](0025-configuration-is-immutable-per-instance.md) for configuration |
| [0010](0010-key-providers.md) | Key providers: local, OpenBAO transit and AWS KMS envelope, behind one interface | accepted; the default is `none` per [0013](0013-no-pseudonymisation-keys-by-default.md) |
| [0011](0011-one-installation-per-service-or-product.md) | One installation per service or product, in that application's namespace | accepted; refined by [0016](0016-three-parts-installed-independently.md) |
| [0012](0012-two-deliveries-and-a-durable-ack.md) | Two deliveries, and the receiver's acknowledgement means durable | superseded by [0017](0017-sink-durability-and-transports.md) |
| [0013](0013-no-pseudonymisation-keys-by-default.md) | No pseudonymisation keys by default | accepted; supersedes the default of [0010](0010-key-providers.md) |
| [0014](0014-lock-modes-and-store-tiers.md) | Lock modes and store tiers: the lock is demanded where a framework demands it | accepted |
| [0015](0015-schema-ids-on-github-pages.md) | Schema identifiers on GitHub Pages, the old ones kept as aliases | accepted |
| [0016](0016-three-parts-installed-independently.md) | Three parts, installed independently; the bucket layout is the contract | accepted; refines [0011](0011-one-installation-per-service-or-product.md) |
| [0017](0017-sink-durability-and-transports.md) | Sink durability: the acknowledgement says how durable, and the start-up refuses less | accepted; supersedes [0012](0012-two-deliveries-and-a-durable-ack.md) and extends [0004](0004-sink-interface-and-transports.md) |
| [0018](0018-v1-bucket-layout.md) | The v1 bucket layout, and v0 is dropped | accepted; supersedes the object layout of [0003](0003-s3-object-lock-as-the-record.md) |
| [0019](0019-seals.md) | Seals: JOSE ES384, chained, per profile, tenant and hour | accepted; supersedes [0008](0008-digest-chain-and-verification.md) |
| [0020](0020-observe-follows-the-bucket.md) | Observe follows the bucket by cursor; notifications only wake it | accepted |
| [0021](0021-one-validated-configuration-file.md) | One configuration file, validated against a schema | accepted; refined by [0025](0025-configuration-is-immutable-per-instance.md); the secret fields it names (`<field>Env`) are `<field>Secret` through `secrets.source` in configuration version 2 (truvity/policy decision 0012; [upgrade](../how-to/upgrade/v0.13.md)) |
| [0022](0022-contracts-proto-and-connect.md) | Contracts are proto and Connect; Lambda RPCs are unary | accepted |
| [0023](0023-archive-retention-and-lifecycle.md) | Archive retention: Object Lock compliance as the target, governance first | accepted; extends [0003](0003-s3-object-lock-as-the-record.md) and [0014](0014-lock-modes-and-store-tiers.md) |
| [0024](0024-indexer-and-query-are-separate-processes.md) | The indexer and the query service are separate processes, under separate database roles | accepted |
| [0025](0025-configuration-is-immutable-per-instance.md) | Configuration is immutable per instance; credentials and State are read live | accepted; refines [0021](0021-one-validated-configuration-file.md) and [0009](0009-versioning-policy.md) for configuration |

Template: [0000-template.md](0000-template.md).
