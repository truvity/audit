# Documentation

Organised by what you are doing ([the contract](https://github.com/truvity/policy/blob/master/docs/contracts/docs.md)).
Pick the road you are on.

| you are | start at |
|---|---|
| installing audit on Kubernetes | [Getting started on Kubernetes](getting-started/kubernetes.md) |
| installing audit on AWS Lambda | [Getting started on AWS Lambda](getting-started/aws-lambda.md) |
| connecting a sluis install | [Getting started with a sluis-connected install](getting-started/sluis.md) |
| connecting an application | [Connect an application](how-to/connect-an-application.md), then [emit records](how-to/emit-records.md) |
| reading or auditing the trail | [Read the trail](how-to/read-the-trail.md), then [verify the trail](how-to/verify-the-trail.md) |
| upgrading | [the upgrade pages](how-to/upgrade/v0.13.md), linked from the [CHANGELOG](../CHANGELOG.md) |
| working on this repository | [repository layout](reference/repository-layout.md) and [CONTRIBUTING](../CONTRIBUTING.md) |

## Getting started

One tutorial per deployment shape: [Kubernetes](getting-started/kubernetes.md), [AWS Lambda](getting-started/aws-lambda.md), [sluis-connected](getting-started/sluis.md).

## How-to

Tasks and runbooks. A runbook has one template: purpose, preconditions, before you start (the traps), steps (with expected output, verify and rollback), afterwards.

| task | page |
|---|---|
| Prepare | [the bucket](how-to/prepare-the-bucket.md), [the database](how-to/prepare-the-database.md) |
| Connect | [an application](how-to/connect-an-application.md), [emit records](how-to/emit-records.md), [read the trail](how-to/read-the-trail.md), [change what a source records](how-to/change-what-a-source-records.md) |
| Run | [the chart in stream mode](how-to/run-stream-mode.md), [enable billing](how-to/enable-billing.md), [enable usage quotas](how-to/enable-usage-quotas.md), [configure OpenBAO keys](how-to/configure-openbao-keys.md) |
| AWS Lambda | [ship a release](how-to/aws-ship-a-release.md), [store secrets in SSM](how-to/aws-store-secrets-in-ssm.md), [turn the Object Lock on](how-to/aws-turn-on-object-lock.md), [switch the archive's encryption](how-to/aws-switch-archive-encryption.md), [run readers in Kubernetes](how-to/aws-run-readers-in-kubernetes.md), [send telemetry](how-to/aws-send-lambda-telemetry.md), [redrive the ingest DLQ](how-to/redrive-the-ingest-dlq.md) |
| Operate | [recover from an outage](how-to/recover-from-an-outage.md), [replay dead-lettered records](how-to/replay-dead-lettered-records.md), [repair or rebuild the index](how-to/rebuild-the-index.md), [diagnose a scheduled job](how-to/diagnose-a-scheduled-job.md), [read from the replica](how-to/read-from-the-replica.md) |
| Verify and hold | [verify the trail](how-to/verify-the-trail.md), [investigate a failed verification](how-to/investigate-a-failed-verification.md), [place a legal hold](how-to/place-a-legal-hold.md) |
| Keys | [erase a tenant's keys](how-to/erase-a-tenants-keys.md), [fix the key directory](how-to/fix-the-key-directory.md) |
| Upgrade | [v0.13](how-to/upgrade/v0.13.md), [v0.6](how-to/upgrade/v0.6.md) |
| Contribute | [test the kind tier](how-to/test-the-kind-tier.md) |

## Reference

| area | pages |
|---|---|
| Configuration | [the file and shared blocks](reference/configuration.md), [the writer](reference/configuration-writer.md), [observe and query](reference/configuration-observe-query.md), [the jobs](reference/configuration-jobs.md), [chart values](reference/chart-values.md), [the emitter library](reference/emitter-library.md) |
| Contracts | [record](reference/record.md), [catalogue](reference/catalogue.md), [extension points](reference/extension-points.md), [API](reference/api.md), [bucket contract](reference/bucket-contract.md) |
| Profiles and verification | [profiles](reference/profiles.md), [`audit verify`](reference/verify-command.md), [archive prefixes and IAM](reference/archive-prefixes-and-iam.md) |
| Platforms | [AWS Pulumi library](reference/aws-pulumi-library.md), [capabilities](reference/capabilities.md), [telemetry](reference/telemetry.md), [repository layout](reference/repository-layout.md) |

## Explanation

[Architecture](explanation/architecture.md), [why](explanation/why.md), [concepts](explanation/concepts.md),
[deployment shapes](explanation/deployment-shapes.md), [levels](explanation/levels.md),
[direct mode](explanation/direct-mode.md), [stream mode](explanation/stream-mode.md),
[AWS Lambda](explanation/aws-lambda.md), [split writer](explanation/split-writer.md),
[search](explanation/search.md), [authentication and authorization](explanation/authn-authz.md),
[integrity](explanation/integrity.md), [key custody](explanation/key-custody.md),
[which profiles to compose](explanation/which-profiles-to-compose.md), [metering](explanation/metering.md),
[billing](explanation/billing.md), [usage quotas](explanation/usage-quotas.md),
[the Audit page](explanation/audit-page.md), [roadmap](explanation/roadmap.md).

## Decisions

[The index](decisions/README.md), with a Status for each, and the
[target architecture](decisions/0016-three-parts-installed-independently.md).
