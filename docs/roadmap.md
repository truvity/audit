# Roadmap

Work that is wanted but not started. Each entry is a note, not a commitment:
nothing here is built, and none of it changes what the pages elsewhere
describe.

R2 is a supported archive store, with governance-grade retention.

## To do

### audit-notary as a Cloudflare Cron Worker over R2

- It lists an hour's prefix, reads each record's sha256 from the object's
  custom metadata (no GETs), builds the hash tree, signs it with ECDSA P-384
  and writes the digest object.
- **Open question.** Go compiled to Wasm, or a small TypeScript port. A size
  prototype decides.
- **Depends on** the bucket-layout spec (ingest-time ordered keys, sha256 in
  object metadata), and the declared retention grade (R2 gives governance).

### Cloudflare Queues as an ingest transport and the R2 change feed

- A `cfqueue` Sink that returns Queued.
- R2 event notifications into Queues as observe's wake-up hint, pulled over
  HTTP. Listing with a durable cursor stays the source of truth.
