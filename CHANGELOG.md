# Changelog

All notable changes to this project are documented here, one `## vX.Y.Z`
heading per released tag, newest first. A section describes the state of the
repository at that version, not the history of edits that got there.

## Unreleased

Configuration is version 2: the group is `audit.truvity.github.io`, a secret is named by `...Secret` and found through one declared source, and on AWS Lambda secrets come from SSM and never from the function's environment. Version 1 is read for one minor.

Migration steps: [upgrade to v0.13](docs/how-to/upgrade/v0.13.md).

- **Breaking: `apiVersion` is `audit.truvity.github.io/<kind>/v2`.** The group is `<product>.truvity.github.io` and the version is 2, for every configuration file (`audit-writer`, `audit-query`, `audit-observe`, `audit-notary`, `audit-writer-lambda`, `audit-verify`, `audit-purge`, `audit-clock-sync`, `audit-migrate`) and for the documents they name (`audit-deployment`, `audit-grants`, `audit-workloads`). The binaries read version N and N-1, as ADR 0025 said: **version 1** (`truvity.github.io/<kind>/v1`, or no `apiVersion`, which means the same) is validated against its frozen schema (`schemas/config/v1/`), converted, validated against version 2's and read, and logs a deprecation warning. It is read for **one minor** and then removed. The schemas' `$id`s move to `https://truvity.github.io/audit/schemas/v2/config/<kind>.schema.json`; version 1's stay at `.../schemas/v1/config/`, and the Pages site serves both. A file with another group, version or kind is refused by name. `preset.ParseDeployment` accepts the new `audit-deployment` group and the old one with a warning.
- **Breaking (in version 2 files): secrets are named by `...Secret` and found through `secrets`.** `database.passwordEnv`, `bucket.credentialsEnv` and `openbao.tokenEnv` become `passwordSecret`, `credentialsSecret` and `tokenSecret`, each holding the **name** of a secret, and the file gets one block, `secrets: {source: env|file|ssm, root: ...}` (default `env`), that says how a name is found: an environment variable; a file under an absolute `root` (a mounted Secret, one trailing newline dropped); or a SecureString `<root>/<name>` in SSM Parameter Store, read decrypted with the process's own identity. A name for `file` and `ssm` is relative and cannot leave the root. An error names the field, the source and the root, never the name or a value. **The `...Env` fields are deprecated and exist in version 1 only**, where the loader reads them as the `...Secret` field of the same name with `secrets: {source: env}`: a version-1 file keeps working as it was. `config.Secret` is gone; `Postgres.PoolConfig` and `cli.Open{Archive,Exports,Keys,Signer}From` take the file's `*config.Secrets`.
- **Pulumi library: no secret in a function's environment, and SSM for the ones the configuration names.** `Writer.Keys` may name a secret (`tokenSecret`); the library then renders `secrets: {source: ssm, root}` into the writer's `audit.yaml` and grants the writer's role `ssm:GetParameter` and `ssm:GetParameters` on `arn:aws:ssm:<region>:<account>:parameter<root>/*` and nothing else of SSM, plus, with `Writer.Secrets.KeyArn`, `kms:Decrypt` on that key through SSM only and for parameters under the root only. New `Writer.Secrets` (`Root`, default `/audit/<name>/private/config`, a path and never a pattern; `KeyArn`), `Args.Region` (looked up like `AccountID`, only when there is something to grant) and the output `SecretsRoot`. **A `...Env` key in `Writer.Keys` is now refused** (a function's environment is not a place for a secret), where it was accepted as a reference; the rendered files carry `apiVersion` version 2. A configuration that names no secret gets no SSM access. The function's environment is, and a test holds it to being, `AUDIT_CONFIG`, `AUDIT_CONFIG_LAYER` and the telemetry's own variables. The library creates no parameter: it is not given the values.
- **Breaking: `secrets.source: env` is refused on AWS Lambda, at runtime.** A process that finds `AWS_LAMBDA_FUNCTION_NAME` set will not read a secret from the environment, whatever its file says, and the error names the field. A version-1 `...Env` that the loader converts to `source: env` therefore **fails on Lambda too**: that is intended (D4), so a Lambda that read a secret from a variable must move to `source: ssm` before it can run on this release. Every conversion of a version-1 `...Env` field logs a deprecation warning naming the field. `Telemetry.ExtraEnv` refuses `OTEL_*HEADERS*` and any key naming a token, secret, password or credential. `secrets.root` has no empty, `.` or `..` segment (file and ssm), and `Writer.Secrets.Root` must be under `/audit/` (at least two segments, the first `audit`), so the SSM grant cannot reach another tree. A failed SSM client is retried with a backoff (1s up to 30s) and not remembered for the life of the process.
- **Chart: `secretFiles`.** A component's `secretFiles` (`{name, secretName, key}`) projects each Secret key as `/etc/audit/secrets/<name>`, for a version-2 config with `secrets: {source: file, root: /etc/audit/secrets}`, which the chart checks and refuses to render without. `secretEnv` is **deprecated** (version-1 configs). The values schema accepts a config in version 2 when it says so and in version 1 otherwise, so existing values render as before. The examples are in version 2, and their goldens changed.

- **Catalogue file names: any `*.yaml` document is a catalogue.** `Writer.CatalogueDirs` accepts a directory whose one `.yaml` is called anything, and `Writer.CataloguePaths` a file called anything; the library ships it as `catalogue-<name>.yaml`, which is what the writer finds, so an application no longer renames its catalogue (the step nexus had). The old rule still decides when it applies: a directory with a `catalogue.yaml` or `catalogue-<name>.yaml` has that one as its catalogue and its other `.yaml` files are not read; two candidates and none named so is refused.
- **An unknown catalogue version is signalled.** A record naming a catalogue version the writer does not have is dead-lettered and acknowledged, so neither queue's alarm saw it. The writer (Lambda and server alike) now logs `event=unknown_catalogue` with the `source` and `catalogue_version`, and counts `audit_writer_catalogue_unknown_total` by both (bounded at 20 distinct pairs, then `other`). The Pulumi library adds a metric filter on the writer's log group (`Audit/<name>` `UnknownCatalogueVersion`) and the alarm **`<name>-writer-unknown-catalogue`**, so `AlarmNames` has an eighth entry and a stack gains a log metric filter and an alarm.
- **`Ingest.Redrivers`, and what a sender is.** A DLQ redrive (`StartMessageMoveTask`) sends to the ingest queue as the caller, which the deny of every non-sender refused. `Ingest.Redrivers` (an operator's break-glass role) is added to the allow and to the deny's exceptions; see the [runbook](docs/how-to/redrive-the-ingest-dlq.md). `Senders` and `Redrivers` are checked on their resolved values and refused unless the ARN of an IAM role or user: no assumed-role session ARN, no `sts` ARN, no bare account id, no wildcard.
- **The unknown-catalogue filter matches a field, not a phrase.** The log line carries `event=unknown_catalogue`, and everything a record says that reaches a writer log line (`source`, `catalogue_version`, `id`, `action`, the dead-letter reason) has `=` replaced by `:` and is bounded, so an emitter's string cannot raise the alarm or forge a line.
- **A stray `.yaml` is not taken for a catalogue.** A document taken as the catalogue only because it is the one `.yaml` of a `CatalogueDirs` directory, or a `CataloguePaths` file not named `catalogue*.yaml`, must have a source, a version and actions, or the preview fails naming the file.
- **`/readyz`, distinct from `/healthz`.** `audit-writer`, `audit-query` and `audit-observe` serve `/readyz`: ready when the database answers and, for the writer's registry and observe's archive, the catalogues can be read (and a stream or queue consumer is running); a 503 names the failed check and never its error. `/healthz` is unchanged. **The chart's readiness probes read `/readyz`** (with `timeoutSeconds: 3`) and the liveness probes stay on `/healthz`; every golden changes by those two lines.
- **Rename leftovers.** The Pulumi library sets the OTLP extension's settings as `AUDIT_OTLP_ISSUER`, `AUDIT_OTLP_STS_AUDIENCE`, `AUDIT_OTLP_ENDPOINT` and `AUDIT_OTLP_AUDIENCE`, and, **deprecated for one minor**, the same four under the old `ACCESS_ROSTER_*` names, which the extension reads until a build of it reads the new ones; `Telemetry.OmitLegacyEnv` drops them. The extension layer is named `audit-otlp` in the docs and tests (the library takes its ARN, as before). The Justfile's `sdk-closure` forbidden list says why `access-roster` and `sluis` are both on it.
- **Breaking: the root module no longer requires `github.com/truvity/sluis`.** The group grammar `authn/roster.go` took from `policy.SplitGroup` and `policy.ScopeAll` is `authn.SplitGroup` and `authn.ScopeAll`, with a test that holds it to the table of cases sluis holds its own reader to, so that sluis can import this module's SQS sink without a cycle.
- **Breaking: `Ingest.Senders` is required** when the ingest side is on, and the queue policy now also **denies** `sqs:SendMessage` to every principal not named (`aws:PrincipalArn`), so the senders are the whole of who may send: on the SQS path the writer's authenticity rests on them, as the queue carries no verified identity of its caller ([authn](docs/explanation/authn-authz.md#on-the-sqs-path)). `Ingest.AnySenderInAccount` is the acknowledged alternative (no sender statement, no deny) and is refused beside `Senders`. A stack that named none now fails at preview. The observer is not stamped from a message attribute: it would be the sender's own claim, and the one thing SQS vouches for (`SenderId`) cannot be mapped to a name by the library.

**Every Breaking entry above is migrated in [the v0.13 upgrade page](docs/how-to/upgrade/v0.13.md)** (version 1 files, secrets on Lambda and Kubernetes, `Senders`, probes, the renamed settings).

## v0.12.0

The Pulumi library ships a catalogue's data schemas with it in the writer's configuration layer, and refuses at preview a catalogue the writer would refuse.

- **The Pulumi library ships a catalogue's data schemas with it, and refuses a catalogue the writer would refuse.** New `Writer.CatalogueSchemas` (by the catalogue's file name, then `<name>.json` and content) and `Writer.CatalogueDirs` (directories each holding one catalogue document and the `.json` schemas it references, the layout `sdk/catalogue.LoadFS` reads and an application embeds; merged like `CataloguePaths`). The writer reads a catalogue with the `.json` files beside it and refuses to start when a referenced schema is missing, and until now the library could only ship the document: a catalogue with a `data_schema` deployed a writer that failed every start while `pulumi up` succeeded, and the ingest queue drained into the dead-letter queue (seen in production on 2026-10-04). Now every catalogue is held, before anything is created, to the writer's own check: each schema it references (`data_schema`, `attributes_schema`, `dimensions_schema`, `context_areas`, legacy ids matched as the writer matches them) is given, each schema given is referenced, each has a `$id` and no two claim one. **A deployment that passed such a catalogue without its schemas is now refused in the preview**, where it was a writer that never started. A catalogue with schemas goes into the layer as `catalogues/<file without .yaml>/` with exactly its schemas; one without stays `catalogues/<file>`, so an existing layer is unchanged. The writer binary is unchanged: it already walks `catalogues/`. See [deployment/aws.md](docs/how-to/change-what-a-source-records.md).

## v0.11.0

The configuration is a file named by `AUDIT_CONFIG` and versioned by `apiVersion`, the writer says which one it ran under, and the Pulumi library deploys the release's zip as released with the configuration as a layer.

