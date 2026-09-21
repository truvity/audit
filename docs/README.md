# Documentation

Start with [architecture](architecture.md): the parts, the catalogue that
binds them, what an acknowledgement means and what can be lost. Then pick
the road you are on.

| you are | start at |
|---|---|
| connecting an application | [integrating](guides/integrate.md), then [emitting](guides/emit.md) |
| running an installation | [deployment](deployment/README.md), then [deploying](guides/deploy.md) step by step |
| reading or auditing the trail | [reading](guides/read.md), then [verification](operations/verify.md) |
| working on this repository | [layout](development/layout.md) and [CONTRIBUTING](../CONTRIBUTING.md) |

| section | pages |
|---|---|
| Entry | [architecture](architecture.md), [why](why.md), [concepts](concepts.md) |
| Deployment | [the shapes](deployment/README.md), [direct](deployment/direct.md), [stream](deployment/stream.md), [billing](deployment/extensions/billing.md), [usage quotas](deployment/extensions/quotas.md) |
| Guides | [integrating an application](guides/integrate.md), [deploying](guides/deploy.md), [emitting records](guides/emit.md), [reading the trail](guides/read.md) |
| Design | [split writer](design/split-writer.md), [search](design/search.md), [authentication and authorization](design/authn-authz.md), [integrity](design/integrity.md), [metering](design/metering.md), [the Audit page](design/audit-page.md) |
| Decisions | [index](decisions/README.md) |
| Reference | [record](reference/record.md), [catalogue](reference/catalogue.md), [extension points](reference/extension-points.md), [presets](reference/presets.md), [API](reference/api.md), [configuration](reference/configuration.md) |
| Operations | [which presets to compose](operations/presets-policy.md), [S3 guide](operations/s3-guide.md), [verification](operations/verify.md), [runbook](operations/runbook.md), [key custody](operations/key-custody.md), [OpenBAO keys](operations/openbao-keys.md) |
| Development | [layout, and how to add to it](development/layout.md) |

The decisions explain why the component is shaped as it is; the design
pages explain each part; the reference pages are the contracts. Operations
is for whoever runs an installation, and the two key pages there apply only
to a deployment that chooses to run a key provider — most do not, since
[0013](decisions/0013-no-pseudonymisation-keys-by-default.md).

The surveys this design rested on — standards, regulation, storage,
metering and comparable products — are no longer carried here. They read as
design and were not, and the decisions that used them quote what they
needed. They remain in the repository's history.
