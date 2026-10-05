# Documentation

Start with [architecture](explanation/architecture.md): the parts, the catalogue that
binds them, what an acknowledgement means and what can be lost. Then pick
the road you are on.

| you are | start at |
|---|---|
| connecting an application | [integrating](how-to/connect-an-application.md), then [emitting](how-to/emit-records.md) |
| running an installation | [deployment](explanation/deployment-shapes.md), then [deploying](getting-started/kubernetes.md) step by step |
| reading or auditing the trail | [reading](how-to/read-the-trail.md), then [verification](how-to/verify-the-trail.md) |
| working on this repository | [layout](reference/repository-layout.md) and [CONTRIBUTING](../CONTRIBUTING.md) |

| section | pages |
|---|---|
| Entry | [architecture](explanation/architecture.md), [capabilities](reference/capabilities.md), [why](explanation/why.md), [concepts](explanation/concepts.md) |
| Deployment | [the shapes](explanation/deployment-shapes.md), [direct](explanation/direct-mode.md), [stream](explanation/stream-mode.md), [AWS](reference/aws-pulumi-library.md), [billing](how-to/enable-billing.md), [usage quotas](how-to/enable-usage-quotas.md) |
| Guides | [integrating an application](how-to/connect-an-application.md), [deploying](getting-started/kubernetes.md), [emitting records](how-to/emit-records.md), [reading the trail](how-to/read-the-trail.md) |
| Design | [split writer](explanation/split-writer.md), [search](explanation/search.md), [authentication and authorization](explanation/authn-authz.md), [integrity](explanation/integrity.md), [metering](explanation/metering.md), [the Audit page](explanation/audit-page.md) |
| Decisions | [index](decisions/README.md), and the [target architecture](decisions/0016-three-parts-installed-independently.md) |
| Reference | [record](reference/record.md), [catalogue](reference/catalogue.md), [extension points](reference/extension-points.md), [presets](reference/profiles.md), [API](reference/api.md), [configuration](reference/configuration.md), [bucket contract](reference/bucket-contract.md) |
| Operations | [which presets to compose](explanation/which-profiles-to-compose.md), [S3 guide](how-to/prepare-the-bucket.md), [verification](how-to/verify-the-trail.md), [runbook](how-to/rebuild-the-index.md), [telemetry](reference/telemetry.md), [key custody](explanation/key-custody.md), [OpenBAO keys](how-to/configure-openbao-keys.md) |
| Development | [layout, and how to add to it](reference/repository-layout.md) |
| Roadmap | [to do](explanation/roadmap.md) |

The decisions explain why the component is shaped as it is; the design
pages explain each part; the reference pages are the contracts. Operations
is for whoever runs an installation, and the two key pages there apply only
to a deployment that chooses to run a key provider — most do not, since
[0013](decisions/0013-no-pseudonymisation-keys-by-default.md).

The surveys this design rested on — standards, regulation, storage,
metering and comparable products — are no longer carried here. They read as
design and were not, and the decisions that used them quote what they
needed. They remain in the repository's history.
