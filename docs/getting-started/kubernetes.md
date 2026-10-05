# Getting started on Kubernetes

From nothing to an installation that archives a record, indexes it and verifies it, using the
chart in direct mode (the writer is the receiver; no stream). Start here; to move to a stream
later see [run the chart in stream mode](../how-to/run-stream-mode.md).

An installation belongs to one application and runs in that application's namespace, rendered
by the application's own chart with this repository's chart as a dependency
([0011](../decisions/0011-one-installation-per-service-or-product.md)). This chart is
instantiated, not deployed on its own.

## What you need

None of these is created by the chart. It takes references to all of them and refuses to render
when one is missing.

| you need | how |
|---|---|
| a bucket and a prefix for this application | [prepare the bucket](../how-to/prepare-the-bucket.md) |
| a Postgres database and a role for each part | [prepare the database](../how-to/prepare-the-database.md) |
| a reference clock for the clock-sync job | an NTP address the pods can reach (`169.254.169.123` on AWS) |
| a workload identity for the pods | Pod Identity or IRSA annotations on each component's ServiceAccount |
| the images | `ghcr.io/truvity/audit/` (`audit-writer`, `audit`, `audit-query`, `audit-notary`), one tag that also stamps the chart; all distroless with no shell |
| the application's catalogue | **nothing to install:** the application registers it with the receiver at start-up |

## 1. Add the dependency

```yaml
# the application's Chart.yaml
dependencies:
  - name: audit
    version: <version>
    repository: oci://ghcr.io/truvity/charts
```

One tag stamps the chart and the images, so the version above is the whole of what a deployment
pins.

## 2. Write the values

The application's values carry an `audit:` block. The smallest useful one composes the `security`
framework profile and runs the writer, the indexer and the query service. A secret is never in
the file: each `config:` names it (`passwordSecret`) and the file's `secrets` block says it is a
file under `/etc/audit/secrets`, which `secretFiles` projects from a Kubernetes Secret.

```yaml
audit:
  mode: direct
  replicas: 2                              # writer.config.replicas must say the same
  profiles:
    security:
      presets: [security]                  # the key is `presets`; each is a framework profile
  externalIdentifiersAreOpaque: true
  workloadIdentity:
    issuers: [{url: https://oidc.example.com/id/CLUSTER}]
    workloads: [{subject: "system:serviceaccount:app:api", source: app}]
  migrate:
    enabled: true
    config:
      apiVersion: audit.truvity.github.io/audit-migrate/v2
      secrets: {source: file, root: /etc/audit/secrets}
      database: {url: "postgres://audit_owner@db.example.com:5432/audit?sslmode=verify-full", passwordSecret: database-password}
      writer: audit_writer
      observe: audit_observe
      reader: audit_query
    secretFiles: [{name: database-password, secretName: audit-db-owner, key: password}]
  writer:
    config:
      apiVersion: audit.truvity.github.io/audit-writer/v2
      secrets: {source: file, root: /etc/audit/secrets}
      deployment: /etc/audit/deployment.yaml
      workloads: /etc/audit/workloads.yaml
      replicas: 2
      archive:
        bucket: {name: audit-eu-example-1, region: eu-example-1}
        prefix: audit/app
      database: {url: "postgres://audit_writer@db.example.com:5432/audit?sslmode=verify-full", passwordSecret: database-password}
    secretFiles: [{name: database-password, secretName: audit-db, key: password}]
  # observe, query, and the verify, purge and clock-sync jobs follow the same pattern:
  # see charts/audit/examples/direct.yaml
```

The whole file, with every component, is
[`charts/audit/examples/direct.yaml`](../../charts/audit/examples/direct.yaml); the chart's tests
render it. Each key of a `config:` is the binary's own, validated against its schema
([configuration](../reference/configuration.md)); the values the chart adds are in
[chart values](../reference/chart-values.md). Which framework profiles to compose is a policy
question ([which profiles to compose](../explanation/which-profiles-to-compose.md)).

Not in the smallest file but usually wanted: the notary (`jobs.notary`, off by default), which
seals each closed hour with a P-384 key that is not the writer's
([key custody](../explanation/key-custody.md#signing-key)); the chart refuses a notary that runs
as the writer.

## 3. Install

```sh
helm dependency build ./charts/<application>
helm upgrade --install <application> ./charts/<application> -n <app> -f values.yaml
```

The chart **refuses to render** a configuration the binaries would reject, or accept and get
quietly wrong: a configuration that does not match its binary's schema (a misspelt key, a
password in a URL), the writer's database given to the query service, `replicas` that is not the
number of writer pods. Each refusal says why
([the chart README](../../charts/audit/README.md)).

One refusal the chart cannot make is the binaries': a profile whose framework profiles demand a
lock stricter than `archive.lockMode` (`pci-dss` on `lockMode: none`, say). The writer refuses to
**start**, naming the profile and both modes, so the first rollout is where it shows.

## 4. Check that it works

1. **The writer is up.** `kubectl -n <app> rollout status deploy/<release>-audit`. Its
   readiness probe is `/readyz`: ready when the database answers, the catalogue registry can be
   read and a consumer is running; a 503 names the failed check. It refuses to start, with the
   reason in its log, if the database is at a schema version it does not know or a profile
   demands a lock the store is not written with.
2. **The application registered its catalogue.** It logs the registration at start-up, and
   refuses to start if the receiver refused the catalogue
   ([connect an application](../how-to/connect-an-application.md)).
3. **A record goes through.** Perform an action the catalogue declares, or run
   [the example application](../../examples/emit/main.go) against the receiver's Service.
4. **It is in the archive.**
   `aws s3 ls s3://audit-eu-example-1/audit/app/records/security/ --recursive` lists an object
   per profile, tenant and ingest batch.
5. **It verifies.** `audit verify --profile security --last 24h --bucket audit-eu-example-1
   --prefix audit/app` checks the archive from the archive alone.
6. **The query service keeps its contract.** `audit conformance --query
   https://audit-query.<app>.svc:8080 --profile security --token-file token`, after the first
   records land.
7. **Alerts.** With `OTEL_EXPORTER_OTLP_ENDPOINT` set on the pods by the platform, page on
   `audit.observe.index.deferred` and `audit.observe.index.lag`, the dead-letter counter, and in
   the application on `audit.emit.records.dropped` ([telemetry](../reference/telemetry.md)).

## 5. Next

- When something fails: [recover from an outage](../how-to/recover-from-an-outage.md),
  [repair or rebuild the index](../how-to/rebuild-the-index.md),
  [diagnose a scheduled job](../how-to/diagnose-a-scheduled-job.md).
- Hold or verify: [place a legal hold](../how-to/place-a-legal-hold.md),
  [verify the trail](../how-to/verify-the-trail.md).
- Switching on billing or usage quotas: [billing](../how-to/enable-billing.md),
  [usage quotas](../how-to/enable-usage-quotas.md).
- Upgrading: [the v0.13 upgrade](../how-to/upgrade/v0.13.md).
