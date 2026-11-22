// Package emitter is the audit emitter (record 0002, "Five pieces"): a library inside each emitting process. It holds no
// credential, and from one call it builds one message, an OCSF 1.9.0 security record as RFC 8785 canonical JSON (record
// 0004, "The record format"), checked against the product's action table, and hands it to atoom's transport port.
//
// The emitter speaks audit's neutral model (record 0003): a product, its components, a scope and a tenant. A product
// configures it with its own names (Config), its action table (Table) and, for records that name a person, a Mac of the
// pseudonym purpose (Pseudonyms). The product's own facts go into one object, unmapped.<product> (Event.ProductObject).
//
// A person is never named in a record. The emitter asks the Mac under the tenant's pseudonym key before the record leaves
// the process (record 0011, "Pseudonyms"), so a Subject becomes "p1.<YYYYMM>.<first 128 bits of the MAC>", over the
// length-prefixed scope, tenant, kind and identifier, with the label of Pseudonyms.
//
// Emit hands the message over and returns; EmitBlocking returns only when the transport says the message is durable
// (record 0002, "The transport"). Delivery is at least once, and the writer removes a repeat by metadata.uid. The library
// keeps no queue and does not retry: the transport driver behind the socket is the durable buffer, and a failure is
// returned to the caller, who logs it.
//
// A best-effort fact that cannot be delivered (Emit, or EmitBlockingBestEffort) is lost, and the emitter counts it in
// memory, for the tenant of the event. A record of that tenant carries the running total of the process as
// unmapped.<product>.lost ({process, total, since}) until a record that carries the current total has been delivered, so
// a repeat is harmless and a reader takes the highest total of a process and never sums (record 0005, "Loss is counted
// while the process lives"). The note is not a queue and nothing is resent; a process that exits with an unreported total
// takes it with it. EmitRequired is Emit for a caller that fails its operation on an error: that fact is not lost and is
// not counted.
//
// The Rust crate audit-emitter of the same directory produces the same bytes from the same input, and the shared vectors
// of vectors/ keep the two in step.
package emitter
