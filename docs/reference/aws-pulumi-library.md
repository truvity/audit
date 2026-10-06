# AWS Pulumi library reference

The inputs, outputs, resources, IAM and alarms of `github.com/truvity/audit/deploy/pulumi`,
a module of its own so that Pulumi is not in the root module's dependency graph. How the
functions behave and why is in [AWS Lambda](../explanation/aws-lambda.md); to deploy, start
with [the AWS Lambda tutorial](../getting-started/aws-lambda.md).

**Nothing here is deployed by this repository.** The library is tested against Pulumi's mocks
(`just pulumi-test`): it declares the right resources with the right arguments and creates
none. See [capabilities](capabilities.md) for what has run in an account.

## The AWS provider

The library makes one invoke, `aws.GetCallerIdentity`, for the account the seal
key's policy names. It is made **through the component**, so it uses the provider
the caller gave `New`, and not the default one, which a stack may have disabled
(`pulumi:disable-default-providers`, as the truvity gitops stacks do):

```go
prov, _ := aws.NewProvider(ctx, "audit-account", &aws.ProviderArgs{Region: pulumi.String("eu-west-1")})
a, err := auditpulumi.New(ctx, "audit", args, pulumi.Provider(prov)) // or pulumi.Providers(prov)
```

Every resource the library creates is a child of the component and takes the
provider the same way. A caller that would rather not make the call, or has no
way to, sets `Args.AccountID` and no invoke is made. No invoke is made either
when the notary is off, since the account is used for nothing else. The tests
check on Pulumi's mocks that the provider reaches the invoke.

## Optional parts

The archive is the one part that is always there. The ingest side and the notary
are each optional, and independent of the other:

| | `Ingest.Disabled` | `Notary.Disabled` | both |
|---|---|---|---|
| left out | queue and DLQ, DynamoDB table, writer function, role, log group and event source mapping, the writer's and the queue's alarms (4) | seal key and alias, notary function, role and log group, the schedule and the scheduler's role, the notary's alarms (3) | all of it |
| outputs that are then empty | `QueueURL`, `QueueArn`, `DlqURL`, `DlqArn`, `DedupeTableName`, `WriterFunctionArn`, `WriterRoleArn` | `SealKeyArn`, `SealKeyAlias`, `NotaryFunctionArn`, `NotaryRoleArn`, `ScheduleArn` | and `AlarmTopicArn` |
| inputs no longer required | `Writer.Package`, `Writer.PackageSHA256`, `Writer.DeploymentYAML` | `Notary.Package`, `Notary.PackageSHA256` | |

The alarm topic exists when at least one alarm does. The resource counts the
tests hold, with the component itself, for the test installation (Governance,
telemetry, alerts, observe): both parts 44, ingest only 30, notary only 29,
neither 13.

Use ingest without the notary where the seals are made elsewhere (a notary on
Talos signing with OpenBao transit), and neither where both are deferred: the
archive, its key and the roles for what reads it are then all the stack holds.
A part turned off later is a plain removal: its resources are deleted by the next
`pulumi up`, except the seal key and the bucket, which are protected and make the
update stop until the protection is lifted by hand.

## Encryption

`Archive.Encryption` has three modes, and `Archive.KeyArn` refines the first:

| Mode | Bucket default encryption | Key | Role grants | `kmsKey` in the functions' configuration | `ArchiveKeyArn` |
|---|---|---|---|---|---|
| `kms` (default, what every earlier version did) | SSE-KMS, bucket keys on | the library creates `alias/<name>-archive` (rotation on, protected) | `kms:GenerateDataKey`, `kms:Decrypt` on it (the read role: `Decrypt`) | the alias | the created key |
| `kms` with `KeyArn` | SSE-KMS under `KeyArn`, bucket keys on | yours: none is created | the same grants, on `KeyArn` | `KeyArn` | `KeyArn` |
| `aws-managed` | SSE-KMS under the AWS-managed key `aws/s3`, bucket keys on | none is created | none | none: the bucket default applies | empty |
| `s3` | SSE-S3 (`AES256`) | none | none | none | empty |

`KeyArn` is refused with `aws-managed` and `s3`, and must be a key ARN
(`arn:<partition>:kms:<region>:<account>:key/<id>`), not an alias ARN, which IAM
cannot grant on. The seal key is a different key and never changes: the notary
still signs with it. Objects already written keep the encryption they were
written with.

