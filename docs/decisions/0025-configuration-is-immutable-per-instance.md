# 0025. Configuration is immutable per instance; credentials and State are read live

- Status: accepted; refines [0021](0021-one-validated-configuration-file.md) and [0009](0009-versioning-policy.md) for configuration
- Date: 2026-10-05

Decided together with truvity/sluis ADR 0036, which carries the same rule for
that service; the two are written to be read side by side.

## Context

An audit installation is one main configuration plus the documents it points
at: `deployment`, `grants`, `workloads` and the catalogues. Until now nothing
said how those reach a running instance or when an instance notices a change.
On Kubernetes they are ConfigMaps; on AWS Lambda the question is sharper,
because the function's code is the released artefact and the writer's ingest is
an SQS queue whose failures drain to a dead-letter queue. A configuration that
is fetched at start, or changes under a running instance, is a new way for the
record path to fail or for two instances to disagree about a catalogue.

The truvity/policy contract (service.md section 1, config.md section 2) says
configuration is a file, its path from an argument or one variable, and that
schemas are authored with tests binding the Go structs.

## Decision

1. **Configuration is immutable per instance; a change is a new set of
   instances.**
   - Kubernetes: `checksum/*` annotations on the pod template roll the
     Deployment when a document changes. ConfigMaps are mounted as
     directories, never through `subPath`.
   - AWS Lambda: the documents ship as an immutable **Lambda layer** at
     `/opt/audit/`. The function code is the **released zip, byte-identical and
     sha256-verified**. A change is a new layer version, then a function update;
     AWS replaces all instances at once. No aliases and no canary (`$LATEST`).
   - Server: a restart.
2. **Credentials and State are read live**: signing and encryption key files,
   client secrets, projected tokens. These are not part of the configuration
   and rotating one is not a deployment.
3. **Configuration is a file everywhere**, its path from an argument or one
   variable, `AUDIT_CONFIG`. The documents are the main configuration and its
   `deployment`, `grants`, `workloads` and catalogue sub-documents, each read
   whole; layering happens only when a document is rendered, never in the
   running binary.
4. **Versioning.** Each document carries `apiVersion`; absent means `v1`. A
   binary accepts N and N-1 and converts N-1 on load. Schemas stay authored by
   hand, with tests that bind them to the structs.
5. **Secrets.** A declared secret name is delivered by an environment
   variable, a mounted file, or a declared secret source (the amendment to
   contract section 5), by the same rule as in sluis.

## Consequences

- **A pre-deploy catalogue/version guard is added.** A new instance set must
  not start against a catalogue or record version that the existing records and
  decoders do not cover (0009: decoders forever); the guard runs before the
  layer is applied and fails the deploy, not the ingest.
- A bad configuration is caught at the start of an instance, in front of the
  queue, instead of surfacing as messages draining to the DLQ.
- On Lambda a configuration change remains a manual Pulumi apply, as in sluis.
- The previous layer version is the rollback.
- The N-1 converters are a cost carried for as long as the window exists.

## Alternatives considered

**Configuration baked into the zip.** Rejected: build, release and run are no
longer separate, and the deployed code is not the release.

**S3 documents pinned by `versionId`.** Rejected: a fetch on cold start, extra
IAM, and a new init failure. For audit that failure sends the SQS ingest to the
DLQ, which is the worst place to find a configuration fault.

**AWS AppConfig.** Not rejected on merit: staged rollout with alarm rollback is
real value. It is an extension layer, about six resources per estate, and
AWS-only. Deferred; it can be added later.

**Environment-only configuration via caarlos0/env.** Rejected: Lambda's 4 KB
environment cap, lists turned into `_0_` variables, and a conflict with
contract section 1.

**Policy and catalogues in SSM.** Rejected: 4 KB / 8 KB per parameter, and a
tree of parameters is not read atomically.

**Lambda aliases with CodeDeploy canary.** Deferred; the owner chose all at
once.

**Runtime reload of configuration.** Rejected: a window in which instances
disagree, and more code. Replacing instances is cheap because State is
external.
