# Run observe and query in Kubernetes with the writer on Lambda

## Purpose

Keep the write path on Lambda and run everything that reads (the indexer, the
query service, the migration and the jobs) in a cluster, reaching the archive
by IRSA or from another account.

## Preconditions

- The Lambda stack is deployed ([getting started on AWS Lambda](../getting-started/aws-lambda.md)),
  so that `QueueURL`, `QueueArn` and `BucketName` exist.
- A Postgres for the index ([prepare the database](prepare-the-database.md)).
- A cluster with a workload identity for AWS: EKS Pod Identity or IRSA. A
  cluster that is not EKS needs an IAM OIDC provider of its own.

## Before you start

- **Every sender must be in `Ingest.Senders`** by role or user ARN, in this
  account or another. The queue policy allows those and denies
  `sqs:SendMessage` to every other principal. A sender's own identity policy
  must still allow `sqs:SendMessage` on `QueueArn`.
- **`Senders` takes the ARN of an IAM role or user**, not an assumed-role
  session ARN, a bare account id or a wildcard; the library refuses those at
  preview.
- **Each component has its own ServiceAccount, so each role holds only its own
  rights.** The chart refuses an enabled component with
  `serviceAccount.create: false` and no `name`, which would run as the writer's
  ServiceAccount that is not rendered.
- **Both IRSA conditions matter.** Without the `sub` pin any ServiceAccount of
  the cluster could assume the role; without the `aud` pin a token minted for
  another audience would be accepted.
- The chart refuses, with a message that says why: a sink whose URL is the
  release's own front door; a query service with no sink; `mode: stream`;
  `workloadIdentity.issuers`, `keysVolume` and `extensions.billing`, which
  belong to the writer.

## Steps

### 1. Let the cluster's ServiceAccounts assume roles

A cluster that is not EKS (Talos) can have an IAM OIDC provider of its own, and a
ServiceAccount's projected token can then assume a role by web identity. Two roles
accept this, each for **one** ServiceAccount:

```go
Observe: &auditpulumi.ObserveArgs{
	IRSA: &auditpulumi.IRSAArgs{
		OIDCProviderArn: oidcProviderArn,        // arn:aws:iam::<account>:oidc-provider/k8s.example.test
		IssuerHost:      "k8s.example.test",    // the provider's URL, no scheme
		Namespace:       "audit",
		ServiceAccount:  "audit-observe",
		// Audience defaults to "sts.amazonaws.com"
	},
},
```

The trust policy is

```json
{
  "Effect": "Allow",
  "Principal": { "Federated": "<OIDCProviderArn>" },
  "Action": "sts:AssumeRoleWithWebIdentity",
  "Condition": { "StringEquals": {
    "<IssuerHost>:aud": "sts.amazonaws.com",
    "<IssuerHost>:sub": "system:serviceaccount:<Namespace>:<ServiceAccount>"
  } }
}
```

