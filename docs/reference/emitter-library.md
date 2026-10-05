# Emitter library

The Go emitter's options (`emit.New`), the middleware and the two metrics worth alerting on. The
configuration files of the server binaries are in [configuration](configuration.md).


`emit.New(emit.Options{…})`:

| option | meaning |
|---|---|
| `Source`, `Catalogue` | the source this emitter speaks for, and its loaded catalogue; a record of an action the catalogue does not declare is refused |
| `Sink` | where records go: `sink.NewClient(httpClient, receiverURL)` for the receiver over Connect, with `auth.TokenFile` for the workload token. The application's sink is the receiver in its own namespace; the JetStream hop, in stream mode, is the receiver's, not the application's |
| `Timeout` | how long a `block` write may take. Default 10s |
| `Queue`, `Batch`, `Flush` | the `async` queue: how many records may wait (1024), how many are sent together (100), and how often (one second). A full queue drops the oldest, counts it and calls `OnDropped` |
| `Bounds` | size limits; default `record.Default` |
| `Version`, `Instance` | this process on every record; the writer replaces the observer's identity with the one it verified |
| `Hooks` | `OnDropped`, `OnFailed`, `OnWritten`, `OnRefused`: where a deployment counts and alerts |

`emit.Middleware(trustedHops)` records each request's client address, user
agent, request and trace ids on every record made while serving it.
`trustedHops` is how many proxies of your own sit in front: 0 records the
connection's peer, and getting it wrong records a load balancer as the actor's
address. `emit.Register(ctx, emit.Registration{…})` registers the catalogue
with the **receiver** at start-up — the same address the sink writes to,
because the receiver serves `RegistryService`.

`Options.Queue`, `Options.Batch` and `Options.Flush` size the async queue, and
`Options.Retry` is how long the emitter waits before trying a batch the sink
could not take, doubling up to a minute. There is no outbox option, and no
file: there are two deliveries
([0012](../decisions/0012-two-deliveries-and-a-durable-ack.md)).

Two metrics are worth alerting on, and they are the pair that says whether
anything was lost:

| metric | means |
|---|---|
| `audit.emit.queue.pending` | how many records are waiting to be acknowledged, and so what this process would lose if it stopped now. A number that only climbs is a receiver that has stopped acknowledging; drops follow. Published by `emit.InstrumentQueue` |
| `audit.emit.records.dropped` | records the queue overflowed and gave up on. Every one of them is also written to the application's log by the emitter (`Options.Logger`). This is the incident; the one above is the alert |

