# 0004. One sink interface at every hop; the queue is invisible

- Status: accepted
- Date: 2026-09-17

## Context

Emitters run in clusters with a message stream, in clusters without one,
and outside clusters as adapters. Audit needs a business-visible
acknowledgement for privileged and billable actions, which log pipelines
cannot give ([research/storage.md](../research/storage.md): the
OpenTelemetry Collector has no delivery guarantee by default, drops when
its queue fills, and has sampling processors). A prior internal telemetry
pipeline was lossy at three hops and is the negative example.

## Decision

There is one write contract, `SinkService.Write`
(`proto/audit/v1/sink.proto`). An emitter calls it. A queue publisher
implements it. A queue consumer calls it on the split writer. An adapter
outside the cluster calls it over Connect. The split writer is a library
that can be embedded in an application or run as a service.

Transports shipped: in-process, direct object storage, and a stream
publisher-consumer pair for NATS JetStream. Others (SQS, Kafka) are further
pairs behind the same contract and never touch emitters or the writer.

Delivery is a mode per action class from the catalogue: **block** waits for
durability at the next hop before the business request completes,
**outbox** writes to a local durable store and publishes later, and
**best_effort** may drop under pressure and alerts. Privileged,
authentication and billable actions default to block or outbox.

Adapters normalise foreign sources (a secret manager's audit device, an
identity provider's event listener, a code host's log stream, the
Kubernetes audit webhook) into the record before the stream. The writer
stamps `observer` from the publisher's verified workload identity; a
reporter never self-declares who it is.

## Consequences

- The stream is a buffer with a bounded horizon, not a store. The writer's
  object-storage PUT is the durability boundary.
- With the writer embedded there may be many writers; the hourly digest is
  therefore a separate job that lists the hour's objects, not a step inside
  one writer.
- Deduplication without a shared table is best-effort by event id at the
  emitter; the outbox mode covers restarts.
- OpenTelemetry is an optional mirror into a log store, never the primary
  path.

## Alternatives considered

- **OpenTelemetry Collector as the pipeline.** Ubiquitous SDKs, but no
  end-to-end acknowledgement, drops by default, PII rides with telemetry,
  and no NATS exporter upstream.
- **Direct object-storage writes from every pod.** No single point of
  failure, but one small object per pod per flush and a lost buffer on
  crash unless every event is its own PUT.
- **Transactional outbox everywhere.** Atomic with the business
  transaction, but only for events that originate in a database
  transaction. Kept as the outbox delivery mode.
