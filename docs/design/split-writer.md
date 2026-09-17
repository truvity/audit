# Split writer

The single trusted consumer of the wide stream. Runs as a service with a
durable pull consumer, or embedded in an application that has no stream.

## Per record

1. **Dedupe** by `id` against a table with a configurable window (preset
   `pipeline.dedupe_window_days`).
2. **Resolve** the catalogue by `source` and `catalogue_version`. Unknown
   version: dead-letter, alert, never drop.
3. **Validate** against the composed schema. Violation: dead-letter.
4. **Stamp** `recorded_at`, `observer` from the publisher's verified
   identity, `origin_hash` as SHA-256 of the canonical wide record.
5. **Split**: for each profile the action belongs to, build a copy with the
   profile's allowed fields and classes.
6. **Treat identities** per profile: clear, pseudonym (HMAC with the
   tenant-and-purpose key), scoped, or omit. Apply `x-audit-sensitive`.
7. **Detach payloads** above the size threshold to the payload prefix by
   content hash; leave the hash in the copy.
8. **Buffer** per profile and tenant. Roll on interval (one to five
   minutes) or size. Split at day boundaries.
9. **PUT** each rolled object with `ObjectLockMode=COMPLIANCE` and
   `RetainUntilDate` from the profile's retention, SSE-KMS, a checksum, and
   `Content-Encoding: zstd`.
10. **Copy schemas** on first use of a catalogue version to the schema
    prefix, locked for the longest profile the catalogue's actions belong
    to. On first use of a record major, copy the record's JSON Schema and
    its proto there too: the archive keeps the meaning of every field, not
    only its shape.
11. **Index**: insert facet rows and update the counts table.
12. **Ack** the stream message only after the PUT and the index succeed.

## Idempotency

Object keys are deterministic per writer instance and window. Index inserts
are keyed by `(profile, id)`. A crash between PUT and index leaves an
object without rows; the nightly reindex of the day repairs it.

## Failure

- Object storage unavailable: the buffer holds until the stream horizon,
  then the writer stops consuming and alerts; the stream's discard-new
  policy surfaces the stall to emitters as publish failures.
- Postgres unavailable: PUT proceeds, index is deferred to reindex, ack is
  withheld until a configurable grace, then dead-letter.
- Writer restart: unacked messages are redelivered; dedupe absorbs them.

## Meta-events

`audit.writer.started`, `audit.writer.stopped`,
`audit.writer.dead_lettered`, `audit.catalogue.registered`.

## Embedded mode

The same library inside an application, with the in-process transport.
Many writers may exist; the digest job is separate and lists the prefix.
Dedupe is best-effort at the emitter; the outbox mode covers restarts.
