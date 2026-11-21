# 0008. The log: tlog-tiles, a write-once fence, signed checkpoints, and a writer whose state lives in the blob

Status: accepted, 2026-11-21

## Context

The log is what the writer seals and the witnesses cosign. Six things shape it.

- **The formats are C2SP's:** `signed-note`, `tlog-checkpoint`, `tlog-tiles`, `tlog-cosignature` and `tlog-witness`. A followed format fixes its own algorithm.
- **Blob stores differ.** AWS S3 can lock objects and can create an object only if absent. Other stores lock differently or not at all. A local file system can create only if absent with the right system calls.
- **Each scope has one audit, and each emitting unit is a project of it** ([0002](0002-pieces-and-holders.md)).
- **A cluster has several nodes, and a log with two writers cannot keep one chain.**
- **Delivery is at least once.** The log must not hold a fact twice.
- **The numbers a design is sized for are facts with a date.** The design volume is about 2.4 records a second at peak and about 75,000 a week, with a ceiling of 51 messages a second held for 14 days for the writer's state.

audit-v1 wrote records to S3 with Object Lock and sealed each hour with a JWS over a Merkle root that a notary signed. That format was its own, and with no standard witness protocol. It is not carried over.

## Decision

### Tiled log

- **Each log has exactly one writer, and a cluster keeps one log per node.** Each node runs its own instance of each project's writer, with its own spool and log key. A single-node installation has one log of each kind per project. On Lambda, the project's one writer function writes one log per project. On Kubernetes there is one log per writer instance. The fence is the blob ([below](#the-write-once-fence)), not a lease.
- **A project's writer writes two kinds of log, the `audit` log and the `usage` log.** Every rule here holds for each log separately: its own prefix, checkpoints, retention, lock mode, tiers and readers.
- **Each log is a `tlog-tiles` log in its project's bucket.** Entry bundles and hash tiles are static objects that anyone with read access can fetch without a server. Tree hashes follow RFC 6962. A checkpoint is a `signed-note` in the `tlog-checkpoint` shape, signed with the log key, naming the log, its size and its root hash. Removing, reordering or altering an entry breaks every later checkpoint.
- **The signature is the standard one:** C2SP signature type 0x02 (ECDSA) over SHA-256, so a generic witness or checkpoint verifier checks a checkpoint without audit code. The log key is therefore P-256.
- **One prefix per log, `<kind>/<log>/` in the project's bucket,** with the `tlog-tiles` paths below it. The origin line of a checkpoint is `<host>/<scope>/<project>/<kind>/<log>`.
- **Every checkpoint is also written once to `checkpoints/<size>`** below the log's prefix. The `tlog-tiles` `checkpoint` object, the one object the format rewrites, is a convenience pointer, written with a conditional put where the store supports it. Verifiers trust the cosigned history and not the pointer ([reference/log-layout.md](../reference/log-layout.md)).
- **The log holds no catalogue and no schema.** Every record names its release ([0004](0004-the-record-format.md)).
- **One log key per writer instance signs its `audit` and `usage` logs,** and each signed checkpoint names its log. The writer asks the `seal` block to sign every checkpoint, and the block lets only that writer use its key. **A log key rotates on compromise only,** because a log's signatures must verify for the life of the log. A verifier accepts more than one key for a log, each with the checkpoint size from which it applies.

### The write-once fence

- **Every object is written once, and the blob store is the fence.** Entry bundles and tiles, partial tiles included, are created only if absent, so a second writer that believes it owns a log cannot overwrite or fork it. The blob port's `Create` is the fence, and its `Put` is for the two pointers only ([blob port](https://github.com/truvity/atoom/blob/master/blob/docs/blob-port.md)).
- **audit supports only blob backends with an atomic create-if-absent.** S3 and compatible stores use `PutObject` with `If-None-Match: *`. A local file system writes and fsyncs a temporary file, then links it to the target, which fails if the target exists, then fsyncs the directory. A backend that ignores conditional writes cannot hold a log.
- **The conformance suite of the blob port tests create-if-absent under concurrent writers for every driver.**
- **No credential carries a delete right on a project's bucket,** except an expiry sweeper's right on expired objects.

### Sealing

- **The writer receives messages from its project's stream, validates them, removes duplicates and seals them in batches.** It checks both parts against the project's catalogue ([0004](0004-the-record-format.md)), refuses an unknown action, and appends each part to the current batch of its kind. It stores the bytes it received, unchanged, after checking they are canonical. It stamps the time it recorded the entry and its own instance into the clear envelope of the entry, never into the record.
- **A batch is sealed at 256 messages or 5 seconds after its first, whichever comes first.** One seal seals the security batch together with the usage batch of the same messages. A batch that took no record is not sealed, so an idle log signs nothing. One exception: a batch that refused messages is sealed even when it took none, because the seal entry is the signed trace that the writer refused them. A batch of repeats alone is not sealed.
- **Each batch ends with a seal entry,** in the tree and in clear, with:
  - the batch's record count;
  - the count of messages it refused and, when there were some, how many for each reason;
  - the writer instance;
  - the observed clock offset;
  - whether the checks by the clock ran, and why not when they did not.

  It is an entry kind of its own, in RFC 8785 canonical JSON, because it carries no tenant data.
- **A batch is sealed when its checkpoint is signed and written.** The writer writes the entry bundles and tiles, asks the `seal` block to sign the new checkpoint, writes it, and, in one conditional write of its state in the project's bucket, registers the sealed batches of both log kinds and the ids it sealed. The transport is acknowledged only after the state and the checkpoint are durable. A crash leaves all of that or none, for both kinds at once, so the two logs never disagree about which messages were written.
- **The writer's state lives in the blob and never in a store.** It is one object, `state/writer-state`, a pointer that a conditional put replaces. It holds no transport cursor: the acknowledgement is the cursor.
- **The leaf of the tree is the hash of the whole entry, ciphertext included,** and the record's own RFC 8785 hash is inside the ciphertext, so a log verifies after its records stop opening ([0011](0011-evidence-encryption-and-pseudonyms.md)).

### Duplicates, quarantine and time

- **Delivery is at least once, and the writer removes duplicates by record id.** The id is a UUIDv7 (RFC 9562, lower-case text form) set by the emitter for the whole message. A message with another id is refused.
- **The writer remembers ids for 14 days,** the longest retention of a supported queue. The memory is the hot ids in the state and write-once segments below `state/seen/`, so that the state stays small whatever the traffic ([reference/writer-state.md](../reference/writer-state.md)).
- **A message is quarantined and reported** when:
  - its time, or its id's time, is more than 14 days behind the clock (`stale`);
  - it is more than an hour ahead of the clock (`future`);
  - its id's time is more than two minutes from its record's (`schema`, on any clock);
  - it fails validation, names a release with no catalogue, or an action the catalogue lacks.

  A quarantined message is kept whole, once, sealed under the evidence key of the tenant its record names, as the object `quarantine/<sha384>` in the project's bucket. Delivering it again keeps it once. No tool re-emits a quarantined message yet ([runbook](../runbooks/handle-quarantine.md)).
- **The time checks use the writer's clock only while it is trusted.** The clock is trusted when the kernel answers and does not say it is unsynchronised, and the wall clock has not stepped against a clock that does not step by more than an hour. On a clock that is not trusted the writer accepts and remembers: it neither refuses a message by its time nor forgets an id, and it does not stop, so a node that boots with a clock that is years wrong still audits. The seal entry says whether the checks ran and why not (`unobserved`, `unsynchronised` or `stepped`). The writer warns when the clock stops being trusted and when it is trusted again.
- **A refusal is acknowledged and never blocks the log.** A segment that the state names and that is missing or changed stops the writer with an error that names the segment. It is never taken as "not seen".

### Witness cosignatures

Witnesses are in [0009](0009-witnesses-and-verification.md).

### Built and planned

Built: the tiled log, the fence, sealing, the state with segments, quarantine, and the clock rules (in sluis, with the `fs` blob driver in atoom). Planned: the S3 blob driver, a tool that rebuilds a lost segment from the log (the entries carry their ids in the clear), a tool that re-emits a quarantined message, and time-stamp anchoring of checkpoints (RFC 3161). Anchoring proves when a hash existed and not that every reader saw one history, which witnesses do, so it stays a planned line.

## Consequences

- An auditor verifies a log with standard tools: fetch the tiles and checkpoints, check the signature against the published log key and the cosignatures against the witness keys, and recompute the tree. Verification needs no key at all and works after a tenant has left.
- Every object but the checkpoint pointer and the writer's state is written once, so a compliance lock and create-if-absent together leave no log object a writer can change. Each seal writes the checkpoint twice.
- A seal costs one signature per log kind that has entries: when busy, one every 5 seconds (17,280 a day) up to 51 messages a second. Above that, one for each 256 messages, up to the rate the key can sign. None when idle. Sealing a record is a `seal` call per record, batched.
- One log per node removes failover from the writer: a lost node leaves its log intact up to its last checkpoint. A project on three nodes has three logs to verify and merge, and events on different nodes within the clock offset may show in either order.
- The writer's state is small whatever the traffic. The memory of 14 days is also segments that the writer never deletes, which expire with the project's lifecycle. A segment that is lost or changed stops the writer until the object is restored.
- The seal entry is one more entry kind that the verifier, the indexer and the reader must know.
- A quarantined message is kept and not lost, but nothing releases it again today.

## Roads not taken

- **Keep audit-v1's hourly JWS seals over a Merkle root.** A format of its own, signed by a notary this design does not have, with no standard witness protocol.
- **A database as the log.** Easy to query, but a row can be changed by whoever administers it and nothing outside proves it was not.
- **The writer's state, or its fence, in a store.** The store would become a second home for evidence and a dependency of every seal. Only backends that can fence are supported.
- **Raise the limit of the blob driver, or encode the state of ids more compactly.** The object still grows with the traffic. A larger limit or a denser encoding moves the wall and does not remove it.
- **Keep only the newest ids, or a shorter window.** It breaks the 14-day guarantee.
- **A persisted floor that only grows.** A clock that jumped ahead would write a floor that the corrected clock never lowered, and every fresh record would be quarantined as stale until real time reached it.
- **Hold the writer until the clock is synchronised.** A node that cannot reach its time source would then have no audit at all.
- **Find a repeat in the log itself.** The id is in the clear envelope of every entry, but a lookup would need an index, or a scan at every start.
- **Load every segment at a start.** The id's time says which hour holds it, so a start needs none.
- **A signing proxy in front of the blob store.** A mediator with a master key on the trusted path.
- **Seal every second, or every minute.** Every second is up to 86,400 signatures a day per log. Every minute leaves a minute of lag and a larger unwitnessed window.
- **Overwrite the checkpoint and rely on versioning.** The exact `tlog-tiles` behaviour, but a local file system has no versioning, so the history would be lost and the fence broken.
- **A log per tenant, or one shared log per cluster with a leader as writer.** A writer and key per tenant and node with no home for a record that concerns two tenants, or a leader failover that can leave two writers and fork the log.
- **A separate writer for the usage log.** Two writers fed from one emit are a dual write that a crash can split.
