# Chart values

The values of `charts/audit/values.yaml`, which comments every one, and what the chart refuses.
How to install is in [the Kubernetes tutorial](../getting-started/kubernetes.md).

The chart passes configuration through. Each component has a `config:` block,
rendered as it stands (`toYaml`) into a ConfigMap `<fullname>-<component>-config`
and mounted at `/etc/audit/config.yaml`. A key under `config:` is the
binary's key, validated twice: by `values.schema.json`, which is generated and
embeds the same schemas, and by the binary at start-up. The chart translates
none of it.

| component | `config:` is the configuration of | notes |
|---|---|---|
| `writer` | `audit-writer` | direct mode: the one pod. Stream mode: the consumers, `writer.consumers` of them |
| `receiver` | `audit-writer` with `mode: receiver` | stream mode only |
| `query` | `audit-query` | `query.enabled` |
| `observe` | `audit-observe` | `observe.enabled`: one Deployment, `<fullname>-observe`, with a ServiceAccount of its own and no Service |
| `migrate` | `audit migrate` | `migrate.enabled`, a pre-install and pre-upgrade hook Job |
| `jobs.notary` | `audit-notary`, in its own image | one CronJob, hourly, off by default (`jobs.notary.enabled`), under a ServiceAccount of its own that the chart refuses to be the writer's |
| `jobs.verify`, `jobs.purge`, `jobs.clockSync` | `audit verify`, `purge`, `clock-sync` | one CronJob each. The verify job is one CronJob (`<fullname>-verify`) covering the `profiles` its config lists, or every profile |

## What is a value and what is configuration

<!-- generated: chart-values -->
Everything else under a component is the platform's, not the binary's:

| value | meaning |
|---|---|
| `secretFiles` | a list of `{name, secretName, key}`: a Secret's key as the file `/etc/audit/secrets/<name>`. The config names `name` in a `...Secret` field (`passwordSecret`, `credentialsSecret`, `tokenSecret`) and says `secrets: {source: file, root: /etc/audit/secrets}`, which the chart checks. `optional: true` allows a missing key |
| `secretEnv` | **deprecated**, for version-1 configs only ([upgrade](../how-to/upgrade/v0.13.md)): environment variables taken from a Secret's keys. `optional: true` allows a missing key |
| `secretMounts` | a list of `{secretName, mountPath}`: a Secret mounted read-only as a directory, for a key or a root the config names by path (`local.rootFile`, a key file) |
| `tokens` | a list of `{audience, mountPath, expirationSeconds, path}`: a projected service-account token of that audience (lifetime 3600 by default), a file named `token` (or `path`) in the directory `mountPath`, which the config names (`tokenFile`, `jwtFile`). It is read afresh by whatever names it, because the kubelet replaces it before it expires |
| `serviceAccount` | the identity of the component, for Pod Identity or IRSA annotations. Every component has its own, `{create, name, annotations}`: `receiver.serviceAccount`, `query.serviceAccount`, `jobs.*.serviceAccount`. Created, it is `<fullname>-<component>` unless `name` says otherwise; with `create: false` the component runs as `name`, or as the release's own top-level `serviceAccount` (the writer's) when `name` is empty. In stream mode the chart refuses a receiver and a writer with the same name |
| `replicas`, `writer.consumers`, `query.replicas` | pod counts. `replicas` is the front door's: the writer in direct mode, the receiver in stream mode |
| `schedule`, `enabled` | for each job |
| `image`, `resources`, `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`, security contexts | the pods |
| `terminationGracePeriodSeconds`, `preStopSleepSeconds` | how a write-path pod stops. The writer takes up to 30s to stop taking records and write what it holds, so the grace period (default 45) must outlast that plus the front door's preStop sleep (default 5, a sleep action, 0 turns it off); the chart refuses a grace period that does not |

The values that are not configuration of a binary:

| value | meaning |
|---|---|
| `mode` | `direct` (one process: the front door and the write path) or `stream` (a receiver in front, `writer.consumers` writers behind). It decides which Deployments are rendered; the binaries' own `mode` is in `receiver.config` |
| `profiles`, `externalIdentifiersAreOpaque` | rendered as the profile document, `/etc/audit/deployment.yaml`, which every config's `deployment` names. `externalIdentifiersAreOpaque` declares that the identifiers the installation receives for external people mean nothing outside its own database, which relaxes a profile's `external: pseudonym` to `clear` ([framework profiles](profiles.md#what-a-deployment-can-relax)) |
| `workloadIdentity.issuers`, `.audience`, `.workloads` | `audience` is the audience an issuer entry takes when it names none. Rendered as `/etc/audit/workloads.yaml`, which the writer's `workloads` names. The chart refuses an installation that keeps an index and verifies callers with no `workloads` mapping |
| `query.grants` | rendered as `/etc/audit/grants.yaml`, which the query service's `grants` names |
| `catalogues` | catalogue documents by name, mounted at `/etc/audit/catalogues` for the writer's `catalogues` |
| `keysVolume.enabled`, `.size`, `.storageClass`, `.accessModes`, `.existingClaim` | the volume for the `local` key provider, mounted at `/var/lib/audit/keys` on every pod that writes. The keys are random, not derived, so this is the only copy: back it up, and use ReadWriteMany for more than one replica. `query.keysVolume: true` mounts it read-only on the query service for resolve |
| `trust.configMap`, `trust.key` | a CA bundle trusted beside the system roots, e.g. trust-manager's for a private chain, mounted at `/etc/audit/trust/<key>` on every pod, for the `ca` and `caFile` keys and a Postgres URL's `sslrootcert` to name |
| `extensions.billing.enabled`, `extensions.quotas.enabled` | the two projections, both off. The toggles are here so that a deployment's values need not change when the work behind them lands; today each renders nothing |
| `query.route.enabled`, `.parentRefs`, `.hostnames`, `.pathPrefix`, `.annotations`, `.securityPolicy` | an HTTPRoute (Gateway API) to the query Service, and with `securityPolicy` an Envoy Gateway SecurityPolicy on it. Off by default. `parentRefs` and `hostnames` are required; `pathPrefix` is stripped before the service sees the request; `securityPolicy` is the SecurityPolicy's `spec` without `targetRefs`, `targetRef` and `targetSelectors`. See the [chart README](../../charts/audit/README.md#publishing-the-query-service) |
| `networkPolicy.enabled`, `.ingressFrom`, `.queryIngressFrom` | who may reach the writer's sink and the query service. Empty `queryIngressFrom` leaves the query service open in the cluster; once set, it must include the gateway's namespace when `query.route` is on |

<!-- /generated -->

## Mount points

A config names these by path. The chart provides:

| path | what | from |
|---|---|---|
| `/etc/audit/config.yaml` | the component's own configuration | `<component>.config` |
| `/etc/audit/deployment.yaml` | the profile configuration | `profiles`, `externalIdentifiersAreOpaque` |
| `/etc/audit/workloads.yaml` | the issuers and workloads | `workloadIdentity` |
| `/etc/audit/grants.yaml` | the query service's grants | `query.grants` |
| `/etc/audit/catalogues/` | the catalogues | `catalogues` |
| `/etc/audit/trust/<key>` | the CA bundle | `trust` |
| `/var/lib/audit/keys` | the local key directory | `keysVolume` |

Anything else a config names, such as a token or a key file, is mounted by the
component's `secretMounts` or `tokens` at the path you give them.

## What the chart refuses

The chart checks what only the platform can see, in
`charts/audit/templates/_checks.tpl`:

- `mode` is `direct` or `stream`, and `profiles` is not empty;
- `writer.config.replicas` equals the number of writer pods the chart renders
  (`replicas` in direct mode, `writer.consumers` in stream mode): the writer
  refuses to run several without a database and refuses in-memory keys with
  several, and can only do that if it is told the truth;
- more than one writer pod needs `database` in `writer.config`;
- stream mode needs `receiver.config` with `mode: receiver`,
  `writer.config.stream` (or `consume`) and a `database` in `writer.config`, and
  `writer.config.mode` must not be `receiver`; direct mode refuses a receiver
  writer;
- more than one writer pod with a `keysVolume` that lacks `ReadWriteMany`, and
  `query.keysVolume` without an enabled `keysVolume` or without
  `ReadWriteMany`;
- `workloads` in the writer's config with no `workloadIdentity.issuers`, and
  issuers with no `workloads` in the config; an installation with an index and
  issuers but no `workloadIdentity.workloads`; a workload that names no issuer
  while more than one is trusted;
- `query.grants.issuers` empty when the query service is enabled, and a query
  `database.url` equal to the writer's, because an owner bypasses the tenant
  policies;
- `query.route.enabled` without `query.enabled`, `parentRefs` or `hostnames`, and
  a `securityPolicy` that sets `targetRefs`, `targetRef` or `targetSelectors`;
- the indexer (`observe.enabled`) running as the writer's, the query service's
  or the receiver's ServiceAccount, or connecting to the database as the
  writer's, the query service's or the migration's role (an owner), and a
  writer that connects as the migration's role: each part's identity is its
  own, at the cloud role and at the database role;
- the notary running as the writer: `jobs.notary.serviceAccount.create: false`
  (it would run as the release's account), a notary account that carries the
  writer's annotations (the same cloud role), or a notary that signs in to
  OpenBAO under the writer's role or token: whoever writes the archive and can
  also sign for it can choose what to sign;
- `extensions.billing` without a profile composed from a metering framework profile, and
  `extensions.quotas` without `mode: stream`.

A value from before the file, such as `bucket` or `lockMode` at the top level,
is not accepted: it fails against `values.schema.json`, naming the key.

## Examples

Four complete value files are kept beside the chart and rendered by its tests:

| file | for |
|---|---|
| [`direct.yaml`](../../charts/audit/examples/direct.yaml) | an internal service: the front door and the write path in one process, the index in a small Postgres |
| [`stream.yaml`](../../charts/audit/examples/stream.yaml) | a product: receivers, JetStream, N writers |
| [`external-writer.yaml`](../../charts/audit/examples/external-writer.yaml) | writer on Lambda, observe and query here (`writer.enabled: false`) |
| [`sqs.yaml`](../../charts/audit/examples/sqs.yaml) | a receiver or writer over SQS |

All four are in configuration version 2. The sink URL in them names the writer's Service, which is
the release's full name: `audit` there, and `<release>-audit` under an application's own.