- **`AUDIT_CONFIG` names the configuration file**, as an alternative to `--config`, in all six binaries (`audit-writer`, `audit-query`, `audit-observe`, `audit-notary`, `audit-writer-lambda`, `audit-notary-lambda`) and in the four jobs of `audit` (`verify`, `purge`, `clock-sync`, `migrate`), through one helper (`internal/config.Path`, which is `truvity/policy`'s `config.PathFrom` plus the Lambdas' defaults). The flag wins when both are given; for a job of `audit` the variable counts only on a command line with no option of its own. The two Lambda binaries no longer hard-code a path: with neither set they read `/opt/audit/audit.yaml` (where the Pulumi library's configuration layer mounts it) and fall back to the old `/var/task/audit.yaml` for one release. See [configuration](docs/reference/configuration.md#the-configuration-file).
- **`apiVersion` on every configuration file and on the documents they name**, in `truvity/policy`'s envelope (`LoadKind`): `apiVersion: truvity.github.io/<kind>/v1`, or absent, which means the same, where `<kind>` is the schema's name: `audit-writer`, `audit-query`, `audit-observe`, `audit-notary`, `audit-writer-lambda`, `audit-verify`, `audit-purge`, `audit-clock-sync`, `audit-migrate`, `audit-deployment`, `audit-grants` or `audit-workloads`. Another version, or another kind's, is refused by name. The root requires `github.com/truvity/policy` v1.44.0. The three documents, which were strict YAML decoded in code, now have authored JSON Schemas beside the others (`schemas/config/audit-deployment.schema.json`, `audit-grants.schema.json`, `audit-workloads.schema.json`), generated by `just config-schemas` and held to the generator by the same test; the code that reads each validates against its schema and then decodes strictly. A misspelt grant operation is now refused with the schema's path (`rules.0.grant.operations.0`) and the list it may be.
- **The writer's start-up record says which configuration it ran under.** `audit.writer.started` carries `data` (`writer-started.json`): `config_file`, `config_digest` (`sha256:` and the SHA-256 of the file's bytes), `deployment_digest`, `workloads_digest`, `catalogues_digest`, and on Lambda `layer` (from `AUDIT_CONFIG_LAYER`, which the deployment sets) and `function_version`. The loader reads the file before and after validating it and refuses one that changed in between. The **common catalogue is 2.1.0** (additive: the extension on that one action), and the SDK is released with it, so the root requires `github.com/truvity/audit/sdk` v0.11.0 and `go.work` carries a versioned `replace` until that tag exists. A writer on this release registers `catalogue/audit/2.1.0` beside the existing 2.0.0, which is untouched.
- **Breaking: the Pulumi library deploys the release's zip as it is, and the configuration is a layer.** `Writer.BinaryPath` and `Notary.BinaryPath` are gone. **`Writer.Package`** and **`Notary.Package`** take the release's `audit-writer-lambda_<version>_linux_arm64.zip` and `audit-notary-lambda_<version>_linux_arm64.zip` (a path or an https URL) and **`PackageSHA256`** the digest from the release's `checksums.txt`, both required: the zip is read, hashed, and refused when it is not that file, has no `bootstrap` at its root or a path outside it, is not an arm64 Linux executable, or is the other command's. The library no longer builds a package with the configuration inside; the function's code is the release's bytes (`sourceCodeHash` is what Lambda reports for them). The rendered `audit.yaml`, the profile document (`deployment.yaml`) and the catalogues are published as an immutable `aws.lambda.LayerVersion` per function (`<name>-writer-config`, `<name>-notary-config`) and mounted at `/opt/audit/`; the function's environment is `AUDIT_CONFIG=/opt/audit/audit.yaml` and `AUDIT_CONFIG_LAYER=<the layer version's ARN>`, beside the telemetry's. Old layer versions are kept (`SkipDestroy`). A function has two layers with the extension, of the five it may have. The configuration's paths are now `/opt/audit/...`; this needs binaries of this release (the previous one looks at `/var/task/audit.yaml`). The writer's start-up record carries the layer. See [deployment/aws.md](docs/explanation/aws-lambda.md#configuration-as-a-layer).
- **The Pulumi library refuses, before any resource, what would stop a function initialising.** A failing init does not fail the deploy: the event source mapping keeps invoking it and the ingest queue drains into the dead-letter queue. In the program, ahead of the function: **the binary's release must be the library's** (read from the zip's release file name, the name its digest is listed under; `Guards.AllowVersionSkew` accepts a difference that is meant, such as a build from a checkout), and **each `Writer.Catalogues` / `CataloguePaths` document is compared with the archive's own `catalogue/<source>/<version>`** (by the `sha256` metadata the writer put on it): a changed document under an unchanged version is refused with the instruction to bump `version:`. An absent object or bucket passes; anything else that stops the comparison is a refusal, and `Guards.SkipCatalogueCheck` is the way to say the deploying identity cannot read the bucket (it needs `s3:GetObject` on `catalogue/*` otherwise). The provider's not-found text (`couldn't find resource`) counts as absent; a 403 is refused with a message naming `s3:ListBucket`, which the deploying identity needs beside `s3:GetObject` (and `lambda:PublishLayerVersion`/`GetLayerVersion`). A catalogue without a `source` or a `version` is refused; so is a secret-looking value in `Writer.Keys`, since layer versions are kept. The configuration layer is last in the function's layer list. Not yet: validating the rendered files against the embedded schemas in the program, reading the binary's own version where its build info has one, and release attestation.
- This release carries the archive encryption modes (`Archive.Encryption` `aws-managed` and `Archive.KeyArn`), described in the entry below.
- **Chart: a pod restarts for every change that reaches it, and stops without losing what it holds.** The writer pods carry `checksum/catalogues` (hashing `catalogues`, which they read once at start-up; the receiver holds none and does not carry it). `checksum/deployment`, on the write path and on the query service, now hashes the **whole** rendered `deployment.yaml` (one shared template renders both the ConfigMap and the hash), where it hashed `profiles` alone and so missed `externalIdentifiersAreOpaque`: flipping it changed what the profiles keep and restarted nothing. The write-path pods get `terminationGracePeriodSeconds` (new value, default 45) longer than the writer's 30s shutdown budget, which the chart refuses to render shorter than that plus the new `preStopSleepSeconds` (default 5), a `preStop` sleep action on the front door (not the consumer, which nothing calls) so that it keeps answering while its endpoints drain. Every checksum annotation of the existing goldens changes; nothing else about a manifest does. The files `config.yaml`, `deployment.yaml`, `workloads.yaml` and `grants.yaml` stay `subPath` mounts, on purpose: the processes read them once and the checksum replaces the pod, so a directory mount that the kubelet updates under a running process would only make the file differ from what that process loaded; the catalogues were already a directory.
- The Pulumi library's `Archive.Encryption` gains **`aws-managed`**, SSE-KMS under the AWS-managed `aws/s3` key with bucket keys: no key is created, no role is granted a `kms` action (S3 decrypts on behalf of any principal in the account that may `s3:GetObject`) and the functions' configuration names no `kmsKey`. New **`Archive.KeyArn`** with `kms` uses an existing customer key instead of creating `alias/<name>-archive`: the writer, notary, observe reader and archive-writer roles are granted `kms:GenerateDataKey` / `kms:Decrypt` on that key through IAM (its key policy must allow IAM), and the functions are configured with its ARN. `KeyArn` is refused with `s3` and `aws-managed`, and must be a key ARN, not an alias. `kms` with no `KeyArn` and `s3` are unchanged. See [deployment/aws.md](docs/reference/aws-pulumi-library.md#encryption), including the ISO 27001 A.8.24 note and the migration from `kms` to `aws-managed` (S3 does not re-encrypt existing objects: copy them in place, then schedule the old key's deletion).

## v0.10.0


The chart can run observe, query and the jobs in Kubernetes while the writer runs elsewhere: writer.enabled false, and every recorder sends its own records over SQS.
- The chart can run **observe and query (and the notary) in Kubernetes while the writer runs elsewhere**, such as the writer Lambda behind SQS. `writer.enabled: false` renders no writer, no receiver, no stream consumers and no writer Service, and the values schema accepts it (the writer's `config` is then free to be empty). The migration hook never depended on the writer and still runs; observe and query still need Postgres. With the writer elsewhere, the chart refuses `mode: stream`, `workloadIdentity.issuers`, `keysVolume`, `extensions.billing`, a recording component whose `sink` names the release's own front door or is missing (the query service), and a component that would run as the writer's ServiceAccount. New example `charts/audit/examples/external-writer.yaml` (observe, query and a notary CronJob with OpenBao Transit, sinks on SQS) and golden `example-external-writer`.
- A `sink` in the configuration of `audit-query`, `audit-notary` (and `audit-notary-lambda`), `audit verify` and `audit clock-sync` takes **`sqs`** (`queueUrl`, `region`) in place of `url`: records are sent to the ingest queue of a writer that runs elsewhere and acknowledged `queued`, with the pod's own identity as the credential (`sqs:SendMessage` on the queue). Exactly one of `url` and `sqs` is required; the default, `url`, is unchanged. The JSON Schemas and the chart's values schema carry it. See [deployment/aws.md](docs/how-to/aws-run-readers-in-kubernetes.md) and [deployment/levels.md](docs/explanation/levels.md).

## v0.9.0
The Pulumi library delivers the application's catalogue to the writer Lambda from files, and the deployment docs gain the full, lite and log levels.

- The Pulumi library takes the application's catalogue from files: `Writer.CataloguePaths` (merged with `Writer.Catalogues` under the files' base names; an unreadable or empty file, or a name given twice with different content, is refused before anything is created). The catalogue is part of the writer function's package, so a changed catalogue redeploys the writer on the next `pulumi up` and reaches it no other way. Tests pin the Truvity shape (both Lambdas, KMS seals, GOVERNANCE) and the hive shape (writer Lambda, SSE-S3, `NONE`, `Notary.Disabled`, an `ArchiveWriter` role), both outside a VPC. See [deployment/aws.md](docs/how-to/change-what-a-source-records.md).

- New [deployment/levels.md](docs/explanation/levels.md): the `full`, `lite` and `log` levels, the decision-tree presets they map to, and what is implemented in code, Pulumi and the chart. The chart already renders the notary CronJob with the OpenBao Transit signer (golden `transit`); nothing was missing there.

## v0.8.0

The Pulumi library gains optional deployment parts and Object Lock can be enabled later without replacement.

- The Pulumi library grows the options a Talos or kernel deployment needs ([deployment/aws.md](docs/getting-started/aws-lambda.md)). **The AWS provider:** the one invoke, `aws.GetCallerIdentity`, is now made through the component, so it uses the provider passed to `New` (`pulumi.Provider`, `pulumi.Providers`) and works with the default providers disabled, where it used to fail; `Args.AccountID` skips the lookup, and with the notary off there is none. **`Archive.Encryption`** is `kms` (the default, unchanged) or `s3`: SSE-S3, with no archive key, no `kms` grant for it on any role and no `kmsKey` in the configuration. **IRSA:** `Observe.IRSA` lets a Kubernetes ServiceAccount assume the read role by web identity (`sts:AssumeRoleWithWebIdentity`, `<issuer>:sub` pinned to one `system:serviceaccount:<ns>:<sa>` and `<issuer>:aud` to the audience, default `sts.amazonaws.com`), alone or beside `TrustedPrincipalArn`; `ArchiveWriter` creates `<name>-archive-writer`, an IRSA role that puts only under the prefixes it is given (default `seals/` and `keys/`), for a digest running on Talos. **Optional parts:** `Ingest.Disabled` leaves out the queue, DLQ, dedupe table, writer and their alarms, and `Notary.Disabled` the seal key, notary, schedule and alarms, independently; the archive is always created. Outputs of a part that is off are empty strings. New output `ArchiveWriterRoleArn`. For existing callers nothing changes by default, with one widening: the observe reader's policy also reads `schema/` (read only, in step with the IRSA read role). See [deployment/aws.md](docs/reference/aws-pulumi-library.md#optional-parts).

- The Pulumi library's archive bucket can run with no Object Lock, and have it turned on later without being replaced. `Archive.ObjectLockMode` accepts `NONE` as well as `GOVERNANCE` and `COMPLIANCE`, and is now **required**: there is no default, so a caller that left it empty (it used to mean `GOVERNANCE`) must now say which. With `NONE` no Object Lock configuration is created, both functions are configured with `lockMode: none` and send no retention or legal-hold header, and their roles are not granted `PutObjectRetention` or `PutObjectLegalHold`. The bucket's own `objectLockEnabled` is no longer set, because it forces replacement; Object Lock is the separate `BucketObjectLockConfiguration` resource, which AWS accepts on an existing versioned bucket, so `NONE` to `GOVERNANCE` creates that one resource and updates the functions and role policies in place. Versioning is on in every mode. The bucket and both KMS keys are protected from a stack destroy in every mode, where only a `COMPLIANCE` bucket was. A `DefaultRetentionDays` with `NONE` is refused. Object Lock, once on, cannot be turned off. See [deployment/aws.md](docs/explanation/aws-lambda.md#the-lock-modes).

## v0.7.1

- Fixes the broken v0.7.0 release: `go install github.com/truvity/audit/cmd/audit@v0.7.0` failed with a missing `go.sum` entry for `github.com/truvity/audit/sdk v0.7.0`, and the release's `sdk-tag` job failed on a hand-pushed annotated `sdk/v0.7.0` (so `deploy/pulumi/v0.7.0` was never created). v0.7.1 is installable: the root `go.sum` has the SDK's lines, `sdk-tag` compares the tag's peeled commit, and goreleaser ignores `sdk/*` and `deploy/*` tags when choosing the version.


## v0.7.0

This release adds AWS Lambda deployment with DynamoDB deduplication, splits indexing into a separate audit-observe process, adds seals and a notary for verification, ships a Pulumi library, and adopts the v1 bucket layout with per-role database permissions.

- AWS Lambda ([deployment/aws.md](docs/reference/aws-pulumi-library.md)), built and tested and **not deployed**. `cmd/audit-writer-lambda` runs the writer behind an SQS event source mapping with partial batch responses (`ReportBatchItemFailures`), under the same `require: archived` guard: a message that is not a record, or that the sink refuses, is returned to the queue and reaches the DLQ, and a failed write returns every message of the batch. The legal holds are re-read at the start of every invocation, and telemetry is flushed at the end of each and on SIGTERM, because an environment is frozen between invocations. `cmd/audit-notary-lambda` runs the notary's logic on an EventBridge Scheduler schedule, and a run that could not seal a tenant fails the invocation. Both are built by the release as `arm64` zips (`bootstrap`). New configuration: `schemas/config/audit-writer-lambda.schema.json` (`deployment`, `catalogues`, `archive`, `keys`, `dedupe.dynamodb`, `require`), read from the package at `/var/task/audit.yaml`; the notary Lambda reads `audit-notary`'s own file.
- Deduplication on DynamoDB, for a writer with no database: `dedupe/dynamodbdedupe` is a second adapter of the writer's `Dedupe` port, one item per written record id (`pk = DEDUPE#<id>`, TTL on `expires_at`), `Seen` a consistent batch read that treats an expired item as absent, `Mark` a conditional put made once the copies are durable. `writer.Config` gains `Dedupe` (a shared store in place of `Database`) and `Writer.RefreshHolds`. `just test-s3` and the `s3` CI job run it against LocalStack.
- Queue metrics: `audit.queue.message.age`, the age of each message at receive, from SQS's `SentTimestamp`, per transport (`audit_queue_message_age_seconds`).
- The Pulumi library, `github.com/truvity/audit/deploy/pulumi`, a module of its own (tagged `deploy/pulumi/vX.Y.Z` by the release): the archive bucket (Object Lock with the mode as a parameter, `GOVERNANCE` first and `COMPLIANCE` only with an explicit acknowledgement and on a bucket Pulumi protects, versioning, SSE-KMS, public access blocked, Glacier IR at 30 days and Deep Archive at 1 year per `records/<profile>/` prefix), an archive key and a P-384 `SIGN_VERIFY` seal key whose policy keeps the account root from using it, the ingest queue with a DLQ, the DynamoDB table, the two functions outside a VPC with the OTLP extension layer, a role per function under the path `/audit/` (`audit-writer`, `audit-notary`, with `audit-observe-reader` and `audit-scheduler`), least privilege and `sts:GetWebIdentityToken` pinned with `ForAllValues:StringEquals` on the audience, the notary's schedule, a cross-account read role for observe, and the CloudWatch alarm set to SNS and alert-ingress: throttles, DLQ not empty, oldest message age, errors, and the notary going silent. `just pulumi-test` runs its tests on Pulumi's mocks, and checks the configuration it ships against the binaries' JSON Schemas; it is a CI job.
- Chart: `telemetry.otlp` (`endpoint`, `protocol`, `extraEnv`) renders `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL` and a per-component `OTEL_SERVICE_NAME` on every pod; empty renders nothing, and the existing goldens are byte for byte unchanged. `extraEnv` takes `OTEL_*` keys only and not the endpoint, and the endpoint must be an http(s) URL. A golden (`telemetry`) and four refusals cover it.
- **Breaking:** observe follows the bucket by cursor and the writer stops indexing ([0020](docs/decisions/0020-observe-follows-the-bucket.md), [0024](docs/decisions/0024-indexer-and-query-are-separate-processes.md)). A new binary and image, `audit-observe`, lists `records/<profile>/<tenant>/` from a durable cursor kept in Postgres (`index_cursor`, schema version 6), indexes the objects older than a settle window (`settle`, default 2m), and moves the cursor in the same transaction as their rows; indexing stays idempotent, so a re-read object changes nothing. Profiles and tenants are discovered by listing, and the listing is the source of truth: `wake.nats` or `wake.sqs` (bucket notifications) only make a pass run sooner, and a lost one costs at most `interval` (default 30s). A record's catalogue is read from the archive's own `catalogue/<app>/<version>` and the extension schemas beside it. An object that does not decode is skipped and counted; a fetch or a catalogue that fails stops that tenant's cursor and is retried, and the other tenants carry on. It is a process of its own and not a mode of `audit-query`, because it holds the index's write credential and the query service must hold none of it. What the writer loses:
  - The `Roller` no longer indexes: `Roller.Indexer`, `OnIndexDeferred` and `OnIndexed` are gone, and `Add` and `AddExpiring` lose their `index.Fields` argument. The writer's metrics `audit.writer.index.lag` and `audit.writer.index.deferred` are gone with them; their replacements are `audit.observe.index.lag` (the time from an object's put to its rows being indexed, floored by the settle window) and `audit.observe.index.deferred` (by profile and `reason`, `retry` or `unreadable`), with `audit.observe.objects.indexed` and `audit.observe.records.indexed`. The alert `AuditIndexLagHigh` reads the first and its default threshold is **600s, not 30s** (`alerts.rules.indexLag.thresholdSeconds`), because the settle window is its floor; `AuditIndexRowsDeferred` reads the second and counts objects, not rows. Both keep their firing and silent cases in `vmalert-tool`, and the dashboard's two panels read the new series. Anything that alerts on the old names must move.
  - An addendum finds the earlier record it extends by scanning the archive within the scanner's budget, where it used to ask the index. The writer's role can no longer read the index.
  - **Search lags the archive by the settle window.** A record is in search about two minutes after it is acknowledged, at the least; anything that must see it sooner reads the sink's acknowledgement.
- **Breaking:** one database role per part, none of them the owner. `audit migrate` grants each named role what its part needs and takes back the rest (`migrate.config.writer`, `.observe`, `.reader`, `.purge`; `--writer`, `--observe`, `--reader`, `--purge`): the **writer** the deduplication table, the catalogue registry and the key directory and none of the index; **observe** the index and its cursors, read and write, and `execute` on a new function `audit_ensure_month(date)` that creates a month's partitions as the owner (the indexer is not the owner, and creating a partition takes one); the **query service** select on the index's tables only, still bound by row-level security (it used to be granted select on every table in the schema, and on those made later); **purge** delete from the index and the deduplication table. A role named for two parts, or the owner, is refused. Before, the writer's role owned every table. Create the roles and re-run the migration, and give the writer, the indexer, the query service and the purge job each its own `database.url`; the migration's `database` is the owner and nothing else connects as it. `index/postgres` gains `GrantRoles`, `GrantWriter`, `GrantObserver`, `GrantPurger`, `Index.Advance`, `Index.Cursor` and `Index.ResetCursor`.
- **Breaking:** the chart renders the indexer: `observe.enabled`, a Deployment `<fullname>-observe` (no Service) with a ServiceAccount of its own (`<fullname>-observe`, `observe.serviceAccount`), a ConfigMap, `image.observe` and `images.audit-observe`, and `schemas/config/audit-observe.schema.json`. It sits beside `query`, which is unchanged. The chart now refuses an indexer that runs as the writer's, the query service's or the receiver's ServiceAccount, that connects as the writer's, the query service's or the migration's database role, and a writer that connects as the migration's role. Its hook-order, golden and refusal fixtures are updated (nine new refusals), and the examples show a role per part. The release builds and publishes the `audit-observe` image and the alerts' default threshold is as above.
- `audit reindex` shares what turns an object into rows with the indexer (`internal/observe.ReadObject`), so a batch over a range and the following of the bucket cannot disagree, and a test holds them to equal rows and counts. `--catalogue` is now optional: a record's catalogue is read from the archive where the files do not hold it, and one that cannot be found is still an error. `--reset-cursor [--tenant]` rewinds the indexer's cursor for a profile, which makes it read the profile again from the start.
- Tests: the indexer is held to one suite over the in-memory archive, a real S3 (`just test-s3`) and a real Postgres (`just test-postgres`): resume after a restart, the settle window, a put that lands behind a key already listed, idempotence under a cursor reset, several tenants and profiles, a lost wake-up found by the poll, a wake-up that shortens it, an unreadable object, a fetch that fails, and the cursor moving with its rows. The roles are held to what each may and may not do as the role itself.
- **Breaking:** the writer writes the v1 bucket layout only, and every reader reads only it: **read v0 with <= 0.6.x**. There is no compatibility and no migration (ADR 0018, 0003's keys superseded). The v0 archive is left where it is, locked until its retention lapses, and stays readable only with the previous release's CLI (`audit verify`, `audit reindex` and the digest chain of v0.6.x). The layout is the [bucket contract](docs/reference/bucket-contract.md):
  - A batch is one object per profile and tenant, keyed `records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>` by the time of **ingest**, not the time of any record in it; the ULID is made when the put starts and never goes backwards within a writer, so keys sort in the order batches arrived. A record's own date no longer decides where it lives.
  - The body is zstd-compressed NDJSON, one `{"hash":"<hex sha256 of the canonical record>","record":{...}}` line per record; the object carries the metadata `format` (`1`), `sha256` (of its stored bytes) and `count`. The put is conditional (`If-None-Match: *`). Retention and legal hold are the profile's, as before.
  - The catalogue of each application is written once at `catalogue/<app>/<version>`, exactly as registered. A key already present with the same bytes is success; with other bytes the writer refuses to start (at start-up for the catalogues it runs with, otherwise at the first record that names one). The Postgres registry stays the writer's; observe reads the bucket copy and the extension schemas beside it, so it needs no grant on the registry. Extension schemas and the record's schema and proto stay under `schema/`, which the contract does not cover.
  - A profile's `prefix:` is removed from the deployment's profiles: the profile's name is the first key component, and it must not contain `/`. A record whose `tenant_id` contains `/` is dead-lettered with the reason.
  - `audit reindex`, the `s3scan` searcher and legal-hold sweeps read the v1 layout. `reindex --from/--to` and the scan's walk are ranges of ingest time; `s3scan` orders rows by `occurred_at` within an ingest day and reads a window of `occurred_at` plus a `Lateness` (default 24h) of arrival. A hold is one prefix, `records/<profile>/` or `records/<profile>/<tenant>/`.
  - `audit verify` checks, for every record object ingested in `[from, to)`, its key, its metadata, the sha256 of its bytes and the hash of every record, and with `--deployment` the lock. It needs the archive and nothing else: `--public-key`, `--lookback` and `--record` (and `publicKeyFile`, `lookback` and `record` in the job's file) are removed, and nothing is written under `verified/` any more. The scheduled job still records `audit.digest.verified` and `audit.digest.failed` per ingest hour until seals replace them.
- **Breaking:** the v0 digest job is removed, because it read the v0 layout and seals (ADR 0019) replace it; there is no v1 reader for it to run. Gone: `audit digest` and `schemas/config/audit-digest.schema.json`; in the chart, `jobs.digest`, its CronJob, ConfigMap and ServiceAccount (`<fullname>-digest`) and the check that it does not sign in as the writer; the `AuditDigestStale` alert (`alerts.rules.digestStale`, now six rules), the `audit.digest.age` metric and its two dashboard panels. The chart cannot run the job without the code, so it is removed outright and not left behind a flag. `audit key public` and the signers stay for seals. The `verified_at` and `digest_id` fields of a record's provenance stay in the API and are empty until seals set them. Remove `jobs.digest` and `alerts.rules.digestStale` from your values, and the digest role from your bucket policy.
- A conformance suite for the contract, `internal/bucketcontract`: black-box over any `store.Store`, it checks the key grammar, the envelope of every line, the metadata, the sha256, the hash of every record, the ordering and uniqueness of keys, and the catalogue, and names the rule each finding breaks. It runs against the in-memory store and against S3 (LocalStack, in the `s3` CI job, which fails if it was skipped); the same checker is what `audit verify` applies to each object. The vectors for seals' Merkle root come with the notary.
- Lifecycle, documented and not created (the chart creates no bucket): Glacier Instant Retrieval at 30 days and Deep Archive at 1 year, one rule per profile on `records/<profile>/` (ADR 0023). See [the S3 guide](docs/how-to/prepare-the-bucket.md).
- Docs: the purge job works on the index only and never needs an archive role; earlier notes said otherwise.

- Seals and the notary ([0019](docs/decisions/0019-seals.md), [the contract](docs/reference/bucket-contract.md#seals)). `audit-notary`, a binary and an image of its own, writes for each profile, tenant and hour that has ended and settled (default 10 minutes) one seal at `seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws`: a JWS, ES384, `typ` `audit-seal+jws`, `kid` the RFC 7638 thumbprint of the signing key, whose payload (`audit.v1.Seal`, in `proto/audit/v1/seal.proto`) carries `tenant`, `profile`, `hour`, `count`, `root`, `first`, `last`, `prev`, `sealed_at` and `meters`. The root is the RFC 6962 Merkle tree over the hash of every record, by object key and then line (`internal/merkle`, with the vectors for n = 0, 1, 2, 3 and 5 and audit paths); `prev` is the SHA-256 of the previous seal's bytes. Quiet hours are sealed too, so a missing seal is a fault. A run is idempotent (a seal is put with `If-None-Match`), resumes from the last seal, and refuses to seal an hour whose objects do not match their own metadata, and any hour after it. It writes `keys/roots.jwks` once if the bucket has none, and refuses to sign with a key the file does not list. Signing a delegation is not built (decision L3); the notary signs with a root.
- **Breaking:** the common catalogue is **2.0.0**: `audit.digest.written`, `.verified` and `.failed` are `audit.seal.written`, `.verified` and `.failed`, the target type `digest` is `seal`, and `schemas/v1/common/digest-written.json` is `seal-written.json`. The SDK is released with it: the root requires `github.com/truvity/audit/sdk` v0.7.0, and `go.work` carries a versioned `replace` until that tag exists.
- Signers: a seal is signed with a **P-384** key. `keys.KMSSigner` takes `ECC_NIST_P384` (`ECDSA_SHA_384`, over a SHA-384 digest) as well as `ECC_NIST_P256`; `keys.TransitSigner` takes `ecdsa-p384` as well as `ed25519`; `keys.LocalSigner` loads a P-256 or P-384 key in PKCS#8 or SEC 1 PEM (`keys.NewLocalP384`); `keys.Verify` accepts ES384 beside ES256 and ed25519; `keys.ParseECPublic` refuses any other key as a seal key. `audit key public` gains `--thumbprint` (what a verifier pins) and `--jwks` (the form of `keys/roots.jwks`).
- `audit verify --root <thumbprints>` (`seals:` in the job's file: `roots`, `settle`, `grace`) checks the seals of the range against the roots it pins and nothing else in the bucket: the signature (a pinned root, or a delegation from one, within its window of at most 25 hours and its scope, not revoked), the chain through `prev`, each hour's count, first and last key and root recomputed from the objects as they are now, the lock, and that no due seal is missing. Findings name the rule they break (`seal.signature`, `seal.chain`, `seal.root`, `seal.missing`, ...). The `audit.seal.verified` and `audit.seal.failed` events replace the digest ones and are recorded only when seals were checked.
- Conformance: `internal/bucketcontract` checks the seals half of the contract (`CheckSeals`), and its suite runs the notary against the in-memory store and S3 (LocalStack in the `s3` CI job), signing with a key file and with a KMS `ECC_NIST_P384` key, checks a seal by hand with nothing of the repository's own code, and hands the checker each broken seal in turn.
- Chart: `jobs.notary` (off by default), an hourly CronJob in the new `image.notary` under a ServiceAccount of its own, with `audit-notary`'s schema (`schemas/config/audit-notary.schema.json`) embedded in the values schema. The chart refuses a notary that runs as the writer: the release's ServiceAccount, the writer's cloud-role annotations, or the writer's OpenBAO role. `jobs.verify.config.seals` pins the roots. `alerts.rules.sealStale` (`AuditSealStale`, now seven rules, 3h) and the `audit.seal.age`, `audit.seal.written` and `audit.seal.failures` metrics, with a dashboard tile and panel; the age is as of the notary's last run, and the alert and the dashboard add the time since.
- A record read through the query service names, in `provenance.digest_id`, the seal that covers its hour when the service is given the archive. `verified_at` stays empty until a verifier marks it: a seal that exists is not a seal that was checked.

## v0.6.1

- `go run github.com/truvity/audit/cmd/audit@<version>` works again; v0.6.0's root go.mod carried a replace directive for the SDK, which Go refuses for `go run` and `go install` of a package. The root now requires `github.com/truvity/audit/sdk` at a released version, and a committed `go.work` joins the two modules for development. CI builds the root without the workspace (`just installable`) and fails a change to `sdk/` that leaves the root's require behind (`just sdk-require`).
- **Behaviour change:** the default trace sampler, when `OTEL_TRACES_SAMPLER` is unset, is a parent-based `always_on` (the OpenTelemetry SDK default) and keeps every trace; it was a parent-based ratio of 0.1. To thin traces again, set `OTEL_TRACES_SAMPLER=parentbased_traceidratio` and `OTEL_TRACES_SAMPLER_ARG=0.1`.
- **Breaking:** the chart gives every component a ServiceAccount of its own, so that a cloud role (EKS Pod Identity, IRSA) can differ per component and a receiver does not share the writer's identity. Before, the writer, the receiver, the consumers and the `purge` and `clock-sync` jobs all ran as the release's ServiceAccount (`<fullname>`). Now the writer (the consumers, in stream mode) keeps `<fullname>`, and these change: the receiver (stream mode) runs as `<fullname>-receiver`, the purge job as `<fullname>-purge` and the clock-sync job as `<fullname>-clock-sync`. `<fullname>-query`, `-digest` and `-verify`, and the migration hook's `-migrate`, are unchanged. Any Pod Identity association or IRSA trust policy bound to `<fullname>` must be moved to the new ServiceAccount names; the purge and clock-sync jobs need no cloud role at all, since the purge job works on the index database only. To keep the old name for a component, set `jobs.purge.serviceAccount.create` or `jobs.clockSync.serviceAccount.create` to `false`, which runs it as the release's `serviceAccount` again (or set `.serviceAccount.name` to pick any name). The receiver cannot keep the old name: running as the writer's account is what the new refusal is for. Each component's `serviceAccount` takes `create`, `name` and `annotations`. The chart now refuses a stream-mode install whose receiver and writer resolve to one ServiceAccount name, because a receiver must not hold the archive's write identity.

## v0.6.0

This release splits the consumer SDK into its own module with independent import paths, configures every binary from one validated YAML file, adds durability guarantees on every acknowledgement with the `require:` guard, adds the `log` and `sqs` sinks for flexible routing, and adds traces and metrics through an OpenTelemetry exporter allowlist, plus the chart's `alerts` and `dashboards` render modes.

- **Breaking:** the Go packages an application imports to emit records are a module of their own, `github.com/truvity/audit/sdk`, which does not depend on the writer's database driver, NATS server, AWS SDK, OpenBAO client, JWT library or the OpenTelemetry SDK and exporters. Take the step: `go get github.com/truvity/audit/sdk@<version>`, rewrite the import paths below, and drop `github.com/truvity/audit` from your `go.mod` if nothing else in it is imported. The root module is no longer an import target for these packages (it carries a `replace` for the SDK and is consumed as binaries, images and the chart), and the SDK is tagged `sdk/vX.Y.Z` at the same commit and version as `vX.Y.Z`. `just sdk-closure` fails the gate if a server-only dependency enters the SDK.

  | old import path | new import path |
  |---|---|
  | `github.com/truvity/audit/record` | `github.com/truvity/audit/sdk/record` |
  | `github.com/truvity/audit/gen/audit/v1` | `github.com/truvity/audit/sdk/gen/audit/v1` |
  | `github.com/truvity/audit/gen/audit/v1/auditv1connect` | `github.com/truvity/audit/sdk/gen/audit/v1/auditv1connect` |
  | `github.com/truvity/audit/emit` | `github.com/truvity/audit/sdk/emit` |
  | `github.com/truvity/audit/catalogue` | `github.com/truvity/audit/sdk/catalogue` |
  | `github.com/truvity/audit/sink` (`Sink`, `Request`, `Result`, `Client`, `NewClient`, `Memory`, `Discard`, `Func`, `Durability`, `Guard`, `Require`) | `github.com/truvity/audit/sdk/sink` |
  | `github.com/truvity/audit/sink/logsink`, `.../sink/sinktest` | `github.com/truvity/audit/sdk/sink/logsink`, `.../sdk/sink/sinktest` |
  | `github.com/truvity/audit/auth` (`Principal`, `Grant`, `Rule`, `Declarative`, `Middleware`, `TokenFile`, `Workload`, `ErrUnauthenticated`) | `github.com/truvity/audit/sdk/auth` |
  | `auth.JWT`, `auth.NewJWT`, `auth.Issuer`, `auth.AccessRoster` | `github.com/truvity/audit/authn` (`authn.JWT`, `authn.NewJWT`, `authn.Issuer`, `authn.AccessRoster`) |
  | `sink.NewHandler`, `sink.Handler`, `sink.Receiver` | `github.com/truvity/audit/sinkserver` (`sinkserver.NewHandler`, `.Handler`, `.Receiver`) |
  | `github.com/truvity/audit/sink/natssink`, `.../sink/sqssink` | unchanged, in the root module: an emitter does not publish to a stream, the receiver does |
  | `github.com/truvity/audit/internal/metaschema` (internal) | `github.com/truvity/audit/sdk/metaschema` |

  The generated Go moves with its path: `buf.gen*.yaml` write it to `sdk/gen`, and `just drift` and `just drift-ts` check that directory. `preset.Category` and `preset.Class` are now aliases of `catalogue.Category` and `catalogue.Class`; the three meta-schemas move from `schemas/` to `sdk/schemas/`, which `schemas/config/` does not (so the published schema URLs are unchanged).

- Traces. The writer, the receiver and the query service serve HTTP server and Connect spans, `sink.Client` and the NATS and SQS publishers add client and producer spans, and the W3C `traceparent` crosses a queue in the message (NATS headers, SQS message attributes) so a consumer continues the trace and links the other messages of its batch. A tracer provider exists only when `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set; without it nothing is exported and nothing fails. The sampler is the standard `OTEL_TRACES_SAMPLER` (a parent-based ratio of 0.1 when unset). Spans leave the process through an allowlist: only action, outcome, tenant id, durability, delivery, counts, transport and the shape of the request survive, never an actor, a subject, a client address or record data, and events, status text and link attributes are removed. `audit-query` now starts telemetry too.
- Metrics: `audit.sink.records.acknowledged` (by durability and transport), `audit.sink.records.rejected`, `audit.sink.write.duration` (by transport and outcome), `audit.sink.consume.failures`, `audit.writer.index.lag`, and `audit.digest.age`, observed by the writer from the archive rather than pushed by the digest job. No series is labelled by tenant.
- The chart renders alert rules and dashboards. `renders: alerts` renders only a `VMRule` (or `PrometheusRule`) of seven rules, with `alerts.ruleLabels` for routing labels such as `k8s_cluster_name`, for a second install beside the write path; `renders: dashboards` renders only the Grafana sidecar ConfigMaps of one audit overview that passes truvity/observability's dashboard lint. The default, `renders: app`, renders exactly what it did. See [telemetry](docs/reference/telemetry.md), which has each alert's threshold and runbook entry. `just telemetry`, a CI recipe, runs the rule unit tests on `vmalert-tool` and the dashboard lint, both at pinned releases.
- The acknowledgement of `SinkService.Write` says how durable the batch is. `WriteResponse` gains `durability` (`DURABILITY_LOGGED`, `QUEUED`, `ARCHIVED`, ordered, `UNSPECIFIED` for a hop that did not say), added without renumbering; `sink.Result.Durability` carries it and the Connect client and handler pass it through. The writer reports `Archived`, `natssink.Publisher` and `sqssink.Publisher` report `Queued` after every acknowledgement, `sink.Memory` and `logsink` report `Logged`, `sink.Discard` reports nothing, and `sink.Receiver` reports what its next hop does. See [ADR 0017](docs/decisions/0017-sink-durability-and-transports.md).
- `sink.Require(s, min)` refuses, at start-up, a chain whose strongest durability (`Guarantees()`) is below `min`, and `sink.Guard(s, min)` also fails any write whose acknowledgement is weaker at run time. `sink.ParseDurability` reads the spelling a configuration will use; `sink.Client.Expecting` says what a remote service is configured to give.
- A `log` sink, `sink/logsink`, writes one JSON line per record, `MESSAGE` and `AUDIT_RECORD`, and reports `Logged`.
- An `sqs` sink, `sink/sqssink`: a publisher that sends records with `SendMessageBatch`, deduplicates by record id on a FIFO queue and carries it as an attribute otherwise, reports `Queued` only once SQS has taken every message, refuses what SQS refuses for the message's own sake and sends again what it failed for its own; and a consumer that writes each receive to a target and deletes only what the target took. `just test-s3` and the `s3` CI job now run it against LocalStack's SQS.
- `sink/sinktest.Run` is a conformance suite for any sink: declared and reported durability, a batch taken whole, an empty request, a cancelled context, a refusal surfaced, a repeated record kept once where the sink claims it. It runs against `sink.Memory`, the Connect client and handler, `natssink`, `logsink` and `sqssink`.

- `require:` is read from the configuration. `audit-writer` takes `require` (`logged`, `queued` or `archived`), wraps its chain in `sink.Guard` and refuses to start when the chain can never give it; the default is the strongest the mode can give, `archived` for a writer and `queued` for a receiver, which holds no archive and so cannot promise `archived` (the schema refuses it there). `audit-query` and the `digest`, `verify` and `clock-sync` jobs take `require` and `sink.expect` (what the writer they record through is configured to give, passed to `sink.Client.Expecting`); `require` without an `expect` at least as strong is a start-up error, and unset checks nothing, as before.
- The receiver's onward transport is selectable: `forward: {nats: ...}`, `{sqs: {queueUrl, region, fifo}}` or `{log: {}}`, exactly one. A writer's optional consumer is `consume: {nats: ...}` or `{sqs: {queueUrl, region, fifo, batch, visibility}}`. `stream` stays as the NATS shorthand (`forward.nats` in a receiver, `consume.nats` in a writer), so an existing NATS file and its rendered manifests are unchanged. The `log` sink is allowed only with `require: logged`. SQS takes no credentials from the file: it uses the SDK's ambient ones, which on Kubernetes is the pod's workload identity. The chart's stream-mode check accepts `writer.config.consume` as well as `stream`; `charts/audit/examples/sqs.yaml` is an SQS install with its golden, and `tests/invalid/audit/` refuses `log` with `require: queued`, two transports in `forward`, and `require: archived` on a receiver.

### Breaking: one validated configuration file, in place of flags and environment

Every flag and variable and its replacement key: [upgrade to v0.6](docs/how-to/upgrade/v0.6.md).

`audit-writer` and `audit-query` are configured by one YAML file, `--config <file>`, and by nothing else. The file is validated against a JSON Schema before anything starts, so an unknown key, a missing required key or a value of the wrong type is a start-up error that names the path to it, and a flag misspelt in a chart is a failed `helm template` rather than a container that ignores it. The schemas are committed as `schemas/config/audit-writer.schema.json` and `audit-query.schema.json` (generated by `just config-schemas`, checked by `just drift`, served with the other schemas on GitHub Pages), and use the shared shapes of [truvity/policy](https://github.com/truvity/policy) and its loader. See [ADR 0021](docs/decisions/0021-one-validated-configuration-file.md) and [the configuration reference](docs/reference/configuration.md), which lists every key.

- **Secrets are the one thing the environment adds, and only the ones the file names.** A field ending in `Env` (`database.passwordEnv`, `bucket.credentialsEnv`, `openbao.tokenEnv`) holds the name of the variable; the process reads exactly those. A password inside a database URL, or a key such as `password`, is refused. A file's tokens, roots and keys are named by path, never held.
- **Telemetry is only the standard `OTEL_*` variables.** `telemetry.otlpEndpoint` is gone from the chart; nothing about telemetry is in a configuration file.
- **The flags and `AUDIT_*` variables of `audit-writer` and `audit-query` are removed**, with no deprecation period: `--bucket`, `--database`, `--listen`, `--mode`, `--stream-url`, `--key-provider`, `--workloads`, `--anonymous-writes`, `--grants`, `--sink`, `--exports`, `AUDIT_BUCKET`, `AUDIT_DATABASE`, `AUDIT_REGION`, `AUDIT_EXPORTS_*`, `BAO_TOKEN` as a fallback, and the rest. Only `--config`, `--version` and `--help` remain. The build's version comes from the release, not from `--version`/`AUDIT_VERSION`.
- **The scheduled jobs of `audit`** (`digest`, `verify`, `purge`, `clock-sync`, `migrate`) take `--config <file>` in place of every other flag (only `--json` may accompany it), each validated against its own schema (`audit-digest`, `audit-verify`, `audit-purge`, `audit-clock-sync`, `audit-migrate`). The interactive flags of `audit` stay, for a person at a keyboard. `verify` in a file takes `profiles` (default: every profile) and runs them in one job.
- **Exports are explicit.** The exports bucket no longer inherits the archive's endpoint and path style, and carries its own `credentialsEnv`; `ca` points a bucket at a private certificate authority; Postgres pool size is `database.maxConnections`.
- **The chart passes `config` through.** Each component (`writer`, `receiver`, `query`, `migrate`, `jobs.digest`, `jobs.verify`, `jobs.purge`, `jobs.clockSync`) has a `config:` block that is rendered as it stands into a ConfigMap, mounted at `/etc/audit/config.yaml`, and validated by `values.schema.json` against the schema its binary uses (the chart's schema is generated and embeds them). Platform values stay values: `replicas`, `mode`, images, service accounts, `resources`, and, per component, `secretEnv` (a Secret's key into the variable a config names), `secretMounts` and `tokens` (a projected service-account token). The flag-shaped values are removed, not aliased. A Go test holds every rendered ConfigMap to the values' `config` and to its binary's schema, and `tests/invalid/audit/` holds the refused configurations as overlay files. The verify job is one CronJob, `<release>-verify`, where it was one per profile.
- **Where the chart's old refusals live.** What spans two components' configs stays a chart refusal (the digest job or resolve signing in as the writer, the query service connecting as the writer's database role, the local key directory with no volume unless `keysVolume.ephemeralIsAcceptable`, replica counts, workload identity, stream mode's shape); what is local to one config is in its schema or the binary's load (exactly one digest signer, exactly one way to sign in to OpenBAO, `ntp` with at least one reference, the lock mode, the stream's `ackWait`). `tests/invalid/audit/` holds a fixture for each.
- **The packaged chart pins its images by digest.** `values.yaml` declares `images.audit-writer`, `images.audit-query` and `images.audit` (registry, repository, tag, digest), which `helmctl package --manifest` fills in from what the build published, as the shared pipeline expects; a pinned image wins over `image.*`, and a chart installed from a directory, with none, uses `image.*` as before. Until now the kind tier installed the released images beside the checkout's chart, so it could not tell a chart that disagreed with the binaries it was about to ship.

#### Migrating values

The snippet shows the shape of the change for a direct-mode installation with an index; `docs/reference/configuration.md` has the full mapping, for the flags as well.

```yaml
# before
bucket: audit-archive
prefix: app
region: eu-example-1
kmsKey: alias/audit
lockMode: governance
anonymousWrites: true
database:
  existingSecret: audit-db        # a Secret whose key `url` held the whole URL
keys:
  provider: none
jobs:
  digest:
    kmsKey: alias/audit-digest
```

```yaml
# after
writer:
  config:
    deployment: /etc/audit/deployment.yaml   # the chart renders it from `profiles`
    anonymousWrites: true
    archive:
      bucket:
        name: audit-archive
        region: eu-example-1
      prefix: app
      lockMode: governance
      kmsKey: alias/audit
    database:
      url: postgres://audit@db.example.com:5432/audit?sslmode=verify-full   # no password
      passwordEnv: AUDIT_DATABASE_PASSWORD
  secretEnv:
    - name: AUDIT_DATABASE_PASSWORD
      secretName: audit-db        # now holding the password under `password`
      key: password
migrate:
  enabled: true                   # was implied by `database`
  config:
    database: { url: postgres://audit@db.example.com:5432/audit?sslmode=verify-full, passwordEnv: AUDIT_DATABASE_PASSWORD }
  secretEnv:
    - { name: AUDIT_DATABASE_PASSWORD, secretName: audit-db, key: password }
jobs:
  digest:
    config:
      deployment: /etc/audit/deployment.yaml
      archive: { bucket: { name: audit-archive, region: eu-example-1 }, prefix: app, lockMode: governance }
      sink: { url: http://audit:8080 }       # the writer's Service
      signer: { kmsKey: alias/audit-digest }
```

## v0.5.3

- The chart deletes the migrate hook's resources once they succeed. Its Job, ServiceAccount and ConfigMap kept only `before-hook-creation`, so after a successful sync they lingered in the cluster and showed in ArgoCD as resources requiring pruning. They now carry `hook-succeeded` as well; a hook that failed is kept for debugging.

## v0.5.2

- A writer whose stream consumer has stopped no longer goes on answering its health check. `natssink.Consumer.Run` returns only when the connection is closed for good or the request is invalid, and `audit-writer` logged "the stream consumer stopped" and carried on with no consumer, so Kubernetes never restarted the pod and records piled up in the stream unread. `/healthz` now answers 503 once the consumer has stopped without being asked to, and the chart's liveness probe, which already reads `/healthz`, restarts the container. A stop asked for by SIGTERM or a cancelled context is an orderly shutdown and leaves the check passing.

## v0.5.1

- The writer no longer warns `no configured profile keeps these` for an action whose catalogue names a profile this deployment does not configure, when another profile it names does keep the records. `audit.catalogue.registered` and `audit.digest.written` name `evidence` beside `security` for trust-service deployments, so every other deployment logged a warning about records it was in fact keeping. That case is now a debug line naming the profiles that keep the action and the ones not configured; the warning is for an action none of whose profiles is configured, whose records are dead-lettered, and it says so.

## v0.5.0

- Schema `$id`s move from `https://schemas.truvity.com/audit/v1/...` to `https://truvity.github.io/audit/schemas/v1/...`, and `.github/workflows/pages.yaml` serves the files there so an `$id` resolves. The catalogue loader accepts the old identifiers as aliases of the new ones (`catalogue.CanonicalID`) and the common catalogue keeps version 1.0.0, so archived catalogues and schemas keep validating; everything newly generated uses the new base. See [ADR 0015](docs/decisions/0015-schema-ids-on-github-pages.md).
- A reconnect to the stream no longer fails a publish. The client fails every acknowledgement outstanding when its connection drops, and `natssink.Publisher.Write` returned that `nats: server is disconnected` to its caller — for a `block` record, the action it recorded. Records whose publish failed to a reconnect are now sent again once the connection is back, within the same timeout, under the same `Nats-Msg-Id`, so the stream's duplicate window absorbs any that had landed. A refusal from the stream is still returned at once.
- `natssink.Consumer.Run` backs off between failed pulls, from 100ms doubling to 5s and back to 100ms after a pull that succeeds, where it used to ask again at once: a stream electing a leader answered thousands of pulls a second with `no responders`, each a log line. A failed fetch is now reported and retried rather than ending the run; only a closed connection or an invalid request ends it.
- The receiver and the writers reopen their stream connection 30 seconds before the projected token they presented expires, reading the `exp` claim unverified, instead of waiting for the broker to expire the session. Every message the NATS client has goes through slog: `authentication expired` is INFO, and the disconnect that follows it or a planned reconnect is INFO; the client's bare stderr line for asynchronous errors is gone. See [the stream](docs/how-to/run-stream-mode.md).
- `.github/policy-conformance.yaml` exempts `internal/s3test` from the contract's C13 `region` check: it is a test-only helper.

## v0.4.0

- `charts/audit` gains `values.schema.json`; the schema surfaced and fixed a mis-indented test fixture that had silently dropped a NetworkPolicy from a golden render.
- `vuln` is no longer part of `check`; `.github/workflows/security.yaml` runs it on its own so a new CVE cannot turn the gate red. `renovate.json` extends the shared preset.
- `Chart.yaml` commits `0.0.0`; the release stamps the version. Examples use `eu-example-1`; the live-S3 test helpers keep the CI account's real region.
- README gains `Consumers`, `Neighbours` and `Releasing`; CHANGELOG headings follow `## vX.Y.Z`; ci-workflows pins unified at v3.13.1.

## v0.3.1

### The archive writes to a store that is not AWS, whatever the object carries

Every record object is zstd-encoded, and on Cloudflare R2 not one of them
could be written: `403 SignatureDoesNotMatch`, while the uncompressed
schema and profile objects beside them wrote fine, which made it look like
anything but a signing problem. The cause is that the store asked the SDK
for a checksum ALGORITHM, leaving the SDK to choose how to send it, and for
an object that also carries a Content-Encoding it chooses the aws-chunked
trailer -- which AWS accepts and R2 signs differently. The store now
computes the SHA-256 itself and sends the VALUE, so the choice is no longer
the SDK's to make per store. The object carries the same checksum it always
did, and nothing changes on AWS.

Measured against a real bucket: algorithm plus encoding fails, either alone
succeeds, the precomputed value succeeds with both.
`internal/s3test` gains the check, against a bucket the environment names
(`AUDIT_S3_COMPATIBLE_BUCKET`, `AUDIT_S3_COMPATIBLE_ENDPOINT`) because no
emulator reproduces it -- an emulator accepts what the SDK sends.

## v0.3.0

### The receiver and the writers identify themselves to the stream

`audit-writer` connected to the broker with no credentials and had no way
to carry any, so a deployment whose broker verifies who connects could not
run stream mode at all. It now takes `--stream-token-file`
(`AUDIT_STREAM_TOKEN_FILE`): a file whose contents both ends of the stream
present as their NATS token, read afresh on every connect because a
projected service-account token rotates, and a refusal no longer ends the
client's reconnecting -- the next attempt reads the file again. The option
list is one helper shared by the receiver and the writer, so the two cannot
drift. Empty, the flag changes nothing.

The chart projects that token under `stream.token`: `enabled` (off, so an
existing values file renders exactly as it did), `audience` (`nats`) and
`expirationSeconds` (3600), mounted into every pod that reaches the stream
and only when one is configured. The broker's auth callout -- the thing
that reviews the token and maps the namespace to an account -- is the
deployment's, and the stream page says what it must accept. The chart's
network policy is ingress-only and is unchanged: it never governed what
the writer may reach.

### The archive runs on any S3-compatible store

The store was AWS by omission: the binaries built an S3 client with no
endpoint, and the chart had no way to hand one credentials that were not
the pod's own identity. Every command that opens the archive now takes
`--endpoint` (`AUDIT_S3_ENDPOINT`; the SDK's `AWS_ENDPOINT_URL_S3` works
too) and `--path-style` (`AUDIT_S3_PATH_STYLE`), and with an endpoint set
the SDK's default CRC32 request checksum -- which AWS answers and other
stores may refuse -- is sent only where an operation requires one. The
SHA-256 the archive names on every put is unchanged. With no endpoint the
client is exactly what it was.

The chart gains `endpoint`, `pathStyle` and `existingSecret` beside
`bucket`, all inert by default, and the same three under `query.exports`
for an exports bucket on a store of its own. `existingSecret` reaches every
container that touches the archive through `envFrom`; the exports' Secret
reaches the one query process as `AUDIT_EXPORTS_*`, a second identity
beside the archive's. With the defaults the chart renders what it rendered
before, apart from what the next section adds.

### The lock is demanded where a framework demands it

[0014](docs/decisions/0014-lock-modes-and-store-tiers.md). Object Lock was
mandatory, which made two things one: the Object Lock API, which most
S3-compatible stores lack, and the reading that every framework demands
WORM storage, which the presets' own citations do not support. Now:

- A preset's `object_lock_mode` is the LEAST lock its framework demands,
  and may be `none`. `pci-dss`, `nen-7513`, `dora` and `evidence-etsi`
  demand `compliance`; `security`, `history` and `billing-nl` demand
  `none`, and each carries a one-line `note` saying why. Composition takes
  the strictest: none < governance < compliance.
- The writer's `--governance` is deprecated in favour of `--lock-mode
  compliance|governance|none` (`AUDIT_LOCK_MODE`), which every command that
  writes the archive takes: the digest and verify jobs write into the same
  store. The chart's `governance` is deprecated in favour of `lockMode`.
- The writer, the digest job and the verify job REFUSE TO START when a
  composed profile demands a stricter lock than the deployment writes with,
  naming the profile and both modes. The chart cannot make that refusal --
  the presets' readings live in the binaries -- and says so.
- On a store with no lock, `ExtendRetention` and `SetLegalHold` answer
  `store.ErrNotLockable`. An addendum that could not lengthen a lock is
  recorded in the trail as before; `audit hold place` is refused and records
  the attempt.
- `audit verify --deployment <file>` reports an object with no lock as
  `unlocked` under a profile that demands none, and `INVALID` under one
  that does. The chart's verify job now passes the deployment. Without it,
  nothing is said about locks.
- The exports store and the S3 harness use the same lock-mode option, and
  the harness runs the archive round trip on a locked and an unlocked
  bucket.

The `record` tier of [0003](docs/decisions/0003-s3-object-lock-as-the-record.md)
is unchanged. The `attested` tier -- the chain under a managed key, no lock --
lists its compensating controls in the decision and the
[S3 guide](docs/how-to/prepare-the-bucket.md#the-attested-tier): a managed
signing key required rather than recommended, a shorter digest interval, no
delete permission anywhere, and an administrative no-delete rule where the
store has one.

`s3store.Options` loses `Governance` and `Unlocked` for `Lock`, and gains
`Endpoint` and `PathStyle`; `cli.Archive` and `cli.ExportStore` are replaced
by `cli.ArchiveFlags` and `cli.ExportFlags`.

## v0.2.6

### The writer takes no caller's word for the shape of a record, its own included

The first adopter's registration produced an object keyed under
`year=1970`: the writer's own `audit.catalogue.registered` record, built
by hand, carried no `occurred_at`, and the write path never ran
`record.Check` -- only emitters did. The object is locked under its
profile's retention like any other, outside every digest window, and stays.

The registration record now carries the time it happened, and `Write`
checks every record it takes and dead-letters one that fails, before it
is keyed. A test holds the writer's own record to the check every
emitter's record passes, and a record with no time is dead-lettered with
the reason, never written.

## v0.2.5

### An instrumented emitter no longer drops in silence

`emit.Instrument` always returned a non-nil drop hook -- it counts, then
calls the application's hook if there is one. `emit.New` installs its own
"a drop is never silent" logger only when the hook is nil, so the wrapper
defeated it: every deployment that followed the guide and called
`Instrument` without a hook of its own dropped records with nothing but a
metric to show for it. Found reviewing the first adopter, which wires no
hook and whose runbook promised a log line.

The wrapper now carries the default itself: counted, then told, on
`slog.Default()` when the application wired nothing. A test provokes a drop
through an instrumented emitter with no hook and reads the line back; it
fails on 0.2.4.

### Documentation that still said built things were not built

The deploy guide said `keys.provider: none` was not built and today's
default was `local`; the emit guide and the runbook said
`audit.emit.queue.pending` was not built, on the same page the runbook
relies on it; the configuration reference said `writer.consumers` and the
extension toggles arrive with the rewrite; the architecture page's status
table said the chart's modes and goldens were still to come and the
TypeScript package is on a registry; the read guide said the same; the
presets policy said `history` was not ready. All of it predates 0.2.0.
Corrected against the code. Two chart comments and the audit-page design
note said things the chart does not enforce or the first adopter
contradicts; reworded.

### Documentation, from commissioning the first installation

- The deploy guide told an adopter to vendor the chart at 0.1.0 because there
  was no release. There are releases, and no document named where the chart
  actually lives; it now points at `oci://ghcr.io/truvity/charts` and says
  that one tag stamps the chart and all three images.
- The runbook gains **a scheduled job is not running**. A failing CronJob says
  nothing, and its pods are deleted with the Job, so there is no log by the
  time anybody looks. The tell is a `lastScheduleTime` with no
  `lastSuccessfulTime`, and the way to get the error back is to re-run the job
  from the CronJob and read that pod. Alert on the CronJob, not on the
  archive: an hour with no digest is only visible from the chain, a day late.
- The runbook's **digest chain has a gap** says that catching up works only
  once something has been sealed. With no digest at all a run seals the hour
  that just closed, not every hour since the archive began — right for a new
  installation, and a silent gap if the job was broken over its own first
  runs. What to compare, and how to backfill by range, are spelled out.
- The status table claimed the chart's modes and goldens were still to come,
  and that `@truvity/audit` is published with a release. The first is done;
  the second is not true — it is consumed from a release tag, not a registry.

## v0.2.4

### A put carries the legal-hold header only when it places a hold

The digest job could not write its own digest: `AccessDenied ...
s3:PutObjectLegalHold`. Every put into a locked archive sent the header,
as OFF when there was no hold, and S3 charges the permission for the
header's presence whatever its value. So writing the archive at all
required the right to place a legal hold -- which the digest and verify
jobs, whose policies follow the guide's least privilege, do not have and
should not.

The header is now sent only to place a hold. An absent header and OFF
leave the object in the same state, because a bucket has no default legal
hold the way it has a default retention, so nothing about an object
changes. The guide says so too, with what the refusal looks like for
anyone who meets it on an older version.

## v0.2.3

Two fixes and a correction, all found by running a real installation.

### The scheduled jobs read the archive from the environment

Every CronJob failed, hourly and silently: `audit: name the archive's bucket
with --bucket`. The chart gives each job the archive in `AUDIT_BUCKET`,
`AUDIT_PREFIX` and `AWS_REGION`, which `audit-writer` and `audit-query`
already read as flag defaults and which `audit` did not read at all. So the
jobs ran with no bucket, said an argument was missing, and looked like a
deployment that forgot one rather than a binary that ignored one.

All three flags now default from the environment in every subcommand that
takes them -- `verify`, `replay`, `reindex`, `digest`, `hold` and `key` --
and a flag given explicitly still wins. `cmd/audit` had no tests at all; it
now has one that sets `AUDIT_BUCKET` and asserts that each of those six gets
past the check, verified to fail on all six without the fix.

### The S3 guide names `schema/` among the writer's reads

Documentation only, and the reason a real installation's writer crash-looped
on a 403. The guide's IAM table listed `holds/`, `profile=` and `identity/`
as what the writer reads, and left out `schema/` -- where it records each
profile's composition and from which it reads the last one back on every
start. A policy written from that table lets the writer put the composition
and not get it, so it writes one object, takes an AccessDenied and dies, on a
loop, which reads as a broken archive rather than a missing verb.

## v0.2.2

One fix, found the moment the first installation's writer started.

### A writer that pseudonymises nobody needs no keys

`keys.provider: none` is the chart's default and the shape
[decision 0013](docs/decisions/0013-no-pseudonymisation-keys-by-default.md)
recommends, and the writer refused to start in it: `a key provider is
required`. A leftover unconditional check sat in front of `GuardKeys`, the
guard that decides this properly, so the guard was unreachable and every
deployment without keys crash-looped whatever its profiles did.

The check is gone. Whether a provider is needed is `GuardKeys` and
`GuardHashes`' decision, from what the composed profiles actually ask for:
a deployment that pseudonymises is still refused by name, and one that
keeps everyone in clear now opens. Both call sites that would use a
provider already refused a nil one with their own message, so nothing
downstream changes.

The test that covered this asserted only the refusal, and passed for the
wrong reason -- its profile pseudonymises, so the message it wanted came
from either check. It now says which, and there is a second test for the
deployment that needs no keys at all.

## v0.2.1

One fix, found installing 0.2.0 for the first time: a release with an index
never finished installing.

### The migration hook brings its own service account

`helm install` of a release with an index never completed. The migration Job
is a `pre-install` hook, and it ran as the writer's service account -- which
the chart creates as an ordinary resource, so it does not exist yet when the
hook runs. The Job was admitted and then never got a Pod (`serviceaccount
"audit" not found`), and the install waited for a hook that could not run.

The Job now has a service account of its own, created as a hook one weight
earlier. It is also the right identity: the migration reads a database URL
from a Secret and talks to Postgres, so it has no business holding the
credentials that write the archive. Nothing a deployment sets changes.

Two things were missing that would have caught it. `charts/audit/examples/`
showed the two shapes a deployment actually installs and nothing rendered
them, while the shapes that were rendered are trial installs with no index --
so the migration hook was never in a golden at all. Both examples are now
rendered into `testdata/golden/`, which also proves the documented files
work. And `testdata/hook-order.py` asserts that every hook that runs a Pod
brings its own account, applied at a lower weight: an ordering fault is not a
render error, so only an install finds it otherwise.

## v0.2.0

One installation per application, and the code to match. The documentation was
rewritten first and is the specification the rest of this version was built
against: what each part holds and never holds, a page per deployment shape with
its diagrams, the decisions behind them, and which framework presets a
deployment actually composes.

Nothing outside this repository pins 0.1.x, which is why the shape could change
this much in one version. Adopters pin this one.

### The chart is instantiated per application

`mode` chooses the shape. In `direct` the chart renders one Deployment that
serves the sink and writes the archive. In `stream` it renders two: a receiver
that serves the sink and publishes, holding neither the bucket nor a key, and
`writer.consumers` writers that read the stream and put the objects. Both come
from one template parameterised by role, so the shapes cannot drift apart. The
Service keeps its name and the receiver keeps the `writer` component label in
both, because it is the address records are written to and that should not move
when a deployment changes shape.

New refusals, each for something the binaries reject or quietly get wrong:
`mode` that is neither shape, `mode: stream` without a stream or without a
database, `extensions.billing.enabled` with no metering profile, and
`extensions.quotas.enabled` without a stream. Both extension toggles exist and
render nothing: what fills them is designed and not yet built, and the toggles
are here so a deployment's values do not change when it lands.

The goldens are now `direct.yaml` and `stream.yaml` rather than `minimal` and
`full`, and `charts/audit/examples/` holds the values an application's chart
sets under its `audit:` key, one file per shape, rendered by the chart's own
tests. The NOTES print the four identities that need rights under the
installation's prefix and what each needs, since that is the part a deployer
has to build outside the chart.

**`profiles` defaults to `security` alone.** It defaulted to `security` and
`history`, and because Helm merges maps a values file naming one profile got
the other as well — a surprise in the setting that decides retention.

`examples/embed` is deleted, and the last comments describing a writer inside
an application are gone. `writer.Open` and `query.New` stay exported, because
the binaries are built on them, and say plainly that they are not a way to
deploy.

### The writer gathers from the stream before it writes

Fetching from a stream returns whatever is there, which under a light load is a
handful of records at a time. Writing each fetch straight through made an
object of each, and an archive of many small objects costs a request to put, a
line in every hour's digest and an entry in every listing, forever.

So a writer consuming a stream accumulates across fetches and writes once a
roll condition is reached: `roll.maxRecords` (5000), the roller's byte limit
(8 MiB), or `roll.interval` (30 seconds). Nothing waits on this but the object.
The records are already durable on the stream, and they stay unacknowledged
until the put, so a writer that dies mid-window leaves them for the next one.

`stream.ackWait` must now exceed `roll.interval` plus the longest a put can
take, and both the chart and the consumer refuse otherwise: a stream that gives
up waiting sooner offers the same records to a second writer, and the day's
objects quietly double. The default rises to two minutes.

Direct mode is unchanged. There is no stream to gather from, every batch is put
before it is acknowledged, and the emitter's own batch size and flush interval
are what decide object count there.

### A keyless deployment is refused a catalogue that hashes

A property a schema annotates for hashing is a pseudonymised property, and it
needs the same keys an identifier does. Until now a deployment running without
a key provider took such a record, failed to hash it and dead-lettered it, one
record at a time, which is something a deployment discovers on the day it
matters rather than the day it was configured.

The writer refuses to start when the catalogues it holds ask for hashing and
no provider is configured, naming the property. The receiver refuses a
registration that arrives later with the same problem, as a validation
problem, so the application does not start against an installation that would
dead-letter its records. `audit validate` lists the hashed properties, so the
application's own CI says it first.

### The tail is asked the case it exists for

The conformance suite indexes a record that happened on an earlier day than
anything already there, and was recorded after the last page was taken, and
requires the tail cursor to deliver it. That is what a tail is for: what
arrives next need not have happened next, and a searcher that ordered the tail
by when things happened would hand a reader a cursor already past the record,
with an empty page and no sign of the gap. Memory and Postgres answer it; the
archive scan refuses `recorded_at` ordering and is held to the refusal.

`indextest.Run` takes the indexer as an explicit argument now rather than
type-asserting the searcher. The read-only Postgres role is an `index.Indexer`
by type and cannot write, so the assertion asked it to index and the suite
failed where nothing was wrong.

Written down with it: a deployment on `query.searcher: s3scan` can search the
trail but cannot follow it, because the archive is laid out by the day things
happened.

### A record the emitter gives up is a log line

Decision 0012 said every dropped record is still a log line. The emitter never
logged anything: it called `OnDropped`, and an application that wired no hook
lost the record silently. A drop with no hook is now written to the
application's log by the emitter itself, with the identifier, action and
reason, through `Options.Logger`.

Decision 0013 described a start-up refusal keyed on the registered catalogues.
What was built refuses on the composed profile instead, because a catalogue can
be registered after start-up and a check on what is registered would be walked
around by arriving late. The record now says so.

### A receiver mode, so stream mode has a front door

`audit-writer --mode receiver` (env `AUDIT_MODE`) serves the sink and
publishes to JetStream, and holds no bucket and no key provider: it refuses
`--bucket` and a key provider rather than quietly being a writer. `--mode
writer` stays the default and is what every installation ran until now.

Until this, nothing published to a stream but a test. An installation that
wanted one had to let the **application** publish, which meant the
application holding the stream's credentials — the thing
[0011](docs/decisions/0011-one-installation-per-service-or-product.md) exists
to prevent.

The receiver stamps each record with the caller its authenticator verified,
and with the moment it took responsibility, before publishing. It has to: a
writer consuming a stream is reading messages, not serving a request, so it
has no caller to verify, and an identity not attached at the front door is one
nothing downstream can recover. A writer told it consumes its own
installation's stream (`writer.Config.FromStream`) therefore keeps a stamp
whose origin hash still describes its record, and stamps afresh one that does
not. On the sink's own port it always stamps, because there a caller that
could keep its own stamp would be choosing the identity it is recorded under.

### Keys are off by default

`keys.provider: none` is the default. Most deployments want it: staff are kept
in clear because that is what accountability is for, and people outside arrive
as identifiers an application already minted, which name nobody without that
application's own database. Encrypting one of those a second time adds a key
to lose and tells a reader of the archive nothing new. `local` and OpenBAO
`transit` stay for a deployment obliged to be able to crypto-shred.

A deployment without keys has to say which it is. New
`external_identifiers_are_opaque` in the deployment document relaxes a
profile's `external: pseudonym` to `clear` — applied where profiles are
composed, so `audit profile explain` shows the treatment that will actually be
used, and says it was relaxed. The writer then holds the deployment to it and
refuses a record whose external identifier looks direct, an address say. A
writer that has neither a provider nor the declaration **refuses to start**,
naming the profile: arriving at clear identifiers in an archive nothing can
edit should take a decision, not an omission.

The `history` preset no longer needs keys either. It omits internal actors
instead of pseudonymising them, so a tenant's administrator sees what was done
and by what kind of person, never by whom — which is what that view should
show anyway. Composed with `security`, the stricter reading still wins.

### Two deliveries, and no file outbox

An action declares `block` or `async`. `block` is unchanged: the call returns
when the receiver has acknowledged durability, and the action fails when it
cannot. `async` is the default, and now keeps what it is given: a bounded
in-memory queue, retried with backoff until the sink acknowledges the batch. A
batch the sink *answers* is never retried — a refusal is recorded where it
happened and repeating it would only repeat the refusal — and a queue that
overflows drops its **oldest** record, counts it and reports it, on the
reasoning that the newest is the one somebody can still act on.

`outbox` and `best_effort` are retired, and refused by name where a catalogue
is loaded, with the replacement in the message. `emit.FileOutbox` and the file
itself are gone, with `Options.Outbox`, `Options.Publish` and the volume that
carried them. New `Options.Retry` paces the retries.
`audit.emit.queue.pending` replaces `audit.emit.outbox.pending`: it counts
everything not yet acknowledged, which is exactly what a process would lose if
it stopped now.

On the wire, `DELIVERY_ASYNC` joins the enum. `DELIVERY_OUTBOX` and
`DELIVERY_BEST_EFFORT` stay in it, deprecated: this package is `v1`, a value
removed is a record nobody can read, and nothing produces them any more.

Two statements are now tested by killing a process outright: a record the
application was told was kept survives, and what was still queued is what is
lost.

A record the queue gives up is written to the application's log by the
emitter itself, with its identifier, action and the reason, when the
application wires no `OnDropped` hook (`Options.Logger`, default
`slog.Default()`). Until now "every dropped record is still a log line" was a
promise the emitter made on the application's behalf.

**Corrected while doing it.** The documentation said an `async` batch was
acknowledged after "the roll that holds it" and that `roll.interval` was the
loss window. It never was: the receiver puts every batch it takes before it
answers, whatever the delivery. The loss window is one flush interval of
records plus the batch in flight, and the pages now say so.

### The registry service is gone

The writer serves `RegisterCatalogue` beside the sink, so an installation is
one Deployment smaller and an application registers its catalogue with the
same address it writes to. `cmd/audit-registry`, its image, the chart's
`registry.*` values, its Deployment, Service, ServiceAccount and network
policy are all removed, and a release now carries three images instead of
four. Nothing about registration itself changed: the same validation, the
same Postgres store, the same copy into the archive, the same coverage report
that warns and never refuses.

Whose catalogue a registration is still comes from the caller's verified
service account and never from the document, and `workloadIdentity.workloads`
is still the only thing that says so: one entry per workload that may
register, naming the source it speaks for. An installation that keeps an index
and verifies callers must fill it in, and the chart now refuses to render when
it is empty rather than letting every registration be refused at run time.

- **Three decisions.**
  [0011](docs/decisions/0011-one-installation-per-service-or-product.md): one
  installation per service or product, in that application's namespace,
  rendered by its own chart, with the receiver serving `RegisterCatalogue`
  and no registry service.
  [0012](docs/decisions/0012-two-deliveries-and-a-durable-ack.md): two
  deliveries, `block` and `async`, the file outbox removed, and the
  receiver's acknowledgement always meaning durable.
  [0013](docs/decisions/0013-no-pseudonymisation-keys-by-default.md):
  `keys.provider: none` by default, with `external_identifiers_are_opaque`
  declared by the deployment. 0004's delivery modes and 0010's preference
  for a managed provider are superseded.
- **A deployment page per shape**, each with a deployment diagram and the
  sequence of one record: [direct](docs/explanation/direct-mode.md) for an
  internal service, [stream](docs/explanation/stream-mode.md) for a product, and
  the two extensions, [billing](docs/how-to/enable-billing.md) and
  [usage quotas](docs/how-to/enable-usage-quotas.md), with the five slots
  each. There is no embedded shape: a writer inside the application is no
  longer a way to deploy this, and `docs/guides/embed.md` is gone.
- **The README is a map**: what it is, the two shapes, the two extensions,
  a row per kind of reader, and the status table.
- **A presets policy**
  ([docs/operations/presets-policy.md](docs/explanation/which-profiles-to-compose.md)):
  compose `security` always, `billing-nl` where the installation meters,
  and leave `dora`, `pci-dss`, `evidence-etsi` and `nen-7513` as files until
  a contract asks. `history` is reworked before anyone composes it.
- **`docs/research/` is removed** from the tree: it read as design and was
  not. The decisions that used it quote what they needed, and the surveys
  remain in the repository's history. `docs/design/pipeline.md` is folded
  into the architecture page, `docs/design/extension-points.md` moves to
  `docs/reference/`, and `docs/design/viewer.md` becomes
  `docs/design/audit-page.md` with the standalone console dropped.

## v0.1.1

The images publish where the chart looks for them. 0.1.0's release
failed: the four ko images carried a `repositories:` list each, and the
release workflow's `KO_DOCKER_REPO` wins over it, so ko tried to publish
`ghcr.io/truvity` itself and the registry answered 400. They now take
their name from the command's import path under one repository path, as
access-roster's two images do, and the chart's defaults name the same
four: `ghcr.io/truvity/audit/{audit,audit-writer,audit-query,audit-registry}`.

## v0.1.0

The foundation, released so that consumers have something to pin. Every
contract, binary and chart below is at its first published version, and
nothing outside this repository depends on it yet — which is the point of
cutting it now rather than later: a version that exists can be adopted a
piece at a time.

- Contracts in `proto/audit/v1/`: record, sink, registry, query. Generated
  Go and TypeScript committed under `gen/` and `ts/src/gen`.
- `record`: the canonical form (RFC 8785 over the protobuf JSON mapping, no
  floating point), identifiers, bounds with a published truncation order,
  and the negative list.
- `preset`: the seven framework presets, loaded and composed into profiles
  with default-deny field lists.
- `catalogue`: catalogue loading, extension-schema annotations, composed
  validation of a record, message-template argument checks, and the category
  cross-check against a deployment's profiles.
- `emit`: the emitter, with block, outbox and best-effort delivery, request
  provenance middleware and a durable file outbox.
- `sink`: the write contract with memory, Connect and JetStream transports.
- `keys`: pseudonyms per tenant and purpose, never rotated; a local signer.
- `store`: the object store interface, the S3 bucket, and a memory store for
  tests that can be tampered with on purpose.
- `internal/writer`: the split into per-profile copies, rolling into locked
  objects, deduplication, the dead letter, and the writer's own account of
  itself, emitted into itself over the in-process sink.
- `internal/digest`: the signed digest chain and its verifier.
- `index`: the index contract and the rows it holds, the facet deltas a record
  produces, and an in-memory implementation. Indexing is idempotent by
  `(profile, id)` and counting has no call of its own, because only the
  transaction that inserted a row can tell a re-delivery from a new record.
- Export: `audit.export.requested` before anything is read, `audit.export.completed`
  with the count and the form. An export is a copy of records made to be taken
  away, so it lives outside every profile's prefix, expires, carries that expiry
  as the file's own retention, and is collected only by whoever asked for it.
  `store.Presigner` is an optional capability rather than part of `store.Store`:
  most of what an archive holds must not be reachable by a URL anybody can hold.
- `audit-query`, the read service behind Connect, over the Postgres index or
  the object-storage scan. Cursors are opaque and bound to the question they
  came from — narrowing included — so one replayed against a different filter
  or a wider grant is refused rather than resumed from an ordering that no
  longer exists. A denial reaches the client as a denial and a bad cursor as a
  bad argument, because a client told "server fault" retries forever.
- `internal/query`: the read service. It compiles a closed request, narrows it
  to the caller's grant as one more filter term, asks a searcher, and records
  the read — `audit.search`, `audit.facets`, `audit.get`, naming the caller and
  the rule that allowed them. A refused read is recorded too, and a record the
  grant does not cover is reported as absent rather than as forbidden, because
  the two are the same answer to someone who should not know it exists.
- `auth`: the `Authenticator` and `Authorizer` seams a deployment plugs into,
  with a declarative authorizer. A grant's zero value grants nothing, every
  tenant is said out loud rather than meant by a nil list, a refusal names
  which of profile, operation or tenant it failed, and `resolve` — undoing a
  pseudonym — is never implied by permission to read.
- `index/s3scan`: a searcher with no index at all, for a deployment too small to
  run a database — and the implementation that cannot cheat, since one backed by
  a table can quietly grow a capability the interface never promised. It refuses
  facets and every ordering but occurred time, with the reason, rather than
  answering something narrower than was asked. A scan is bounded by a budget and
  by a horizon.
- `index`: a memory `Searcher` beside the Postgres one, asked the same
  questions — one implementation is a description of its own habits with an
  interface drawn around it.
- `index`: the `Searcher` contract — a closed query, keyset cursors, facets,
  provenance on a single record — and the Postgres implementation of it. The
  grant is one more term in the query rather than a layer above it, so there is
  no path to a row outside it. Paging is keyset, so a deep page costs what a
  shallow one does and a record appended meanwhile cannot shift a page already
  handed out. The last page still carries its boundary, because a tail keeps
  polling it.
- `index/postgres`: the default index and the shared deduplication table, with
  a checked-in schema, monthly partitions created on demand, and row-level
  security by tenant.
- Deduplication asks before the write and marks after it, so that a crash
  between the two costs a duplicate object rather than a lost record.
- `audit validate`, `audit profile explain`, `audit check-emitters`,
  `audit verify`, `audit replay`, `audit migrate`, `audit reindex`,
  `audit digest`, `audit purge`, `audit clock-sync`, `audit hold`,
  `audit key destroy`.
- `audit key destroy` is erasure: the copies stay and their pseudonyms can
  never be recomputed. It refuses while a legal hold covers the tenant, and
  refuses without a writer, because an erasure the trail does not record is one
  nobody can prove was lawful.
- Chart: the query service (`query.enabled`), with its own database role —
  refused when it names the writer's credentials, since an owner bypasses the
  tenant policies — the grants file, exports, and resolve through the
  writer's key directory or its own transit token. The registry and the query
  service get their own service accounts. The writer's pods now carry
  `app.kubernetes.io/component: writer`: its Service selected on the release's
  labels alone and so also routed sink traffic to the registry's pods. That
  selector is immutable, so an install from an earlier commit deletes the
  writer Deployment before upgrading.
- `keys.Transit`: pseudonymisation keys in an OpenBAO (or Vault) transit
  engine, one key per purpose and tenant named `<prefix>.<purpose>.<tenant>`
  so the engine's policy scopes each role to its purposes. A pseudonym is the
  engine's HMAC and a sealed identifier its encryption, both pinned to the
  key's first version, so the key never leaves the engine and every replica
  agrees without a shared directory. Destroy trims the first version and
  leaves the key as its own erasure marker. It and the transit digest signer
  sign in with the pod's projected service-account token on a JWT auth mount
  (`keys.JWTLogin`), signing in again as the lease runs out; they address an
  OpenBAO namespace and trust a private chain's bundle. `--key-provider
  local|transit` and the shared `--transit-*` flags on the writer, the query
  service, `audit key destroy` and `audit digest`; the chart's `openbao`,
  `keys.provider: transit`, a role per component, and `trust.configMap` for
  OpenBAO and Postgres alike.
- Template arguments use underscores: `{targets_0_id}`, `{data_items}`,
  `{actor_id}`. Dotted names (`{targets.0.id}`) are not valid ICU, so no
  standard renderer could fill them; the validator now refuses them with the
  underscore spelling, and refuses data properties that would collide as
  arguments. The viewer renders templates as written.
- `audit conformance --query <url> --profile <p>`: holds a running query
  service to the search contract from outside — paging, order, get against
  search, filters, refusals, and optionally digest coverage — over the records
  it already holds, reading only. `just conformance` runs the whole suite with
  Postgres, LocalStack and an OpenBAO dev server.
- A record that is not there is `not_found` from every searcher
  (`index.ErrNotFound`, now in the searchers' conformance suite). Before, the
  searchers returned a plain error, which the service reports as `unavailable`
  — telling a client to retry for a record that does not exist. Found by the
  first conformance run. `id` predicates take whole UUIDs; the Postgres index
  failed on anything else.
- `@truvity/audit` (built from `ts/`):
  - the query client and the typed contract;
  - the qualifier box compiled to the typed filter;
  - records rendered as their catalogues' sentences, through FormatJS;
  - `@truvity/audit/react`: `AuditProvider`, `useSearch`, `useTail`,
    `useFacets`, `useRecord` and a default MUI `AuditView` for an
    application's console.

  Catalogue templates name arguments by path (`{targets.0.id}`), which ICU
  refuses as written. The renderer finds them with a port of the validator's
  scanner, and both are held to `testdata/messages.json`. `audit messages`
  prints a catalogue's sentences as JSON for the viewer. The generated
  TypeScript now imports with `.js`, which Node's ESM resolution needs.
- `writer` and `query`: the writer and the query service as public libraries,
  so an application can embed its own trail — its emitter writing into the
  writer in process, its console reading through the query API behind its own
  sign-in (`auth.AuthenticatorFunc`). Both binaries are built on them. The
  deployment document is `preset.ParseDeployment`. `examples/embed` does the
  whole round trip with public imports only, and a test holds it to that.
- Retention addenda: an action that `extends` the records a data property
  names lengthens the lock on the objects holding them — a renewal on the
  issuance, a credential on the identity proofing it relied on — to its own
  expiry plus the profile's years, after it is durable and never shorter.
  `store.Store.ExtendRetention` (S3 `PutObjectRetention`; the memory store
  refuses a shorter date as a compliance bucket does). Each extension and each
  failure is an `audit.retention.extended` record; a failure never fails the
  batch and is counted as `audit.writer.retention.not_extended`.
- Legal holds: `audit hold place|release|list`, hold records in the archive
  under the same lock and append-only like everything else, and the writer
  setting the hold on objects written under a held prefix — an object held only
  by a later sweep was deletable in between.
- The digest and verify jobs keep an account of themselves, as the writer
  does: `audit.digest.written` per sealed window, `audit.digest.verified` and
  `audit.digest.failed` per window checked. A chain never sealed and one sealed
  over a quiet hour are otherwise identical in the archive, and a verification
  that never ran looks exactly like one that found nothing wrong.
- An uncovered object now names the window that should have covered it, so a
  failure points at an hour rather than at the whole profile.
- `internal/clock`: an SNTP client with no dependencies, so that the daily
  check ETSI EN 319 401 §7.10 asks for is recorded as an audit event rather
  than assumed. It measures and records; it never sets the clock.
- A digest covers every tenant of its profile. The chain is keyed by profile
  and the archive puts the tenant between the profile and the date, so a
  builder given the profile prefix covered nothing and one given a tenant's
  prefix covered one tenant. `store.Store` gained `Prefixes` to ask which
  tenants exist without walking every object.
- `s3store.List` pages to the end when asked for everything. It took S3's
  first thousand keys for the whole, which the verifier, the reindex, the
  replay and the digest's own chain-linking all relied on; every walk of the
  archive now goes tenant by tenant and day by day through `store.WalkDays`,
  and the S3 test double pages, sorts and groups as S3 does so that the next
  listing that stops early is caught here.
- `internal/s3test`: the archive walks run against a real S3 in CI, which is
  what would have caught the two bugs a memory store hid — a digest covering one
  tenant, and a listing stopping at the first thousand keys. It takes an
  endpoint rather than a product, so which S3 answers it is a variable. The
  image is pinned by digest to the community line: LocalStack's `latest` and
  `stable` now resolve to a licensed build that exits without a token, which in
  a public repository would fail every fork's CI.
- CI, as the estate's other public repositories have it: each `just` recipe is
  its own job, with the race detector, the TypeScript drift check and a
  Postgres-backed run as jobs of their own, and a `leak-canary` recipe that
  enforces mechanically what a public repository may not contain.
- `audit-registry`, the catalogue registry as a service: it validates a
  document with the same toolchain that validates it in the application's own
  tests, refuses a source registering another's catalogue, and refuses a
  catalogue that would leave a profile's required categories uncovered.
  Registering the same version twice is how a deployment rolls; registering a
  different document under the same version is refused, because a version says
  what records already written under it mean.
- `emit.Register` for an application to register at start-up and not start if
  the deployment refuses its catalogue.
- `audit-writer`, the writer as a service behind Connect and, given
  `--stream-url`, behind a durable JetStream consumer shared by every replica.
  A batch is acknowledged only once its records are in the archive, so a writer
  that cannot write leaves them for the redelivery; `MaxDeliver` is unlimited,
  because a record must not fall out of the stream for having been offered a
  few times, and nothing loops forever on a bad record — one the writer cannot
  process is accepted and dead-lettered.
- `charts/audit`, write side: the writer, the catalogue registry, a pre-upgrade
  hook applying the index schema, and the digest, verify, purge and clock-sync
  jobs. It refuses to
  render twelve configurations the binaries reject at start-up or accept and
  get quietly wrong — several replicas without a shared deduplication table,
  several replicas sharing a key directory none of them can both write,
  a disposable key directory, governance mode.
- Decisions 0001 to 0009 accepted.
- Design, research, reference and operations documents.
