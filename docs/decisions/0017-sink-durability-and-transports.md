# 0017. Sink durability: the acknowledgement says how durable, and the start-up refuses less

- Status: accepted
- Date: 2026-10-02

Supersedes [0012](0012-two-deliveries-and-a-durable-ack.md). Extends
[0004](0004-sink-interface-and-transports.md): its sink contract and adapter
model stand, and this adds what an acknowledgement carries and which
transports exist.

## Context

[0012](0012-two-deliveries-and-a-durable-ack.md) made the receiver's
acknowledgement mean "durable", and defined durable per mode: a replicated
publish in stream mode, an object in the bucket in direct mode. That holds
while there are two shapes. It does not hold once a record can reach the
archive through a queue, a function invocation, a process-local spool or, as
a last resort, a log line: the word "durable" now covers things that differ
by orders of magnitude in what they survive, and an application cannot tell
which one it got.

0012 also let a full emitter queue drop its oldest records and count them.
For a component whose point is a complete trail, a drop is a decision made
by an overflow rather than by a person, and the caller cannot tell it
happened.

## Decision

**The acknowledgement carries a durability**, one of:

| durability | means | survives |
|---|---|---|
| `Archived` | the record is in the archive's bucket | everything the bucket survives |
| `Queued` | a durable, replicated queue holds it and will deliver it to the archive | the loss of a pod or a node, not of the queue |
| `Logged` | the process wrote it to its log | nothing but the log pipeline |

Durabilities are ordered, `Archived` over `Queued` over `Logged`. The
`Result` of `SinkService.Write` names the durability for the whole batch.

**Every hop is a Sink, and a Sink wraps the next.** A chain is built from
configuration: a spool in front of a queue publisher, a queue consumer in
front of the in-process writer. A hop returns the durability of the last hop
that took the batch, and a wrapper never reports more than its successor
did.

**`require:` is a start-up guard.** The configuration names the weakest
durability a sink chain may give (`require: archived`, `queued` or
`logged`). The process computes the best the chain it was configured with can
ever give and refuses to start if that is weaker. A write whose
acknowledgement is weaker than required at run time is a failure, not a
success with a caveat.

**A full spool fails the write.** A bounded queue in front of a sink that
cannot keep up refuses the next record with an error the caller sees,
instead of dropping the oldest. `block` waits for an acknowledgement that
meets `require`; `async` returns once the spool has the record, and a spool
that cannot take it returns the error to the caller. Dropping is no longer a
behaviour; `OnDropped` stays as the hook for the records a process loses
because it died.

**Transports, by platform:**

| platform | transports |
|---|---|
| Kubernetes | `nats` (JetStream, `Queued`), `http` (Connect to the receiver) |
| AWS | `sqs` (`Queued`), `lambda` (a direct invocation of the writer function) |
| everywhere | `s3` (in process: the writer's own put, `Archived`), `log` (`Logged`) |

Each transport is a Sink and a matching consumer; none touches the emitter
or the writer. `log` exists for development and for the deployment that has
accepted that a log pipeline is its record, and `require:` is what keeps it
from being the accident.

## Consequences

- **What an application was promised is a value, not a convention.** The
  same call returns `Archived` in one deployment and `Queued` in another,
  and a deployment that needs the first says so in `require:`.
- **A misconfigured chain fails at start-up**, not on the first privileged
  action: `require: archived` over a queue-only chain never starts.
- **Backpressure is visible.** A full spool is an error at the call site and
  a metric, where before it was a counter nobody was forced to read.
- The delivery modes `block` and `async` of 0012 stay as the catalogue's
  per-action choice of whether the caller waits; what changes is what they
  wait for and what a full queue does.

## Alternatives considered

- **Keep "durable" as one word.** It is what 0012 chose, and it is what
  stopped working once a queue and a function platform existed beside the
  bucket.
- **Make `Archived` the only acceptable answer.** Simple, and wrong for a
  deployment where a replicated queue is the real boundary and the bucket is
  one hop behind it; it would send those deployments to invent their own
  weaker acknowledgement.
- **Keep dropping the oldest when a queue fills.** Cheaper for the caller;
  it turns a capacity problem into silent loss, which is the one failure
  this component exists to make loud.