Both conditions matter: without the `sub` pin any ServiceAccount of the cluster
could assume the role, and without the `aud` pin a token minted for another
audience would be accepted. The library refuses an empty namespace or
ServiceAccount, and a name with a wildcard in it. The ServiceAccount's token must
be projected with the audience, and the workload set `AWS_ROLE_ARN` and
`AWS_WEB_IDENTITY_TOKEN_FILE` (the AWS SDK's own web identity provider).

- **`Observe.IRSA`** makes `<name>-observe-reader` trust the ServiceAccount. It is an
  alternative to `Observe.TrustedPrincipalArn`, or in addition to it (the role
  then has both statements). The role is read only: `GetObject` on `records/`,
  `catalogue/`, `schema/`, `seals/` and `keys/`, `ListBucket` under those prefixes,
  and `kms:Decrypt` on the archive key when there is one.
- **`ArchiveWriter`** creates `<name>-archive-writer` for a workload outside AWS
  that writes part of the archive itself: hive runs the digest on Talos, and it
  writes `seals/` and `keys/`. `ArchiveWriter.IRSA` is the same block;
  `ArchiveWriter.Prefixes` are what it may put under (any of `records/`,
  `catalogue/`, `schema/`, `identity/`, `dlq/`, `seals/`, `keys/`; default `seals/` and
  `keys/`). Its rights are `PutObject` on those prefixes (and `PutObjectRetention`
  unless the mode is `NONE`), `GetObject` on `records/` and those prefixes,
  `ListBucket`, and the archive key's `GenerateDataKey` and `Decrypt` when there is
  one. It has no delete, no legal hold, no seal key, no queue and no table.

Output: `ArchiveWriterRoleArn`, empty without `ArchiveWriter`.

### 2. Point the chart's components at the queue

An installation can keep the write path here and everything that reads in a
cluster: the chart with `writer.enabled: false` (`charts/audit/examples/external-writer.yaml`,
golden `example-external-writer`). It renders the indexer, the query service,
the `migrate` hook (which never depended on the writer: the `writer` role in
`migrate.config` is optional, and the Lambda has no database) and the jobs. It
renders **no writer, no receiver, no stream consumers and no writer Service**,
and `mode` stays `direct`. `keysVolume`, `workloadIdentity` and the writer's
ServiceAccount do not apply.

Observe and query still need Postgres. What in the release records sends to the
Lambda's ingest queue instead of an in-cluster front door:

| component | what it records | where |
|---|---|---|
| query | every read of the trail (who looked) | `query.config.sink` |
| notary | `audit.seal.written` | `jobs.notary.config.sink` |
| verify | `audit.seal.verified`, `audit.seal.failed` | `jobs.verify.config.sink` |
| clock-sync | the clock's offset | `jobs.clockSync.config.sink` |

Each takes `sink.sqs` (`queueUrl`, `region`; `fifo` is derived from the URL).
The acknowledgement is `queued`, so `require: queued` is what each can ask for
and `sink.expect` may be left out. The queue URL is the `QueueURL` output of this
library. The chart refuses, with a message that says why: a sink whose URL is the
release's own front door; a query service with no sink; `mode: stream`;
`workloadIdentity.issuers`, `keysVolume` and `extensions.billing`, which belong
to the writer; and an enabled component with `serviceAccount.create: false` and
no `name`, which would run as the writer's ServiceAccount that is not rendered.
An external front door's URL is still accepted as a `sink.url`.

```yaml
writer:
  enabled: false
query:
  enabled: true
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: <role with the IAM below>
  config:
    require: queued
    sink:
      sqs:
        queueUrl: <QueueURL>
        region: eu-west-1
jobs:
  notary:
    enabled: true
    serviceAccount:
      annotations:
        eks.amazonaws.com/role-arn: <the notary's role>
    config:
      require: queued
      sink:
        sqs: {queueUrl: <QueueURL>, region: eu-west-1}
      signer:
        transit: {key: audit-seal, openbao: {address: ..., login: {mount: ..., role: audit-notary, jwtFile: /var/run/openbao/token}}}
```

**IAM, per ServiceAccount** (EKS Pod Identity is an association made outside the
chart; IRSA is the annotation above; each component has its own account, so each
role holds only its own rights):

| component | rights |
|---|---|
| query | `sqs:SendMessage` on the ingest queue (`QueueArn`); `s3:GetObject` and `s3:ListBucket` on the archive prefix and `kms:Decrypt` on the archive key; put on its exports bucket |
| notary | `sqs:SendMessage` on the ingest queue; read the archive, `PutObject` (and retention, with a lock) on `seals/` and `keys/` (the `ArchiveWriter` role); the seal key is OpenBao Transit through its own JWT role, or KMS `Sign` when not |
| verify, clock-sync | `sqs:SendMessage` on the ingest queue when they have a `sink`; verify also reads the archive |
| observe | read the archive; no queue |

Only `sqs:SendMessage` is needed on the queue: a sender does not receive or
delete. The queue is `QueueArn` in the library's outputs. Every sender, in this
account or another, must be named in `Ingest.Senders`: the queue policy allows
those and denies `sqs:SendMessage` to every other principal, and denies everything
that is not TLS. A sender's identity policy still has to allow `sqs:SendMessage`
on `QueueArn`; the policy names who may, not who does.

### 3. Or read from another AWS account

With `Observe`, the library creates `<name>-observe-reader` in the archive's
account. It trusts `Observe.TrustedPrincipalArn` (in the kernel's account, the role
`audit-observe` runs as; add `ExternalID` to require `sts:ExternalId`) and may
list and get on `records/`, `catalogue/`, `schema/`, `seals/` and `keys/`, and decrypt under
the archive key (when there is one), and nothing else. A cluster outside AWS reaches it
by IRSA (step 1) instead, or as well. Observe assumes it and follows the bucket by
cursor ([0020](../decisions/0020-observe-follows-the-bucket.md)); everything
downstream of that, including the index, is in the other account. The principal's
own side needs `sts:AssumeRole` on the role's ARN.

## Afterwards

- Check `audit conformance` against the query service and `audit verify` against
  the archive ([verify the trail](verify-the-trail.md)).
- Add each new sender to `Ingest.Senders` in the same change that gives it the
  queue ARN; there is no other way for it to send.
