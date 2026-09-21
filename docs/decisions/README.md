# Decisions

Architecture decision records in the MADR format. A decision here is one
that any deployer of this component would also face. Deployment-specific
choices are recorded by the deployer.

| id | title | status |
|---|---|---|
| [0001](0001-record-schema-proto-with-json-schema-slots.md) | Record schema in Protocol Buffers with JSON Schema extension slots | accepted |
| [0002](0002-profiles-and-framework-presets.md) | Profiles composed from framework presets, one copy per profile | accepted |
| [0003](0003-s3-object-lock-as-the-record.md) | S3 Object Lock in compliance mode is the record; everything else is a projection | accepted |
| [0004](0004-sink-interface-and-transports.md) | One sink interface at every hop; the queue is invisible | accepted; delivery modes superseded by [0012](0012-two-deliveries-and-a-durable-ack.md) |
| [0005](0005-identity-tiers-and-pseudonymisation.md) | Identity tiers and per-purpose pseudonymisation in the split writer | accepted |
| [0006](0006-search-contract.md) | Search contract: DNF typed predicates, cursor page object, tail cursor | accepted |
| [0007](0007-authentication-and-authorization-plug-points.md) | Pluggable authentication and authorization with declarative defaults | accepted |
| [0008](0008-digest-chain-and-verification.md) | Hourly signed digest chain and an auditor-run verify command | accepted |
| [0009](0009-versioning-policy.md) | Versioning: package per major, major.minor on the record, decoders forever | accepted |
| [0010](0010-key-providers.md) | Key providers: local, OpenBAO transit and AWS KMS envelope, behind one interface | accepted; the default is now `none` per [0013](0013-no-pseudonymisation-keys-by-default.md) |
| [0011](0011-one-installation-per-service-or-product.md) | One installation per service or product, in that application's namespace | accepted |
| [0012](0012-two-deliveries-and-a-durable-ack.md) | Two deliveries, and the receiver's acknowledgement means durable | accepted |
| [0013](0013-no-pseudonymisation-keys-by-default.md) | No pseudonymisation keys by default | accepted |

Template: [0000-template.md](0000-template.md).
