# Split writer

The single trusted consumer of the wide stream. Runs as a service with a
durable pull consumer, or embedded in an application that has no stream.

Every replica shares one durable consumer, which is what makes a second replica
a second pair of hands rather than a second copy of every record. The stream
itself is the deployment's to create and the writer refuses to start without it:
its retention and discard policy decide whether a full stream refuses publishers
or drops records, and that is not a choice this component should make quietly.
The acknowledgement wait must exceed the longest a write can honestly take,
since a batch is acknowledged only once its records are in the archive; set it
too short and the stream offers the same records to a second replica while the
first is still writing them.

## Per record

1. **Ask** the deduplication table whether the `id` has been written, within a
   configurable window (preset `pipeline.dedupe_window_days`). Asking marks
   nothing; see below.
2. **Resolve** the catalogue by `source` and `catalogue_version`. Unknown
   version: dead-letter, alert, never drop.
3. **Validate** against the composed schema. Violation: dead-letter.
4. **Stamp** `recorded_at`, `observer` from the publisher's verified
   identity, `origin_hash` as SHA-256 of the canonical wide record.
5. **Split**: for each profile the action belongs to, build a copy with the
   profile's allowed fields and classes.
6. **Treat identities** per profile: clear, pseudonym (HMAC with the
   tenant-and-purpose key), scoped, or omit. Apply `x-audit-sensitive`.
8. **Buffer** per profile, tenant and day. Roll on interval (one to five
   minutes) or size, measured before compression. An object's retention is
   fixed when it is opened rather than when it is written, so every copy in
   it is kept at least as long as the profile asks of the oldest.
9. **PUT** each rolled object with `ObjectLockMode=COMPLIANCE` and
   `RetainUntilDate` from the profile's retention, SSE-KMS, a checksum, and
   `Content-Encoding: zstd`.
10. **Copy schemas** on first use of a catalogue version to the schema
    prefix, locked for the longest profile the catalogue's actions belong
    to. On first use of a record major, copy the record's JSON Schema and
    its proto there too: the archive keeps the meaning of every field, not
    only its shape.
11. **Index** the object's rows, which moves the facet counts for the rows the
    insert actually created. A failure here does not fail the write: the index
    is a projection and `audit reindex` rebuilds it from the objects. The
    deployment is told, because an index nobody notices is behind is one that
    quietly answers wrongly.
12. **Mark** the identifiers as written, now that the copies are durable.
13. **Ack** the stream message only after the PUT.

## Asking and marking are two calls

The order is the interesting part, and it is the opposite of what reads best.

Claiming an identifier before writing it is the natural shape, and it loses
records. With deduplication in one process a crash takes the table with it, so
the redelivery is accepted and nothing is lost. With a shared table the mark
survives the crash: a writer that claims an identifier and dies before its PUT
leaves the record nowhere, and the redelivery that would have saved it arrives
looking like a duplicate. That is a silent hole in an audit trail, produced by
the component whose job is to have none.

Marking afterwards can only fail the other way. A crash between the PUT and the
mark means a redelivery is written again, and the archive holds a second copy:
the index keeps one row per identifier, the digest chain accounts for both
objects, and a reader sees the record once. A duplicate costs an object. A loss
cannot be repaired at all.

Because nothing is marked until the batch is durable, a batch carrying a
redelivery beside its original is not settled by asking. The writer keeps its
own account within the batch, and the second copy is absorbed there.

## Payloads are not detached, and why

An earlier design stored a body above a threshold once under `payload/sha256=…`
and referenced it from each copy, so that several copies would not each carry
it. With the presets this repository ships there is nothing to duplicate:
`capture` is kept by the security profile alone, and billing and history forbid
it. The emitter already drops a body over its bound and caps the whole record,
so object size is bounded without a payload prefix.

The cost would not be small. A payload referenced later by a longer-lived
profile would need its lock extended, and a writer cannot know its future
referrers, so every payload would be locked for the longest profile: seven
years on a request body kept for a copy that lives one.

This comes back when a deployment keeps `capture` in more than one profile, or
carries a large shared-class property that every copy gets. Until then it is
machinery for a case that does not exist.

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

They are records like any other, so the writer holds an emitter bound to the
common catalogue whose sink is the writer itself, over the in-process
transport. That is a loop by construction and it is the right one: the
writer's own account of itself lands in the same archive under the same rules,
and there is no second path to keep honest. Two rules keep the loop safe. The
emitter uses best-effort delivery, because a block write from inside the
writer's own batch would wait on itself. And a dead letter caused by one of
these records is dead-lettered and logged, never emitted about, or one bad
meta-record would beget another.

## Replay

A dead letter carries the reason and the full record. Once the cause is fixed,
`audit replay --dlq --from --to` reads the dead letters of a range and hands the
records back to the writer as a batch. Deduplication makes a replay of
something that did get through harmless. Without a sink it reads and groups the
reasons and sends nothing, which is how an operator decides what to replay;
`--reason` and `--action` then narrow it to the cause that was fixed.

Replay reports what failed again by listing the prefix before and after. That
works because the writer dead-letters within the call that carried the record,
so once a blocking write returns, whatever it could not process is already
back. It is also the only way to tell: the writer *accepts* a record it
dead-letters, and is right to, because a record that can never become valid
must not be retried forever by every hop below.

## Embedded mode

The same library inside an application, with the in-process transport.
Many writers may exist; the digest job is separate and lists the prefix.
Dedupe is best-effort at the emitter; the outbox mode covers restarts.