**A key you bring.** The roles are granted the key through their IAM policies,
so the key's own policy must let IAM grant access: the default key policy's
`arn:aws:iam::<account>:root` statement does that, and a policy without it makes
every put and get fail with `AccessDenied` however the roles are written. The
library neither edits nor protects a key it did not create; its rotation, its
deletion window and its policy stay yours. For a key in another account, the
key policy there must also name the roles.

**The AWS-managed key.** No grant on a key is needed or made. S3 uses `aws/s3`
on behalf of the caller and decrypts for any principal in the account that holds
`s3:GetObject` on the object, so the bucket's IAM and bucket policy are the only
access control over plaintext; the key policy cannot be changed and cannot add a
second control. Its use is logged in CloudTrail under the account, not under a
key of its own.

**What SSE-S3 gives up** is the key policy and the CloudTrail record of every
use that either KMS mode has.

**ISO 27001 (A.8.24, use of cryptography).** The control asks for a documented
policy on cryptography and key management, not for a customer-managed key.
AWS-managed keys are acceptable when the policy says so and records who rotates
(AWS, yearly), who can use the key and how its use is evidenced. Choose a
customer key (`kms`, with or without `KeyArn`) when the ISMS policy or a
customer contract requires control of the key: its policy, rotation, a
separate-duties split between key administrators and users, or the ability to
disable it. Use `KeyArn` when the organisation already manages keys centrally.


## Inputs

Required inputs are marked. Anything not listed has the default stated.

