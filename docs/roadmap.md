# Roadmap

Work that is wanted but not started. Each entry is a note, not a commitment:
nothing here is built, and none of it changes what the pages elsewhere
describe.

## To do

### Cloudflare Workers for audit-notary and audit-ingest (R2)

- **Notary as a Cron Worker.** It lists an hour's prefix, reads each record's
  sha256 from the object's custom metadata (no GETs), builds the hash tree,
  signs it with ECDSA P-384 and writes the digest object.
- **Ingest as a Worker.** It validates against the catalogue and writes to R2
  or to a queue, with the same Sink semantics as today.
- **Open question.** Go compiled to Wasm, or a small TypeScript port. A size
  prototype decides.
- **Depends on** the bucket-layout spec (ingest-time ordered keys, sha256 in
  object metadata), the Sink interface, and the declared retention grade
  (R2 gives governance).

### Cloudflare Queues as an ingest transport and the R2 change feed

- A `cfqueue` Sink that returns Queued.
- R2 event notifications into Queues as observe's wake-up hint, pulled over
  HTTP. Listing with a durable cursor stays the source of truth.
