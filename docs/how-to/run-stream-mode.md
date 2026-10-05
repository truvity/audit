# Run the chart in stream mode

## Purpose

Switch an installation to the stream shape (receivers in front, JetStream, N writers
behind), or install it that way, and check the parts specific to it.

## Preconditions

- The [Kubernetes tutorial](../getting-started/kubernetes.md) works in direct mode, or you
  have its prerequisites (a bucket, a database with roles).
- A NATS account with JetStream for this application, and a broker policy for who may
  connect.
- Which shape you are leaving or choosing: [stream mode](../explanation/stream-mode.md).

## Before you start

- **`stream.ackWait` must exceed `roll.interval` plus the longest a put can take.** The
  writer refuses to start otherwise: a stream that gives up waiting sooner offers the
  same records to a second writer and the day's objects quietly double.
- **`writer.config.replicas` must equal `writer.consumers`**, and more than one writer
  needs a `database`. The chart refuses a mismatch and a missing database.
- **A receiver must not hold the archive's write identity.** In stream mode the chart
  refuses a receiver and a writer that share one ServiceAccount name.
- **`DiscardNew` is the right stream policy:** when the stream is full, refuse a publish
  rather than silently drop the oldest evidence. Size the stream for the longest writer
  outage you want to survive.
- **`extensions.billing` and `extensions.quotas` currently render nothing.** The toggles are
  there so that values do not change when the work behind them lands; the chart refuses
  `billing` with no metering profile and `quotas` without `mode: stream`.
- **A token the broker refuses at start-up stops the pod**, with the reason in its log. A
  refusal later is retried on the next reconnect with the file read again; the token
  itself is never logged.

## Steps

1. **Set the mode and the two config blocks.** The receiver is `audit-writer` with
   `mode: receiver`: it holds neither the archive nor a key. The full file is
   [`charts/audit/examples/stream.yaml`](../../charts/audit/examples/stream.yaml), which the
   chart's tests render; the stream parts of it are:

   ```yaml
   audit:
     mode: stream
     replicas: 2                       # receivers
     receiver:
       config:
         apiVersion: audit.truvity.github.io/audit-writer/v2
         secrets: {source: file, root: /etc/audit/secrets}
         mode: receiver
         deployment: /etc/audit/deployment.yaml
         workloads: /etc/audit/workloads.yaml
         database:
           url: postgres://audit_writer@db.example.com:5432/audit?sslmode=verify-full
           passwordSecret: database-password
         stream:
           nats:
             url: nats://nats.app.svc:4222
             tokenFile: /var/run/audit/stream/token   # the broker verifies who connects
           name: AUDIT
       secretFiles:
         - {name: database-password, secretName: audit-db, key: password}
       tokens:
         - {audience: nats, mountPath: /var/run/audit/stream}
     writer:
       consumers: 3
       config:
         replicas: 3                     # must equal writer.consumers
         stream:
           nats: {url: nats://nats.app.svc:4222, tokenFile: /var/run/audit/stream/token}
           name: AUDIT
           consumer: audit-writer
           batch: 100
           ackWait: 2m
         roll: {interval: 30s, maxRecords: 5000}
         # archive, database, apiVersion, secrets: as in the example file
       tokens:
         - {audience: nats, mountPath: /var/run/audit/stream}
   ```

   Expected: `helm template` renders a receiver and `writer.consumers` writers and refuses
   `mode: stream` without `receiver.config` or `writer.config.stream`. Roll back: set
   `mode: direct`; switching is a change to the receiver's configuration, not to any record.

2. **Authenticate to the stream with the projected token, never a stored credential.** Give
   the receiver and the writers a `tokens` entry of the broker's audience (`nats`) and
   name the file in `stream.nats.tokenFile`; it is read afresh on every connect because
   the kubelet replaces it before it expires. The broker's side is the deployment's: an
   auth callout that takes the token from the connect request, reviews it with the cluster
   (a `TokenReview`) and answers with a user in the account the namespace maps to. A
   broker that verifies nobody needs neither `tokenFile` nor the `tokens` entry.

   A broker that binds the session to the token ends it when the token expires, so each pod
   reopens its connection thirty seconds before the token it presented does. A publish in
   flight across a reconnect is sent again under the same message id, so it neither fails
   its caller nor lands twice. Verify: pods connect and the receiver is ready (`/readyz`).
   Roll back: remove `tokenFile` and the `tokens` entry on a broker that verifies nobody.

3. **Point the query service at the grants** that name your console's issuer
   ([who the query service trusts](../explanation/stream-mode.md#who-the-query-service-trusts)).

4. **Scale to the load.** Receivers scale with the application's request rate (they validate,
   publish and acknowledge). Writers scale with the record rate and the number of profiles,
   because each record becomes one object per profile per roll; they are the pods that
   touch S3 and Postgres.

## Afterwards

```console
$ audit conformance --query https://audit-query.app.svc:8080 \
      --profile security --token-file ./token
$ audit verify --profile security --last 24h \
      --bucket audit-eu-example-1 --prefix audit/app
```

Then check what is specific to this shape: the consumer's pending count must not grow, and
a writer rollout leaves it briefly raised and then flat. A count that never returns to zero
means the writers cannot keep up or cannot write ([recover from an outage](recover-from-an-outage.md)).
