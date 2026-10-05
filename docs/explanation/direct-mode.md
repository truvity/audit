# Direct mode

The receiver is the writer. A record arrives over Connect, is validated,
split per profile, rolled into an object and put into the bucket, and only
then acknowledged. There is no stream and nothing to consume.

This is the shape for an internal service — an identity service, an
operations console, anything whose record rate is tens or hundreds a minute
rather than thousands a second — and for any cluster that has no message
stream to use.

## What it looks like

```mermaid
flowchart TB
  subgraph ns["the application's namespace"]
    subgraph APP["application pods"]
      E["emit"]
    end
    subgraph CTL["a second workload of the same application"]
      E2["emit"]
    end
    RW["audit-writer ×2<br/>receiver = writer"]
    Q["audit-query ×1"]
    OB["audit-observe ×1<br/>the indexer"]
    PG[("database")]
    CJ["CronJobs<br/>verify, purge, clock-sync"]
    E --> RW
    E2 --> RW
    RW -- "dedupe, registry" --> PG
    OB -- "index, cursors" --> PG
    Q -- "reads" --> PG
    E -- "the console's Audit page" --> Q
  end
  S3[("the environment's bucket<br/>audit/app/")]
  RW --> S3
  OB -- "lists, reads" --> S3
  Q --> S3
  CJ --> S3
```

Two receiver replicas are safe: the deduplication table is in Postgres, so
two pods cannot write the same record twice, and the application's client
follows the Service to whichever is ready.

## One record

A privileged sign-in, declared `block` in the catalogue:

```mermaid
sequenceDiagram
  participant P as the person
  participant A as the application
  participant R as receiver = writer
  participant S3 as bucket
  participant PG as database

  P->>A: sign in, good proof
  A->>A: validate against the catalogue
  A->>R: Record, block (Connect, projected token)
  R->>R: verify the caller, stamp the observer, split per profile
  R->>S3: put object, retention from the profile
  S3-->>R: stored
  R->>PG: mark the id
  R-->>A: durable
  A-->>P: signed in
  Note over A,S3: the bucket refuses, so the sign-in is refused
```

An `async` record takes the same path, except that the application does not
wait: it is queued, batched with whatever else is queued, and the batch is put
and acknowledged as one. The application's queue holds a record from the
moment it is recorded until that acknowledgement — one flush interval plus a
round trip, and that is the loss window if the pod dies. The emitter's `Flush`
and `Batch` are the knobs.

## Running it

The values, the index's database and the checks are in the
[Kubernetes tutorial](../getting-started/kubernetes.md) (the whole of
[`charts/audit/examples/direct.yaml`](../../charts/audit/examples/direct.yaml) is rendered by the
chart's own tests) and [prepare the database](../how-to/prepare-the-database.md). The index
is behind the archive by the indexer's settle window (two minutes by default,
[0020](../decisions/0020-observe-follows-the-bucket.md)); anything that must see a record
sooner reads the sink's acknowledgement, not the index. What to do when something fails is
in [recover from an outage](../how-to/recover-from-an-outage.md).
