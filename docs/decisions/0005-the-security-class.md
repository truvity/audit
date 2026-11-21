# 0005. The security class: what happened, with counted loss and no blocking it does not need

Status: accepted, 2026-11-21

## Context

A security record says that something happened: a sign-in, a grant, a refusal, a change. Every flow of a product is audited, refusals included. But the audit path can fail: the transport can be full, the disk can be full, a writer can be down. A product must decide what an operation does then.

Two simple answers are both wrong. If every operation fails when its record cannot be delivered, a full spool stops every audited operation, and a revocation, which must never wait on the log, waits. If every record is buffered in memory, a restart loses them silently. An audit trail that drops records without saying so is not one.

## Decision

### Three ways to emit

The emitter offers three calls. The product's action table says which one an action uses.

| Call | What it does | Used for |
|---|---|---|
| **Best effort** | Hands the message to the transport and does not wait. A failure is counted. | A refusal, a denial, a start, a reload, a creation of a data key, and every fact not named below |
| **Required** | Waits until the record is durable in the transport. When it is not, the call returns an error and **the operation fails.** | A grant, a token signature, a PIN release, a read of the log, and the successful outcomes that issue a credential |
| **Wait, never fail** | Asks for the blocking wait and carries on without it. The answer says the fact was not audited. | A tightening change: a revocation, a change of a signer's ring. A full spool must never block one. |

- **Durable means what the transport driver says:** accepted by the queue, acknowledged by the writer in direct mode, or fsync'd to the local spool. A transport answers an emit, blocking or not, only when the message is durable ([0002](0002-pieces-and-holders.md)).
- **A refusal or a failure of an action is answered as it was, whatever happens to its fact.** The fact of a refusal is best effort.
- **A call is audited whether it succeeds or not.** The exception is the health `check` ([0004](0004-the-record-format.md)).

### Loss is counted while the process lives

- **The emitter buffers nothing and returns the failure to its caller,** so the caller still logs the loss in its own journal. Failing every operation and buffering in memory were both rejected ([0002](0002-pieces-and-holders.md)).
- **The rule:** while the process lives, the log says how many best-effort facts it failed to deliver. The count is not complete: a restart or an exit loses a total that no record has reported yet, and an emitter that never emits again reports nothing.
- **The note.** For each tenant the emitter keeps, in memory, a running total of its failed best-effort emits since the process started, the time of the first not yet reported, and the largest total a delivered record has carried. A failed emit adds one. A record of that tenant carries `lost = {"process": <16 hex digits>, "total": n, "since": ms}` in the product object ([0004](0004-the-record-format.md)) while the total is larger than the reported one, and stops once a record that carried the current total has been delivered. `process` is random and belongs to one emitter for its life. `since` is the emitter's clock, at or before the first loss not yet reported.
- **A reader takes the highest `total` per component, tenant and process, and never sums.** A record that reached the spool while its caller timed out is repeated by the next record with the same or a larger total, so the log can hold 5 and then 6 about the same losses. Across a restart a new `process` starts at zero.
- **A record carries only its own tenant's note.** The loss of a fact for tenant A is reported in A's next record, sealed under A's key, and in no other. The emitter keeps at most 256 tenants. Past that a loss is counted in the operator's own tenant, and one warning says so. Tenants come from server state, not from a client's input, so nobody can fill the slots on purpose.
- **What the total says.** It counts emits that failed. A call that was cut off after the spool took it is in the log and in the total, so the total can exceed the facts missing by those. It names no action and no person.
- **The note is on the record and not a record of its own.** A gap record would be one more emit into the same full spool. When the note would make a record larger than the bound, the record goes without it and the note stays owed.
- **A fact can also be lost** when the emitter cannot build it: a refusal of the event (`schema`, `bound`) or a pseudonym that the seal block did not make, with a healthy spool.
- **Visible outside the log.** When a record that carries a note is delivered, the emitter logs a warning with the process, the total and the time of the first, naming no tenant. The emitter exposes the total over all tenants for a component to export.
- **The limits, stated plainly.** A process that exits, crashes or restarts while it holds an unreported total takes it with it. A quiet component after a loss reports nothing, and then the callers' journal lines are the only sign. This is accepted: a durable counter would have to be written to the disk that the emit just failed to write to.

### Every read of the log is audited

Every search, get, tail, export and reversal of a pseudonym is itself a record, with the rule that granted it ([0013](0013-access.md)).

### Built and planned

Built: the three calls, the note and its tests (in sluis, where the field is `unmapped.sluis.lost`). Planned: indexing of the note for the reader, so an auditor need not scan; the neutral name of the field for other products; an endpoint for the exported total.

## Consequences

- A full spool stops the operations that must not happen unaudited, and no others. A revocation never waits.
- An auditor sees how much was lost, per tenant, but not which facts.
- The cost of the first call kind is an uncounted gap in the case of a crash while a total is held. The second kind has no such gap, at the price of an operation that fails when the audit path does.
- One product's note never reveals another tenant's activity.

## Roads not taken

- **A blocking emit for every fact.** A full spool would block a revocation, and a refusal of a caller would answer on the health of the log.
- **A durable counter of lost facts, in a file.** It would be written to the disk that has just refused the emit, and it adds state to every emitter for a case that is loud already.
- **A gap record instead of a note on the record.** One more emit into the full spool and a second write at recovery, to say what the next record says already.
- **One count for the whole emitter.** It showed the loss of tenant A in tenant B's record, and a record that reached the spool while its caller timed out was counted again.
- **Failing the operation when a best-effort fact cannot be delivered, or keeping it in memory until the spool is back.** Rejected in [0002](0002-pieces-and-holders.md).
