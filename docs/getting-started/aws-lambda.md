# Getting started on AWS Lambda

From nothing to a writer Lambda archiving records into S3 and a notary Lambda sealing the
hours, built by the Pulumi library. About an hour with an AWS account and a Pulumi stack.

The shape: an application sends records to an SQS ingest queue; the writer Lambda archives
them in S3 (deduplicating in DynamoDB); the notary Lambda seals each closed hour with a KMS
key. How it works is in [AWS Lambda](../explanation/aws-lambda.md); every input is in the
[library reference](../reference/aws-pulumi-library.md).

## What you need

- A Pulumi (Go) program and an AWS account and region.
- The release's two zips and their checksums, from the GitHub release of this version:
  `audit-writer-lambda_<version>_linux_arm64.zip`, `audit-notary-lambda_<version>_linux_arm64.zip`
  and `checksums.txt`. The library at the same release (`go get github.com/truvity/audit/deploy/pulumi@v<version>`).
- The ARN of the IAM **role** of whatever will send records (not a session ARN).
- The emitting application's catalogue directory (its document and its JSON schemas), or
  nothing yet if you only want to see the stack come up.

## 1. Write the profile document

The writer reads one document naming which profiles it keeps. Save it as a string for
`Writer.DeploymentYAML`:

```yaml
apiVersion: audit.truvity.github.io/audit-deployment/v2
external_identifiers_are_opaque: true
profiles:
  security:
    presets: [security]   # `presets` is the key's name; each is a framework profile
```

Which framework profiles to compose is a policy decision
([which profiles to compose](../explanation/which-profiles-to-compose.md)); `security` is
the one every installation composes.

## 2. Write the stack

```go
import auditpulumi "github.com/truvity/audit/deploy/pulumi"

a, err := auditpulumi.New(ctx, "audit", &auditpulumi.Args{
	Archive: auditpulumi.ArchiveArgs{
		BucketName:     "acme-audit-trial",
		Profiles:       []string{"security"},
		ObjectLockMode: auditpulumi.None, // required: NONE, GOVERNANCE or COMPLIANCE
	},
	Ingest: auditpulumi.IngestArgs{
		// Required: the whole of who may send. Role or user ARNs, never session ARNs.
		Senders: []pulumi.StringInput{emitterRoleArn},
		// An operator's break-glass role, for redriving the DLQ (optional).
		Redrivers: []pulumi.StringInput{breakGlassRoleArn},
	},
	Writer: auditpulumi.WriterArgs{
		Package:        "dist/audit-writer-lambda_<version>_linux_arm64.zip",
		PackageSHA256:  writerSHA, // the line of checksums.txt, pinned in this file
		DeploymentYAML: deploymentYAML,
		CatalogueDirs:  []string{"catalogue/shop"}, // a document and its *.json schemas
	},
	Notary: auditpulumi.NotaryArgs{
		Package:       "dist/audit-notary-lambda_<version>_linux_arm64.zip",
		PackageSHA256: notarySHA,
	},
})
ctx.Export("queueUrl", a.QueueURL)
```

Three things that bite, each with its own page:

- **`Senders` is required.** The queue policy denies `sqs:SendMessage` to everyone not named,
  so this list is the writer's authenticity on this path. A stack that names none fails at
  preview ([run readers in Kubernetes](../how-to/aws-run-readers-in-kubernetes.md) has the
  sender rules).
- **The catalogue is a document plus its schemas.** The writer refuses to start without the
  schemas the document references ([change what a source records](../how-to/change-what-a-source-records.md)).
- **Library and zip must be the same release**, or the preview fails
  ([ship a release](../how-to/aws-ship-a-release.md)).

If the writer needs a secret (an OpenBAO token for pseudonymisation keys), it is an SSM
SecureString under the root `/audit/<name>/private/config`, never an environment variable
([store the writer's secrets in SSM](../how-to/aws-store-secrets-in-ssm.md)). A trial with
`keys.provider: none` (the default) needs none.

## 3. Deploy

```sh
pulumi preview   # the guards run first: package digest, release match, catalogues
pulumi up
```

Expected: a bucket, a KMS key for the archive and one for seals, the ingest queue and its
DLQ, a DynamoDB table, two functions, an hourly schedule and the alarms. Verify:
`pulumi stack output queueUrl` prints the queue URL.

## 4. Send a record

Point an emitter at the queue: a component that records takes `sink: {sqs: {queueUrl, region}}`
and its pod or role is one of `Senders`. The emitter registers nothing here: the writer
already has the catalogue from step 2.

Verify, a minute later:

```sh
aws s3 ls "s3://acme-audit-trial/records/security/" --recursive
audit verify --profile security --last 1h --bucket acme-audit-trial
```

and the trail holds an `audit.writer.started` record. If the DLQ alarm
(`audit-ingest-dlq-not-empty`) or the `audit-writer-unknown-catalogue` alarm fires, see
[redrive the ingest DLQ](../how-to/redrive-the-ingest-dlq.md).

## 5. Next

- Turn the lock on when the retentions are signed off
  ([turn the Object Lock on](../how-to/aws-turn-on-object-lock.md)).
- Read the trail with observe and query in a cluster
  ([run readers in Kubernetes](../how-to/aws-run-readers-in-kubernetes.md)).
- Send the functions' telemetry ([send the Lambda functions' telemetry](../how-to/aws-send-lambda-telemetry.md)).
- Upgrading from an earlier release: [the v0.13 upgrade](../how-to/upgrade/v0.13.md).
