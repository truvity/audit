# 0011. One installation per service or product, in that application's namespace

- Status: accepted
- Date: 2026-09-22

## Context

Until now this component was written to be deployed once and shared: a
central installation with its own namespace, a registry service that took
every application's catalogue, a writer every application sent to, and one
query service in front of them all. Applications were plugins of it.

Deploying it that way raised questions none of which have a good answer.
Which cluster owns the installation, when the applications that emit are
spread across several? How does a writer in one namespace trust workload
identities from another, and how does an operator keep that trust from
becoming a way for one application to write another's records? Whose
Postgres is it, and who is paged when it fills? A rollout of the shared
writer stops every application that declares an action `block`. And an
application that only ever wants its own trail pays for a registry service,
a second namespace and a cross-namespace network policy to get it.

The alternative is to stop treating the trail as infrastructure that
applications connect to, and treat it as a part of the application, the way
a database or a cache is: not shared, small, and owned by the team that owns
the application.

## Decision

**One installation per service or product, in that application's namespace,
rendered by the application's own chart** (this repository's chart as a
dependency). An installation is the receiver, the writer, the query service,
the index database and the CronJobs — small Deployments that exist once per
application.

**The emitter stays an in-process library; everything after it is a
service.** The application validates against its catalogue and waits for the
acknowledgement in its own process. The receiver, the writer and the query
service are separate Deployments, so that:

- a fix, or a change of write strategy, never rebuilds the application;
- the application holds no credentials for the bucket, the index or the
  stream, only the address of a service in its own namespace;
- the write path scales separately from the application.

**The environment owns the bucket; each application writes under its own
prefix** (`audit/<application>/…`). Object Lock, replication, deny-delete
and lifecycle are configured once per environment. Each installation's IAM
is scoped to its prefix. Tenant identifiers stay the application's own:
there is no tenant registry, because nothing joins two applications' records
at write time.

**`RegisterCatalogue` is served by the receiver.** One installation has one
emitting application, so a registry as its own Deployment is a service
account, a Service and a network policy for a single call. The receiver
already validates every record against the catalogue; it now also accepts
it.

## Consequences

- **A cross-application view is a read, not a design.** Nothing in the write
  path knows about more than one application. A view over several is a
  read-only query service pointed at several prefixes, added later, with no
  writer touched. Until someone asks for it, it does not exist.
- **N installations cost N sets of small Deployments.** The receiver and the
  query service are one or two pods each; the jobs are CronJobs. The
  expensive parts — the bucket, the Postgres cluster, the stream — are ones
  the application already has, or one per environment.
- **Each application's trail fails alone.** A rollout, a bad catalogue or a
  full index stops one application's records. This is the point.
- **Versions move independently.** Two applications may run different
  versions of this component against the same bucket, because the archive
  layout is the contract between them, not the code.
- **The chart is instantiated, not deployed.** It has to render from an
  application's values: the mode, the prefix, the database, the grants. See
  [the deployment pages](../deployment/direct.md).
- **What the earlier shape provided is gone**: the central installation, the
  registry service, `workloadIdentity.workloads` as a map of many
  applications, and a shared grants file. An installation admits one
  application, and its grants name that application's readers.

## Alternatives considered

- **One installation per environment, applications as plugins.** Fewer
  moving parts to deploy, and one place to look. But it makes the trail a
  cross-team dependency in the request path of every `block` action, needs
  cross-namespace workload trust, and gives one team's incident to another
  team's on-call. It is also what this repository already was, and the
  questions above are the ones it could not answer.
- **One installation per cluster.** The same, one level down, with the added
  problem that an application's records then live wherever it happens to
  run rather than where it belongs.
- **A writer inside the application** (a library, no services). Fewest
  Deployments of all, and it was built and documented here as "embedded".
  It puts the bucket's credentials in the application's pods, makes every
  fix an application release, and ties the write path's scaling to the
  application's. Withdrawn as a deployment shape: the packages stay public
  because the services are built on them, but no deployment is described
  that way.
