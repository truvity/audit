# Configuration reference

The configuration file of each binary: its shape, secrets, the shared blocks and what is
refused. The per-binary keys are in [the writer](configuration-writer.md),
[observe and query](configuration-observe-query.md) and [the jobs](configuration-jobs.md); the
Go emitter's options in [the emitter library](emitter-library.md); the chart's values in
[chart values](chart-values.md). Every name here exists in the code, in
`schemas/config/` or in `charts/audit/values.yaml`, and the chart's file has a
comment on each value. Where something is designed and not built,
it says so.

One installation serves one application, in that application's namespace,
rendered by the application's own chart with this one as a dependency
([0011](../decisions/0011-one-installation-per-service-or-product.md)).
There is no registry service and nothing configures one.


## The configuration file

`audit-writer`, `audit-observe` and `audit-query` take one flag, `--config <file>` (and
`--version`, `--help`). `audit verify`, `audit purge`,
`audit clock-sync` and `audit migrate` take `--config <file>` in place of every
other flag of the command; only `--json`, which changes how the report is
printed, may accompany it. The interactive flags of `audit` stay for a person
at a keyboard, and a command line that names both a file and another flag is
refused. Nothing else configures a process: there are no flags with an
environment fallback
([0021](../decisions/0021-one-validated-configuration-file.md)).

The one variable that is not a fallback is **`AUDIT_CONFIG`**, which says
*where the file is* and is read by all six binaries (`audit-writer`,
`audit-query`, `audit-observe`, `audit-notary` and the two Lambdas) and by the
four jobs of `audit`. `--config` wins when both are given. For a job of `audit`,
the variable counts only on a command line with no option of its own, so that a
person who has it exported and runs `audit verify --deployment ...` is not told to
put that in a file they never named. The Lambda binaries look at `/opt/audit/audit.yaml`
(where a layer mounts it) and then at `/var/task/audit.yaml` (the function's own
package, where the previous release put it; kept for one release after the file
moves to the layer), when neither the flag nor the variable is set.

Each binary's file is YAML, and is validated against that binary's JSON Schema
before anything starts. The schemas are in `schemas/config/`, one per binary
or command, and ship in the release:

| binary or command | schema |
|---|---|
| `audit-writer` | `audit-writer.schema.json` |
| `audit-observe` | `audit-observe.schema.json` |
| `audit-query` | `audit-query.schema.json` |
| `audit verify`, `purge`, `clock-sync`, `migrate` | `audit-verify.schema.json`, `audit-purge.schema.json`, `audit-clock-sync.schema.json`, `audit-migrate.schema.json` |

Every file carries `apiVersion: audit.truvity.github.io/<kind>/v2` (`<kind>` is
the schema's name: `audit-writer`, `audit-query`, `audit-writer-lambda`, and so
on). The group is `<product>.truvity.github.io`. A binary reads version N and
N-1 (ADR 0025): **version 1** is deprecated, is converted on load and logs a
warning, and is read for one more minor; how to move off it is in
[the v0.13 upgrade](../how-to/upgrade/v0.13.md). Another version or another kind's is refused, so that a later shape
arrives by a version and not by a file that quietly means something else. The
version-2 schemas are `schemas/config/<kind>.schema.json`
(`$id` `https://truvity.github.io/audit/schemas/v2/config/<kind>.schema.json`);
version 1's are kept, frozen, in `schemas/config/v1/`. The `audit-deployment`
document moved to the new group and nothing else.

Everything on these pages, and the chart's examples, is version 2.

