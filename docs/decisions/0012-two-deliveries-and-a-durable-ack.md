# 0012. Two deliveries, and the receiver's acknowledgement means durable

- Status: accepted
- Date: 2026-09-22

Supersedes the delivery modes of
[0004](0004-sink-interface-and-transports.md); the rest of 0004 stands.

## Context

[0004](0004-sink-interface-and-transports.md) gave an action three
deliveries: `block` waited for durability, `outbox` wrote the record to a
file on the pod and published it later, and `best_effort` could drop under
pressure. The catalogue chose per action.

Three deliveries turned out to be one too many in each direction.

`outbox` was written for one failure: the receiver is down *and* the
application's pod dies before it comes back. It costs a volume on every
emitting pod, a size cap, a reader that has to survive a partial write, and
a failure mode of its own — a full or unwritable volume, on the request
path. A pod's local disk is also not durable in the way the name suggests:
it dies with the node, and a pod that never comes back takes the file with
it.

`best_effort` says that a record may be dropped without anybody waiting. In
a component whose point is that the trail is complete, an action either
matters enough to keep or does not belong in the catalogue. In practice it
was used to mean "do not make the caller wait", which is what a retrying
queue gives without the licence to drop.

The third thing 0004 left unsaid is what the acknowledgement means when the
writer is not the process that puts the object. With a stream, the receiver
acknowledges a publish; with direct writes it acknowledges a put. An
application cannot reason about a promise whose meaning changes with the
deployment.

## Decision

**Two deliveries**, chosen per action in the catalogue:

- **`block`** — the call returns when the receiver has acknowledged
  durability. If it cannot, the action fails. For a privileged sign-in, a
  key destruction, a billable operation.
- **`async`** (the default) — the call returns at once. The record waits in
  a **bounded in-memory queue** and is retried with backoff until the
  receiver acknowledges it. It is dropped, counted and reported through
  `OnDropped` only if the queue overflows.

`outbox` and `best_effort` are refused by the catalogue loader, with a
message naming the replacement. The file outbox is removed.

**The receiver's acknowledgement always means durable.** In stream mode that
is the stream's replicated publish acknowledgement. In direct mode it is the
object in the bucket: the receiver acknowledges an `async` batch only after
the roll that holds it has been put and indexed. Nobody is waiting on an
`async` batch, so the delay costs nothing but the queue's depth, and
`roll.interval` is the knob.

## Consequences

- **What can be lost is stated, and small.** A `block` record is never lost:
  the application had no acknowledgement to act on. An `async` record is
  lost only if the application's pod dies with the record still in its
  queue — up to one roll interval in direct mode, milliseconds in stream
  mode. A receiver or writer crash loses nothing, because it acknowledged
  nothing it had not stored.
- **Every dropped record is still a log line.** The emitter logs what it
  could not deliver, so the loss is visible where the application's other
  evidence is.
- **Two metrics matter**: the queue's depth (`audit.emit.queue.pending`) and
  what overflowed (`audit.emit.records.dropped`). A queue that is filling is
  the alert; a drop is the incident.
- **No volume, no cap, no file format** on the emitting pod, and one fewer
  thing that can fail on the request path.
- **An application chooses per action, not per deployment.** The same
  catalogue behaves the same way in direct and stream mode; only the size of
  the loss window changes.
- The writer's account of itself (`SelfReporting`) stays `async` by
  construction: it must not wait on itself.

## Alternatives considered

- **Keep the outbox for `block` actions only.** It would cover the case
  where a `block` action must succeed while the receiver is down. But that
  is exactly the case where failing is correct: an action that may not go
  unrecorded may not proceed unrecorded either.
- **A sidecar with a volume, instead of a file in the application's pod.**
  The same durability question, plus a second container in every pod, to
  cover a window that a replicated stream closes better.
- **Keep `best_effort` for high-volume actions.** High volume is a reason to
  use a stream and a bigger queue, not a reason to license loss. An action
  nobody would miss can be left out of the catalogue.