<!-- generated: aws-library-inputs -->
| input | default | meaning |
|---|---|---|
| `Tags` | none | on every resource that takes tags |
| `AccountID` | looked up | the account; empty looks it up through the component's provider, see [the AWS provider](#the-aws-provider) |
| `RolePath` | `/audit/` | the IAM path of every role the library creates |
| `LogRetentionDays` | 30 | each function's log group |
| `Archive.BucketName` | **required** | the bucket; it is in the functions' configuration, so it has to be known before anything is created |
| `Archive.ObjectLockMode` | **required** | `NONE`, `GOVERNANCE` or `COMPLIANCE`; there is no default, so every caller chooses. See [the lock modes](../explanation/aws-lambda.md#the-lock-modes) |
| `Archive.AcknowledgeCompliance` | false | the deliberate step before `COMPLIANCE`; without it the library builds nothing |
| `Archive.DefaultRetentionDays` | **required** (> 0) with `GOVERNANCE` and `COMPLIANCE` | the bucket's default retention, a floor: the writer sets each object's own. 0 is refused with a lock (a lock with no default rule is a trap), and any value with `NONE` |
| `Archive.Encryption` | `kms` | `kms` (SSE-KMS under an archive key), `aws-managed` (SSE-KMS under `aws/s3`) or `s3` (SSE-S3); the last two create no key and grant no `kms` on one; see [encryption](#encryption) |
| `Archive.KeyArn` | empty | an existing KMS key ARN for `Encryption: kms`: no key is created and the roles are granted it; refused with the other modes |
| `Archive.Profiles` | **required** | one lifecycle rule per `records/<profile>/` prefix |
| `Archive.GlacierIRDays`, `.DeepArchiveDays` | 30, 365 | [0023](../decisions/0023-archive-retention-and-lifecycle.md) |
| `Region` | looked up | the region, for the ARN of the SSM parameters; looked up like `AccountID`, and only when there are secrets to grant |
| `Writer.Secrets.Root`, `.KeyArn` | `/audit/<name>/private/config`, none | where the writer reads the secrets `Writer.Keys` names, and the customer-managed key they are encrypted with; see [secrets](../how-to/aws-store-secrets-in-ssm.md). Unset and `Writer.Keys` naming no secret: no SSM access at all |
| `Ingest.Disabled` | false | leaves out the queue, the table, the writer and their alarms; see [optional parts](#optional-parts) |
| `Ingest.Senders` | **required** unless `Ingest.AnySenderInAccount` | principals (role or user ARNs) allowed to send to the queue. The queue policy allows them `sqs:SendMessage` and denies every other principal: the queue carries no verified caller identity, so this list is the writer's authenticity on this path ([authn](../explanation/authn-authz.md#on-the-sqs-path)) |
| `Ingest.Redrivers` | none | role or user ARNs (an operator's break-glass role) allowed to send to the queue so that a DLQ redrive (`StartMessageMoveTask`, which sends as the caller) works. Allowed and excepted from the deny, and not a sender the trail relies on; same forms as `Senders`. See [redrive the ingest DLQ](../how-to/redrive-the-ingest-dlq.md) |
| `Ingest.AnySenderInAccount` | false | the acknowledged alternative, for a trial: no sender statement and no deny, so any principal of the account with `sqs:SendMessage` in its identity policy may send. Refused with `Senders` |
| `Ingest.MaxReceiveCount` | 5 | deliveries before a message moves to the DLQ |
| `Ingest.RetentionDays` | 14 | the queue's retention; 14 is SQS's limit and the deduplication window's floor |
| `Writer.Package` | **required** unless `Ingest.Disabled` | the release's `audit-writer-lambda_<version>_linux_arm64.zip`, a path or an https URL; the function's code as released |
| `Writer.PackageSHA256` | **required** with the package | that zip's SHA-256 in hex, from the release's `checksums.txt` |
| `Writer.DeploymentYAML` | **required** unless `Ingest.Disabled` | the profile configuration, the document the chart renders |
| `Writer.Catalogues` | none | application catalogues by file name (`catalogue.yaml`, `catalogue-<name>.yaml`) and content; see [the application's catalogue](../how-to/change-what-a-source-records.md) |
| `Writer.CataloguePaths` | none | the same, read from files on disk under their base names; merged with `Catalogues` |
| `Writer.CatalogueSchemas` | none | the data schemas each catalogue references, by the catalogue's file name, then `<name>.json` and content; a referenced schema missing or a schema unreferenced is refused |
| `Writer.CatalogueDirs` | none | directories each holding one catalogue document and its `.json` schemas; merged into `Catalogues` and `CatalogueSchemas` |
| `Writer.Keys`, `.ForgetIdentities` | none | the `keys` block and `forgetIdentities` of the file |
| `Writer.DedupeWindow` | the profiles' widest | a Go duration |
| `Writer.MemoryMB`, `.TimeoutSeconds` | 512, 120 | |
| `Writer.BatchSize` | 10 | 1 to 10, the sink's limit |
| `Writer.MaxBatchingWindowSeconds` | 5 | how long the mapping gathers a batch: fewer, larger objects for a few seconds of latency |
| `Writer.MaxConcurrency` | 10 | the mapping's concurrency cap, 2 or more |
| `Notary.Disabled` | false | leaves out the seal key, the notary, its schedule and alarms |
| `Notary.Package` | **required** unless `Notary.Disabled` | the release's `audit-notary-lambda_<version>_linux_arm64.zip` |
| `Notary.PackageSHA256` | **required** with the package | its SHA-256 in hex |
| `Guards.AllowVersionSkew`, `.SkipCatalogueCheck` | false | acknowledge a binary of another release than the library, and skip the comparison of catalogues with the archive; see [guards](../how-to/aws-ship-a-release.md) |
| `Notary.Schedule` | `cron(15 * * * ? *)` | EventBridge Scheduler, UTC |
| `Notary.Profiles`, `.Settle` | every profile, `10m` | as `audit-notary` |
| `Notary.MemoryMB`, `.TimeoutSeconds` | 256, 900 | |
| `Telemetry` | nil | nil gives the functions no extension, no `OTEL_*` and no `sts:GetWebIdentityToken` |
| `Telemetry.ExtensionLayerArn` | **required** with `Telemetry` | the OTLP extension, published as a layer in the account and region (by convention `audit-otlp`) |
| `Telemetry.OmitLegacyEnv` | false | leave out the deprecated `ACCESS_ROSTER_*` names of the extension's settings, which are set beside the `AUDIT_OTLP_*` ones for one minor |
| `Telemetry.IssuerURL`, `.OTLPEndpoint` | **required** with `Telemetry` | the issuer's base URL, and the OTLP/HTTP base URL (https) |
| `Telemetry.STSAudience`, `.OTLPAudience` | `otlp` | the audience asked of STS, which the roles' policies pin, and the exchange's audience |
| `Telemetry.ExtraEnv` | none | other `OTEL_*` variables |
| `Alerts.EndpointURL` | none | the HTTPS endpoint of alert-ingress; none creates the topic and the alarms and no subscription |
| `Alerts.OldestMessageAgeSeconds` | 900 | |
| `Alerts.NotarySilenceHours` | 3 | |
| `Observe.TrustedPrincipalArn` | one of this, `IRSA` and `PodIdentity` is **required** with `Observe` | the principal that may assume the read role |
| `Observe.ExternalID` | none | required of the assuming principal when set; does not apply to IRSA |
| `Observe.IRSA` | none | a ServiceAccount that may assume the read role by web identity, see [IRSA](../how-to/aws-run-readers-in-kubernetes.md) |
| `Observe.PodIdentity` | none | `ClusterName`, `ClusterArn`, `Namespace`, `ServiceAccount` (all **required**), `Region`, `PermissionsBoundaryArn`: `Namespace` and `ServiceAccount` are Kubernetes names (DNS-1123, at most 63 and 253 characters). The role also trusts EKS Pod Identity (`pods.eks.amazonaws.com`, pinned to the cluster, its account and the ServiceAccount) and the library creates the `aws.eks.PodIdentityAssociation`. Refused together with `IRSA`; may be given with `TrustedPrincipalArn` |
| `Query` | nil | `PodIdentity` (the same block, **required**) and `RecordReads` (false): a role for audit-query on EKS (its ServiceAccount must differ from `Observe.PodIdentity`'s), `<name>-query`, with the observe reader's read rights, plus `sqs:SendMessage` on the ingest queue with `RecordReads` (refused with `Ingest.Disabled`) |
| `ArchiveWriter` | nil | `IRSA` (the same block) and `Prefixes` (default `seals/`, `keys/`): a write role for a workload outside AWS |
<!-- /generated -->

## Outputs

<!-- generated: aws-library-outputs -->
| output | what |
|---|---|
| `BucketName`, `BucketArn` | the archive |
| `ArchiveKeyArn` | the symmetric key objects are encrypted with (rotation on); the given `Archive.KeyArn` if set; empty with `Encryption: s3` or `aws-managed` |
| `SealKeyArn`, `SealKeyAlias` | the `ECC_NIST_P384` `SIGN_VERIFY` key and its alias, `alias/<name>-seal`. `audit key public` reads its public half for `keys/roots.jwks` and the verifier's pin |
| `QueueURL`, `QueueArn` | the ingest queue a receiver or an application sends to (`forward.sqs.queueUrl`), and what the chart's `sink.sqs` of the query service and the jobs names when the writer runs here ([observe and query in Kubernetes](../how-to/aws-run-readers-in-kubernetes.md)): `QueueURL` is the `queueUrl`, `QueueArn` the resource of `sqs:SendMessage` |
| `DlqURL`, `DlqArn` | the dead-letter queue |
| `DedupeTableName` | the DynamoDB table |
| `WriterFunctionArn`, `NotaryFunctionArn` | the functions |
| `WriterRoleArn`, `NotaryRoleArn`, `ObserveReaderRoleArn` | the roles, see below; `ObserveReaderRoleArn` is empty without `Observe`; the outputs of a part that is turned off are empty too |
| `ArchiveWriterRoleArn` | the IRSA write role, empty without `ArchiveWriter` |
| `QueryRoleArn` | the Pod Identity role of audit-query, empty without `Query` |
| `SecretsRoot` | the SSM parameter path the writer reads secrets from, empty when its configuration names none |
| `AlarmTopicArn` | the SNS topic every alarm publishes to |
| `ScheduleArn` | the notary's schedule |
<!-- /generated -->

## What it creates

| resource | notes |
|---|---|
| S3 bucket | versioning enabled in every mode, protected from a stack destroy in every mode, the bucket's own `objectLockEnabled` never set (it forces replacement), and Object Lock as a separate configuration resource that exists unless the mode is `NONE`; SSE-KMS under the archive key with bucket keys (the AWS-managed key with `aws-managed`, SSE-S3 with `s3`), all four public-access blocks, bucket-owner-enforced ownership, a policy that denies plain HTTP, a lifecycle rule per profile prefix and one that aborts incomplete multipart uploads after 7 days. `ForceDestroy` is never set |
| KMS archive key | symmetric, rotation on, protected, alias `alias/<name>-archive`; only with `Encryption: kms` and no `KeyArn` |
| KMS seal key | `ECC_NIST_P384`, `SIGN_VERIFY`, protected, alias `alias/<name>-seal`, and a key policy of its own (below) |
| SQS ingest queue and DLQ | SSE-SQS, visibility timeout six times the writer's timeout, a redrive policy to the DLQ and a redrive-allow policy on the DLQ, a queue policy that denies plain HTTP and allows the named senders |
| DynamoDB table `<name>-dedupe` | on-demand, hash key `pk` (string), TTL on `expires_at` |
| Lambda `<name>-writer`, `<name>-notary` | `provided.al2023`, `arm64`, no VPC, a log group each, the extension layer when there is one |
| event source mapping | queue to writer, `ReportBatchItemFailures`, scaling capped by `Writer.MaxConcurrency` |
| EventBridge Scheduler `<name>-notary` | the schedule, a role of its own that may invoke only the notary, no retries, and no asynchronous retries on the function |
| IAM roles | below |
| CloudWatch alarms, SNS topic `<name>-alarms` | [below](#alarms) |

The library supports the `aws` partition and one region per stack.

## IAM roles

**One role per function, under the path `/audit/`**, named `<name>-<part>`. The
name is in the role because an IAM role name is unique across the account whatever
its path, and one account may hold several installations. For the default
installation, `audit`, the exact ARNs, which an estate's gitops grants and the OTLP
issuer's group matcher name, are

| role | ARN | runs as |
|---|---|---|
| writer | `arn:aws:iam::<account>:role/audit/audit-writer` | the writer function; the identity the OTLP door sees |
| notary | `arn:aws:iam::<account>:role/audit/audit-notary` | the notary function; the identity the OTLP door sees |
| observe reader | `arn:aws:iam::<account>:role/audit/audit-observe-reader` | assumed by observe in another account or by a Kubernetes ServiceAccount, by IRSA or Pod Identity (only with `Observe`) |
| query | `arn:aws:iam::<account>:role/audit/audit-query` | audit-query on EKS, by Pod Identity (only with `Query`) |
| archive writer | `arn:aws:iam::<account>:role/audit/audit-archive-writer` | a Kubernetes ServiceAccount, by IRSA (only with `ArchiveWriter`) |
| scheduler | `arn:aws:iam::<account>:role/audit/audit-scheduler` | EventBridge Scheduler, to invoke the notary and nothing else |

The writer, notary and scheduler roles exist only with their part; the KMS
row is empty with `Encryption: s3` or `aws-managed`, and the IRSA write role is described under
[Kubernetes workloads](../how-to/aws-run-readers-in-kubernetes.md).

The kernel OTLP door's provisional single role, `role/audit/audit`, is not used:
the writer and the notary must not share a role, because whoever can write the
archive and can also sign for it can choose what to sign
([0019](../decisions/0019-seals.md)).

What each role may do, and nothing more:

| | writer | notary | observe reader |
|---|---|---|---|
| S3 put | `PutObject`, `PutObjectRetention`, `PutObjectLegalHold` on `records/`, `catalogue/`, `schema/`, `identity/`, `dlq/` | `PutObject`, `PutObjectRetention` on `seals/`, `keys/` | none |
| S3 read | `GetObject` on the same and `holds/`, `ListBucket` | `GetObject` on `records/`, `seals/`, `keys/`, `ListBucket` | `GetObject` on `records/`, `catalogue/`, `schema/`, `seals/`, `keys/`; `ListBucket` under those prefixes |
| KMS | `GenerateDataKey`, `Decrypt` on the archive key | the same, and `Sign`, `GetPublicKey`, `DescribeKey` on the **seal key** | `Decrypt` on the archive key |
| DynamoDB | `GetItem`, `BatchGetItem`, `PutItem` on the dedupe table | none | none |
| SQS | `ReceiveMessage`, `DeleteMessage`, `GetQueueAttributes`, `ChangeMessageVisibility` on the ingest queue | none | none |
| logs | its own log group | its own log group | none |
| STS | `GetWebIdentityToken` with `Telemetry` | the same | none |

`PutObjectRetention` and `PutObjectLegalHold` are listed beside `PutObject`
because S3 refuses a put that carries an Object Lock header unless the caller also
holds the matching permission. With `ObjectLockMode: NONE` the functions send no
lock header, so both grants are left out. No role has a delete: nothing in the archive is
deleted by anything in this stack.

**The seal key has a policy of its own.** The default key policy hands a key to
IAM, so any principal in the account with a `kms:Sign` allow could sign seals.
This one does not: the account's root administers the key (create, describe,
enable, put policy, schedule deletion and so on) and **cannot use it**, and only
the notary's role may `Sign`, `GetPublicKey` and `DescribeKey`.

**The web identity statement** is, for both functions, with the audience the
`Telemetry` input names (default `otlp`):

```json
{
  "Effect": "Allow",
  "Action": "sts:GetWebIdentityToken",
  "Resource": "*",
  "Condition": {
    "ForAllValues:StringEquals": { "sts:IdentityTokenAudience": ["otlp"] },
    "StringEquals": { "sts:SigningAlgorithm": "ES384" },
    "NumericLessThanEquals": { "sts:DurationSeconds": "300" }
  }
}
```

`sts:IdentityTokenAudience` is a multi-valued key, so it needs
`ForAllValues:StringEquals`: a plain `StringEquals` is an implicit deny when the
request carries the audience as a list. `ForAllValues` also passes on an empty
set, which is safe here only because `Audience` is a required parameter of
`GetWebIdentityToken`. The algorithm and the lifetime are what the extension asks
for.

## Alarms

With `Ingest.Disabled` the writer's and the queue's alarms are not created, and with
`Notary.Disabled` the notary's three are not; with neither there is no topic.

CloudWatch alarms publish, on ALARM and on OK, to the SNS topic `<name>-alarms`,
which is subscribed to alert-ingress over HTTPS. This is the D13 set:

| alarm | metric | fires when | why |
|---|---|---|---|
| `<name>-writer-throttles` | `AWS/Lambda` `Throttles`, writer | any, in 5 minutes | the writer is not keeping up, or the account's concurrency is spent |
| `<name>-notary-throttles` | `AWS/Lambda` `Throttles`, notary | any, in 5 minutes | |
| `<name>-ingest-dlq-not-empty` | `AWS/SQS` `ApproximateNumberOfMessagesVisible`, DLQ | above 0 | a record was delivered `MaxReceiveCount` times and is not in the archive |
| `<name>-ingest-oldest-message-age` | `AWS/SQS` `ApproximateAgeOfOldestMessage`, ingest | above `Alerts.OldestMessageAgeSeconds` (900) | the writer is behind or not running |
| `<name>-writer-errors` | `AWS/Lambda` `Errors`, writer | any, in 5 minutes | an invocation failed |
| `<name>-writer-unknown-catalogue` | `Audit/<name>` `UnknownCatalogueVersion` (a metric filter on the writer's log group, matching the field `event=unknown_catalogue`) | any, in 5 minutes | a record named a catalogue version the writer does not have and was dead-lettered in the archive and acknowledged, which neither queue's alarm sees: the writer and its emitters are out of step. The log line names the `source` and `catalogue_version`; the same count is `audit_writer_catalogue_unknown_total` by `source` and `catalogue_version` over OTLP |
| `<name>-notary-errors` | `AWS/Lambda` `Errors`, notary | any, in an hour | a tenant could not be sealed, or the signer failed |
| `<name>-notary-silent` | `AWS/Lambda` `Invocations`, notary | below 1 in each of the last `Alerts.NotarySilenceHours` (3) hours | the schedule or the function is gone, and the chain of seals is growing a gap |

"The function going silent" is the notary's, as an alarm on the platform's own
`Invocations` metric: Lambda publishes no datapoint for an hour with no
invocations, so missing data is treated as breaching, and a function that has
stopped is the one case an alarm that depends on the function's own telemetry
cannot see. The writer is quiet when nothing is written, so its silence is not
an alarm; the oldest-message-age alarm is what says it has stopped with work to
do.

**How alarms reach alert-ingress.** CloudWatch publishes to the topic, and the
topic delivers to `Alerts.EndpointURL` over HTTPS. The subscription is created with
`EndpointAutoConfirms` false: SNS POSTs a `SubscriptionConfirmation` to the URL and
the subscription stays pending until the endpoint follows its `SubscribeURL`, so
alert-ingress has to handle SNS's message types (`SubscriptionConfirmation`,
`Notification`, `UnsubscribeConfirmation`) and should verify the message
signature. The topic is not encrypted with a customer key, which CloudWatch could
not publish to without a key policy of its own: an alarm's body names a queue and
a function and carries no record.

## Lifecycle

One rule per `records/<profile>/` prefix: Glacier Instant Retrieval at 30 days
(still readable by observe's reindex and by `audit verify` without a restore) and
Deep Archive at one year (for what nobody expects to read before retention ends;
reading it needs a restore, and `audit verify` over such a range says so first).
`seals/`, `keys/` and `catalogue/` are small, are read often, and have no rule.
Objects below the storage class's minimum billable size stay where they are, which
is S3's default.

## Not covered

- **A deployment.** Nothing here has run in an account.
- **The `lambda` sink** (a direct invocation of the writer function) is still
  designed ([capabilities](capabilities.md)); the queue is the transport.
- **FIFO ingest.** The queue is a standard queue and deduplication is the writer's;
  a FIFO queue would also absorb a repeat inside its five-minute window, and is
  not what the library creates.
- **A signed delegation.** The notary signs with a root, as everywhere
  ([0019](../decisions/0019-seals.md)).