An unknown key, a missing required key or a value of the wrong type is a
start-up error that names the path to it. A few rules a schema cannot say run
after it has accepted the file; they are listed under
[Refusals](#refusals). The chart validates the same files when it renders, and
`just config-schemas` regenerates the schemas from
`internal/config/schema/schema.go`.

Durations are strings in Go's notation: `30s`, `2m`, `168h`.

### Secrets

A secret is never in the file. A field that holds one is named `...Secret` and
holds the **name** of the secret, which the file's one `secrets` block says how
to find. The process reads exactly the secrets the file names, and one that is
absent or empty is an error naming the field and where it looked (the source and
the root), never the name it holds or a value. These are all of them:

| key | holds |
|---|---|
| `database.passwordSecret` | the Postgres password |
| `bucket.credentialsSecret.accessKeyID`, `.secretAccessKey` | the static credentials of an S3-compatible store |
| `openbao.tokenSecret` | an OpenBAO token |

```yaml
secrets:
  source: file                      # env (the default), file or ssm
  root: /etc/audit/secrets          # a directory (file) or a parameter path (ssm); not with env
```

| `source` | a name is | `root` |
|---|---|---|
| `env` | the name of an environment variable | refused |
| `file` | a path under `root`, one file per secret, read as it is (one trailing newline is dropped): a mounted Kubernetes Secret | an absolute directory, required |
| `ssm` | a SecureString parameter `<root>/<name>` in AWS Systems Manager Parameter Store, read decrypted with the process's own identity | a path starting with `/` and not ending in one, required |

A name for `file` and `ssm` is relative and cannot leave the root: segments of
letters, digits, `.`, `_` and `-`, no `..`. **On AWS Lambda use `ssm`: the
function's environment is not a place for a secret, so `source: env` is refused there
(when `AWS_LAMBDA_FUNCTION_NAME` is set), which includes a version-1 `...Env` that the loader converted
to it.** A `root` has no empty, `.` or `..` segment. The Pulumi library renders
the block and grants the function `ssm:GetParameter(s)` on its root and nothing
else of SSM ([AWS](../how-to/aws-store-secrets-in-ssm.md)). On Kubernetes use `file`: the
chart's `secretFiles` projects each Secret key as the file the name stands for.

A password inside a database URL is refused. Token, key and root **files** are
referenced by path, not by name: `tokenFile`, `jwtFile`, `rootFile`,
`keyFile.path`, `caFile`. On a platform the file is a mounted
Secret or a projected token.

### Telemetry

Telemetry is the OpenTelemetry SDK's own environment, and the file has nothing
about it: `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME` and the rest of
`OTEL_*`. The platform decides where signals go, so the same file runs in
every environment. The chart's `telemetry.otlp` value renders those variables
on every pod ([telemetry](telemetry.md#the-chart-sets-the-environment));
on AWS Lambda they are the function's environment
([AWS](../how-to/aws-send-lambda-telemetry.md)).

### Health: `/healthz` and `/readyz`

`audit-writer`, `audit-query` and `audit-observe` serve both on their `listen` address.
`/healthz` says the process is up and, for a writer with a stream or queue consumer,
that the consumer has not stopped on its own: it is the liveness probe, and a failure
restarts the pod. `/readyz` says it can do its work now: the database answers (writer
with a `database`, query with the `postgres` searcher, observe), the catalogue
registry can be read (writer with a `database`), the archive can be listed (query with
the `s3scan` searcher, observe's catalogues), and a consumer is running. It is the
readiness probe: a failure takes the pod out of the Service and does not restart it,
because restarting does not bring a database back. The checks run at every probe, in
parallel and within 2 seconds, and a 503 names the checks that failed and never their
errors (those are in the log). A receiver with no database or consumer is ready when
it is up.

### Evidence: the writer's start-up record

`audit.writer.started` carries, as `data` (schema `writer-started.json`, in
common catalogue 2.1.0), what the writer was configured with: `config_file` and
`config_digest` (`sha256:` and the SHA-256 of the file's bytes, which is what
`sha256sum` prints), the digests of the documents it names (`deployment_digest`,
`workloads_digest`, and `catalogues_digest`, which is the digest of each file's
relative name and digest in name order), and on Lambda `layer`
(the layer version ARN the deployment declares in `AUDIT_CONFIG_LAYER`) and
`function_version` (`AWS_LAMBDA_FUNCTION_VERSION`). A field that is not known is
left out. The file is read twice, before and after it is validated, and a file
that changed in between is refused, so that the digest names the bytes that ran.

### Documents the file points at

Four things a file names by path are separate documents, each with its own
contract, and not part of the file. Three of them have a JSON Schema in
`schemas/config/` (`audit-deployment.schema.json`, `audit-grants.schema.json`,
`audit-workloads.schema.json`) and carry `apiVersion` as the file does (version 2 under `audit.truvity.github.io`, version 1 deprecated); the code
that reads them validates against it and then decodes strictly:

| key | document | in the chart |
|---|---|---|
| `deployment` | the profile configuration: which framework profiles each profile is composed from, and `externalIdentifiersAreOpaque` | `/etc/audit/deployment.yaml`, from `profiles` and `externalIdentifiersAreOpaque` |
| `workloads` | the issuers trusted to name a workload, and which service account speaks for which source ([Workload identity](configuration-writer.md#workload-identity)) | `/etc/audit/workloads.yaml`, from `workloadIdentity` |
| `grants` | the query service's issuers, framework profiles and rules ([Query service](configuration-observe-query.md#query-service)) | `/etc/audit/grants.yaml`, from `query.grants` |
| `catalogues` | a directory of catalogue documents registered at start-up | `/etc/audit/catalogues`, from `catalogues` |

## Shared blocks

These blocks recur under several keys. They follow the shared fragments of the
component contract (`postgres.json`, `bucket.json`, `listen.json`,
`nats.json`).

**`database`** (a PostgreSQL connection)

| key | type | default | meaning |
|---|---|---|---|
| `url` | string, required | | a `postgres://` or `postgresql://` URL without a password. A URL with one is refused, and one that does not parse is refused |
| `passwordSecret` | string | none: the connection needs no password | secret by reference: the name of the secret holding the password |
| `maxConnections` | integer, at least 1 | 10 | the pool size of this process. Size it against the server's limit divided by the number of processes |

**`bucket`** (an object store addressed by the S3 API)

| key | type | default | meaning |
|---|---|---|---|
| `name` | string, required | | the bucket, which exists already |
| `region` | string | the SDK's own resolution | the region the bucket is in |
| `endpoint` | URI | the SDK's own resolution for the region | override the API endpoint, for a store that is not AWS |
| `ca` | path | the system trust store | a CA bundle trusted for that endpoint, mounted by the platform |
| `pathStyle` | boolean | false | address the bucket as a path rather than a host, for a certificate that does not cover a bucket subdomain |
| `credentialsSecret.accessKeyID`, `.secretAccessKey` | strings, both required if the block is present | none: the SDK's ambient credentials, which is what a workload identity provides | secret by reference: the names of the secrets holding static credentials |

**`archive`** (where the archive is, and how it is written)

| key | type | default | meaning |
|---|---|---|---|
| `bucket` | `bucket`, required | | the archive's bucket |
| `prefix` | string | none | the prefix within the bucket. Required in a bucket shared with other installations: it is what keeps two apart |
| `lockMode` | `compliance`, `governance` or `none` | `compliance` | the Object Lock mode every object is written in; `none` is for a store without Object Lock or profiles that demand none ([0014](../decisions/0014-lock-modes-and-store-tiers.md)). A process refuses to start when a profile demands a stricter mode. `audit-query` and `audit verify` read; `audit-query` has no `lockMode` |
| `kmsKey` | string | the bucket's default encryption | the key objects are encrypted with. Only `audit-writer` has it |

**`listen`**: `address` (string, `host:port` such as `:8080`, required when the
block is present). Both servers default it to `:8080`.

**`sink`** (the writer a process records through)

| key | type | default | meaning |
|---|---|---|---|
| `url` | string | | the writer's base URL. Exactly one of `url` and `sqs` |
| `sqs` | `queueUrl` (required), `region`, `fifo` | | the ingest queue of a writer that runs elsewhere, such as the writer Lambda: records are sent to the queue and acknowledged `queued`. There is no credential in the file: the pod's own identity is it (EKS Pod Identity, or IRSA through the service account's annotation), and its role needs `sqs:SendMessage` on the queue. `tokenFile` is refused with it, and `expect` is `queued` and may be left out |
| `tokenFile` | path | none: no token, which only an anonymous trial install accepts | the file holding the bearer token, read afresh on every request; in a cluster the pod's projected service-account token |
| `expect` | `logged`, `queued` or `archived` | none: the client claims nothing | what the writer at `url` is configured to give. A client cannot learn that until it writes, so the file says (`sink.Client.Expecting`); the process's `require` is checked against it at start-up and against every acknowledgement afterwards |

**`openbao`** (how a process reaches an OpenBAO transit engine)

| key | type | default | meaning |
|---|---|---|---|
| `address` | string, required | | the server, for example `https://openbao.example.com:8200` |
| `mount` | string | `transit` | where the transit engine is mounted |
| `namespace` | string | the root namespace | the namespace the engine and the auth mount are in |
| `caFile` | path | the system roots | a PEM bundle trusted beside them |
| `login.mount`, `.role`, `.jwtFile` | strings, all required | | sign in with a JWT: the auth mount, the role on it, and the file holding the pod's projected token, read at every login. Nothing is stored |
| `tokenFile` | path | | sign in with a token read from a file on every call |
| `tokenSecret` | string | | secret by reference: the name of the secret holding a token |

Exactly one of `login`, `tokenFile` and `tokenSecret`.

**`keys`** (where pseudonymisation keys live)

| key | type | default | meaning |
|---|---|---|---|
| `provider` | `none`, `local` or `transit`, required | | `none` means no pseudonyms, no key material and no resolve ([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)). `local` requires `local`, `transit` requires `transit`, and `none` allows neither |
| `local.rootFile` | path, required | | a file holding the 32-byte root the data keys are wrapped under |
| `local.dir` | path | in memory | where the wrapped data keys are kept. They are random, not derived, so this directory is the only copy. Unset keeps them in memory, which only a trial install should |
| `transit.prefix` | string | `audit` | what every key's name starts with: `<prefix>.<purpose>.<tenant>` |
| `transit.openbao` | `openbao`, required | | the engine and how to sign in |


## Refusals

What the schemas say, and what is checked after them. Every one is a start-up
error that names the key.

Beyond types, required keys and unknown keys, the schemas refuse:

- a `workloads` and an `anonymousWrites` together, or neither;
- `mode: receiver` with `archive`, `catalogues`, `keys` or `consume`, or with
  neither `stream` nor `forward`;
- `forward` with none, or more than one, of `nats`, `sqs` and `log`; `consume`
  with none, or both, of `nats` and `sqs`; `forward` in a writer;
- `require: archived` in a receiver, and `forward.log` without `require: logged`;
- `require` on an emitter with no `sink`;
- a `writer` without `archive`;
- `keys.provider` of `local` without `local`, of `transit` without `transit`,
  or either block beside a provider that is not its own;
- an `openbao` with none, or more than one, of `login`, `tokenFile` and
  `tokenSecret`;
- `secrets` with `root` and source `env`, or with source `file` or `ssm` and a
  root that is not an absolute directory or an SSM path;
- a `signer` with none, or more than one, of `keyFile`, `kmsKey` and `transit`;
- a database URL that carries a password;
- a query service with the `postgres` searcher and no `database`, or with
  `s3scan` and no `archive`.

After the schema, the binaries refuse:

- a `require` the chain can never give (the guard, at start-up), and an
  emitter's `require` with no `sink.expect`, or one weaker than it;
- `forward.sqs.fifo: true` on a URL that does not end in `.fifo`;

- `stream.ackWait` not longer than `roll.interval`: a writer gathers records
  for one interval before it writes them and leaves them unacknowledged
  meanwhile, and a stream that gives up waiting sooner offers the same records
  to another writer;
- `replicas` above 1 without `database`: deduplication in one process only
  absorbs a repeat on the replica that saw the original, so a redelivery
  landing on another would be written twice;
- `replicas` above 1 with local keys and no `keys.local.dir`: each replica
  would mint its own keys and the same person would get a different pseudonym
  on each;
- an `exports.bucket` that is the archive's bucket on the same endpoint: an
  export is an unlocked copy meant to be cleared, and the archive's policy
  denies every delete;
- `keys` on the query service without `archive`, or with the local provider and
  no `keys.local.dir`: resolve opens what the writer sealed in the archive;
- a `database.url` that does not parse;
- a profile that demands a stricter `lockMode` than the archive's: the writer
  refuses at start-up, naming the profile and both modes;
- a profile whose name contains `/`: it is a key component.

The grants file has refusals of its own, listed with it under [Query service](configuration-observe-query.md#query-service).

