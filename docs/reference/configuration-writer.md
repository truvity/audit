# Configuration: the writer

The keys of `audit-writer` (the receiver and the writer), the durability knobs, the writer Lambda's
file, and the workload identity file the receiver reads. Shared blocks (`database`, `bucket`,
`archive`, `sink`, `openbao`, `keys`) are in [configuration](configuration.md#shared-blocks).

## audit-writer

One binary in two roles, chosen by `mode`. As a `writer`, the default, it
serves the sink, writes the archive and consumes a stream when `stream` is
set. As a `receiver` it serves the sink and publishes to the stream, and holds
neither an archive nor a key provider: a receiver holding either would be a
writer. In direct mode one process in `writer` mode is both. In stream mode
the receiver publishes to JetStream and acknowledges the replicated publish,
and the same image runs again as the writer. Both serve `RegistryService`, so
the application registers its catalogue with the address it writes to.

| key | type | default | meaning |
|---|---|---|---|
| `mode` | `writer` or `receiver` | `writer` | the role above |
| `listen` | `listen` | `:8080` | the address the sink is served on |
| `deployment` | path, required | | the profile configuration |
| `workloads` | path | | the file naming the issuers trusted to say which workload is publishing ([Workload identity](#workload-identity)). Exactly one of `workloads` and `anonymousWrites` |
| `anonymousWrites` | `true` | | accept writes from callers nobody verified, stamped with no observer. For a trial install only |
| `catalogues` | path | none | a directory of catalogues registered at start-up. An application that registers its own over `RegisterCatalogue` needs none. Not in a receiver |
| `archive` | `archive` | | required in `writer` mode, refused in `receiver` mode |
| `database` | `database` | none | the shared deduplication table and the catalogue registry, in the application's Postgres, as the writer's **own** role (`audit migrate --writer`): it holds those tables and none of the index, which `audit-observe` writes. Without it the writer deduplicates in process and may run one replica only. The writer refuses to start on a schema version it does not know and never migrates itself |
| `replicas` | integer, at least 1 | 1 | how many writers share this stream: the number of writer pods, which the writer cannot see for itself |
| `keys` | `keys` | none, which is provider `none` | pseudonymisation keys. Not in a receiver |
| `forgetIdentities` | boolean | false | do not keep the identity behind each pseudonym, sealed under its key. By default it is kept, so that resolve can find it |
| `require` | `logged`, `queued` or `archived` | `archived` for a writer, `queued` for a receiver | the weakest durability the chain may give ([0017](../decisions/0017-sink-durability-and-transports.md)). At start-up the process wraps its chain in `sink.Guard` and refuses to run if the chain can never give it; a write acknowledged weaker fails. See [Durability](#durability-require-forward-consume) |
| `forward.nats`, `.sqs`, `.log` | exactly one; `nats` and `sqs` as below | | a receiver's onward transport. Required in `receiver` mode (or `stream`, its NATS shorthand); refused in `writer` mode. `forward.nats` takes the keys of `stream` |
| `forward.sqs.queueUrl` | string, required with `forward.sqs` | | the queue the receiver publishes to |
| `forward.sqs.region` | string | the SDK's (`AWS_REGION`) | the queue's region |
| `forward.sqs.fifo` | boolean | derived from the URL | the queue is FIFO. It must agree with the URL, which ends in `.fifo` for one |
| `forward.log` | `{}` | | the `log` sink: one JSON line per record on standard output, which survives nothing but the log pipeline. Allowed only with `require: logged` |
| `consume.nats`, `.sqs` | exactly one | | what a writer reads its records from, beside its own sink. Not in a receiver. `consume.nats` takes the keys of `stream` |
| `consume.sqs.queueUrl`, `.region`, `.fifo` | as `forward.sqs` | | the queue the writer consumes |
| `consume.sqs.batch` | integer, 1 to 10 | 10 | how many messages are received at once |
| `consume.sqs.visibility` | duration | `1m` | how long a received message is hidden from other consumers while the writer writes it. It must outlast a write to the bucket, or the message is delivered twice |
| `stream.nats.url` | string, required with `stream` | | the JetStream server, for example `nats://nats:4222` |
| `stream.nats.tokenFile` | path | none: no credentials | the token presented to a broker that verifies who connects, read afresh on every connect ([stream](../how-to/run-stream-mode.md)) |
| `stream.name` | string | `AUDIT` | the stream |
| `stream.consumer` | string | `audit-writer` | the durable consumer this installation's writers share |
| `stream.batch` | integer, at least 1 | 100 | how many records are taken at once |
| `stream.ackWait` | duration | `2m` | how long the stream waits for a batch to be taken before offering it again. Records are acknowledged only once they are in the archive, so it must exceed `roll.interval` plus the longest a put can take |
| `roll.interval` | duration | `30s` | how long gathered records wait before they are written. In direct mode there is no stream to gather from, and it is only how long an object may stay open inside one write |
| `roll.maxRecords` | integer, at least 1 | 5000 | how many gathered records are written at once. The roll ends at whichever of the two is reached first, or at the roller's byte limit |

`stream` is the NATS shorthand and is kept as it was: in a receiver it is
`forward.nats`, in a writer `consume.nats`, with the same defaults. Give it or
the longhand, not both. A `receiver` requires `stream` or `forward`. A writer
with neither `stream` nor `consume` only serves its own sink; with one, it
also consumes.

### Durability: `require`, `forward`, `consume`

Every acknowledgement carries a durability ([0017](../decisions/0017-sink-durability-and-transports.md)):
`archived` (the object is in the bucket), `queued` (a replicated queue holds it
and will deliver it) or `logged` (a log line). `require` is the floor a process
holds its own chain to, and its default is the strongest the mode can give, so
that anything weaker is something a person wrote down:

- a **writer** defaults to `archived`: it puts the object itself, and a
  deployment that wants a weaker promise says so;
- a **receiver** defaults to `queued`, not `archived`: it holds no archive (a
  receiver holding one would be a writer), so `archived` is not its to promise,
  and it is refused there. Its promise is what its onward transport gives, and
  the writers behind it are what reach `archived`.

At start-up the process builds its chain (the receiver, and the transport
`forward` names; or the writer) and computes the best it can ever give. A chain
below `require` is a start-up error, not a surprise on the first privileged
action. `forward.log` can give `logged` at most, so it is allowed only with
`require: logged`, which the schema, the loader and the guard each refuse
otherwise: it is for a deployment that has chosen its log pipeline as its
record.

The queue's credentials are never in the file. `sqs` uses the AWS SDK's ambient
credentials, which on Kubernetes is the pod's workload identity (EKS Pod
Identity, or IRSA through a service-account annotation), the same way the
archive's bucket does. The role needs `sqs:SendMessage` for the receiver,
`sqs:ReceiveMessage`, `sqs:DeleteMessage` and `sqs:ChangeMessageVisibility`
for the writers. `charts/audit/examples/sqs.yaml` is a full SQS install; its
transport is not yet tested against live AWS, only against a fake and
LocalStack.

A process that records through a writer of its own (`audit-query` and every
job with a `sink`) takes `require` too, with `sink.expect` saying what that
writer gives: `require` unset checks nothing, and set it needs `expect` at
least as strong, checked at start-up, with every acknowledgement checked
afterwards.

The receiver verifies who writes with `workloads` and stamps the caller's
service account as each record's observer. Without it, it refuses to start
unless given `anonymousWrites: true`. Records that arrive over the stream
carry no verified observer: the stream's own authentication is what admits a
publisher there. The observer version stamped on records is the build's
version and is not configurable.

### audit-writer-lambda

The write path as an AWS Lambda function behind an SQS event source mapping
([AWS](aws-pulumi-library.md)). It runs the same writer under the same
`require: archived` guard as `audit-writer`, and has none of the rest of it: no
listener, no registry, no stream, no database, no HTTP front door. A function has
no process that stays up, so there is nothing to serve and no replica count to
state; what the Postgres table does for `audit-writer` is a DynamoDB table here.
The file is read from `--config`, or `AUDIT_CONFIG`, or `/opt/audit/audit.yaml`,
or `/var/task/audit.yaml`, whichever of them is first to exist. In the Pulumi
library's deployment it is in the function's configuration layer at
`/opt/audit/audit.yaml`, named by `AUDIT_CONFIG`, rendered from the stack's own
arguments ([AWS](../explanation/aws-lambda.md#configuration-as-a-layer)). The writer says
which file it read, and its digest, in its start-up record
([evidence](configuration.md#evidence-the-writers-start-up-record)).

| key | type | default | meaning |
|---|---|---|---|
| `deployment` | path, required | | the profile configuration, in the layer at `/opt/audit/deployment.yaml` |
| `catalogues` | path | none | a directory of catalogues registered at start-up, `/opt/audit/catalogues` in the layer. There is no registry service to register one with |
| `archive` | `archive`, required | | the bucket, `lockMode` (default `compliance`; the library sets the bucket's own) and `kmsKey` |
| `keys` | `keys` | none | pseudonymisation keys. Only a provider a function outside a VPC can reach is usable: `transit` over a public address |
| `forgetIdentities` | boolean | false | as in `audit-writer` |
| `dedupe.dynamodb.table` | string, required | | the table: a string hash key `pk`, TTL on `expires_at` |
| `dedupe.dynamodb.region` | string | the SDK's (`AWS_REGION`) | the table's region |
| `dedupe.dynamodb.window` | duration | the widest window any profile asks for | how long a written record's id is remembered. It wants to be at least as long as the queue keeps a message (SQS: 14 days at most) |
| `require` | `logged`, `queued` or `archived` | `archived` | the weakest durability the chain may give; the writer gives `archived` at best |

The notary Lambda reads `audit-notary`'s file, unchanged
([audit-notary](configuration-jobs.md#audit-notary)), from the same place; its `signer.kms` names the
seal key by alias.


## Workload identity

The receiver reads one file, named by `workloads` (in the chart,
`/etc/audit/workloads.yaml`, rendered from `workloadIdentity`):

```yaml
issuers:
  - url: https://oidc.example.com/id/CLUSTER   # the cluster's service-account issuer
    audience: audit
```

A caller presents its projected service-account token as a bearer. The jobs and the
query service read it from the file their `sink.tokenFile` names on every
request, because the kubelet replaces it before it expires; the interactive
`audit` commands read `--token-file` or `AUDIT_TOKEN_FILE` the same way.

Whose catalogue a registration is, comes from that verified identity and never
from the document. The file's `workloads` list is what says so: which service
account speaks for which source. A caller missing from it registers as nobody
and its registration is refused, which is also why an installation that keeps
an index and verifies callers must fill the list in — the chart refuses to
render otherwise. There is deliberately no shortcut that lets any verified
caller register for the application: a workload that could register under
another source could describe another application's records, and everything
downstream reads the description.

The issuer's discovery document is fetched at start-up, so it must be reachable
over HTTPS from the pods. A managed cluster's public OIDC provider is. The API
server's own in-cluster issuer usually is not without its CA and a credential,
which this does not yet take.

