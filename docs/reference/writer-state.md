# Writer state

The decision is in [0008](../decisions/0008-the-log.md). The writer keeps one object, `state/writer-state`, in the project's bucket. A conditional put replaces it. It registers the sealed batches of both log kinds and the ids the writer has sealed, so that a message the transport delivers again is dropped.

## The problem

One object that holds every id of 14 days cannot last. At the top rate of the design, 51 messages a second, 14 days hold 61.7 million ids. At about 66 bytes an id that is 4 GB in one object, and the blob port takes at most 4 MiB in one request. The volume the design is sized for (about 75,000 messages a week, so about 150,000 ids in 14 days) already passes the limit after about six days. A writer that cannot put its state stops, and every restart fails again on the same put.

## The state

| Part | Holds |
|---|---|
| Sealed batches | For each log kind, the batch that the latest checkpoint covers |
| Hot ids | The ids of the latest batches: at most 1,024, and the batch that crosses it (1,280 with a batch of 256). 39 bytes each in the JSON. When they pass 1,024, the writer spills all of them to segments. |
| Segment references | For each segment: the hour, the count and the digest, about 63 bytes each |
| `head` | The newest id time sealed on a trusted clock. It is evidence and not a ratchet: it can only make the writer forget less. |
| Format | `format` 2. A state of the first format (every id listed) is read once into the hot ids, and the next commit spills them. A writer of the first format refuses a state of format 2. |

## Segments

- **Where:** `state/seen/<hour>-<digest>`. The hour is one hour of UUIDv7 time. The digest is the first 128 bits of the SHA-256 of the segment's bytes, in hex.
- **Form:** sorted ids, 16 bytes each after an 8-byte header, at most 131,072 ids (2 MiB).
- **Created once.** The same ids give the same bytes and the same name, so a write repeated after a crash is the same write. An object that is there under the name with other bytes is not the writer's: the writer stops with an error that says so, and not with the fence's.
- **Merging.** A spill merges, first, every hour that holds more than 8 segments of fewer than 131,072 ids. This is never skipped, and it is what bounds the state. Then it merges the oldest hours that ended more than an hour ago and still hold more than one small segment, at most 4 hours in a spill. A full segment is not read again.
- **A check** loads the segments of the id's hour and no others: the hot ids first, then a binary search in each segment of the hour. A start loads no segment. The writer keeps up to 32 MiB of segments in memory, least recently used first.
- **The order:** the writer creates the segments, then puts the state once on its entity tag, then signs the checkpoint. A crash leaves the old state or the new one and, at worst, a segment that nothing names. The entries of a crashed batch are adopted at the next start, and their ids join the hot ids.
- **No id is lost or taken as new.** A segment that the state names and that is missing, or whose bytes are not the digest, stops the writer with a fatal error that names the segment. It is never counted as "not seen".
- **The writer has no delete right.** A segment that the state no longer names is an expired object, and it goes with the expiry that the project's lifecycle declares.
- **A damaged segment costs a stop,** not limited to its hour: the batch that needed it is not acknowledged and is delivered again, and a missing small segment also fails the merge of its hour. The ways back are a copy of the object (the bucket's backup, or its version history), or none. No tool rebuilds a segment from the log, whose entries carry their ids in the clear. That tool is a planned follow-up.

## The window and the clock

- **A check comes before the lookup.** A message is checked for form and time first, and looked up only if the checks take it, so a quarantined message never reads a segment.
- **The window is the writer's clock, and only while the clock is trusted.** On a trusted clock:
  - a message whose record time or id time is more than 14 days behind the clock is quarantined as `stale`;
  - one more than an hour ahead of it is quarantined as `future`;
  - a message whose id time is more than two minutes from its record's time is quarantined as `schema`, on any clock.
- **The clock is trusted** when the kernel answers and does not say it is unsynchronised, and the wall clock has not moved against the clock that does not step by more than an hour since the writer took it as right. A step that holds for 30 minutes with the kernel calling the clock synchronised becomes the new anchor, so a clock set by time synchronisation or by an operator is trusted again without a restart.
- **The writer drops an id or a segment only on a trusted clock,** and then only when the cutoff, the clock less 14 days but never later than `head` less 14 days and an hour, has passed it.
- **On a clock that is not trusted the writer accepts and remembers.** It neither refuses a message by its time nor forgets an id, and it does not stop. The seal entry of every batch says whether the checks by the clock ran: `time_checks` is `{"run": true}`, or `{"run": false, "why": ...}` with `unobserved` (the kernel did not answer), `unsynchronised` or `stepped`. The writer logs a warning when the clock stops being trusted, every 10 minutes while it lasts, and when it is trusted again.
- **What this does not cover.** A clock that is wrong and reported synchronised when the writer starts, and stays wrong, is taken as right: the expiry may drop the ids of up to the size of the error (at most 14 days), and a repeat in that window is written twice. A node with no time source never has a trusted clock: nothing is forgotten, so the 14-day window does not bound the state. It grows by about two references an hour at the top rate and reaches the 3 MiB the writer puts after about 2.8 years (5.5 at the sizing volume). The writer warns from half of that and again at every tenth more, and stops with `ErrStateTooLarge` at the limit. The remedy is a time source: once the clock is trusted, the next seal drops every hour older than the window and the state shrinks at once.

## The arithmetic

At the top rate of the design (51 messages a second, a batch of 256 every 5 seconds, 14 days):

| | One object of ids | State and segments |
|---|---|---|
| Ids in 14 days | 51 x 86,400 x 14 = 61,689,600 | the same |
| Bytes of ids | 61.7 million x 66 = 4.07 GB in one object. The limit holds 63,500 ids, which this rate fills in 21 minutes. | 61.7 million x 16 = 987 MB, in segments |
| The state object | 4.19 MB, then refused | at most 114,761 bytes for ids that arrive in time order on a trusted clock: 1,280 hot ids and 1,028 references. Typically 92 KB. |
| Bytes put per seal | 4.19 MB, which is 72 GB a day at 17,280 seals | at most 115 KB, which is 2.0 GB a day |
| Largest object | the state, over the limit | a segment of 2,097,160 bytes, half of the request limit |
| Where the 3 MiB is reached | 63,500 ids | 47,600 references: 18.6 million ids an hour, 100 times the top rate |

The writer does not put a state over 3 MiB. It stops with `ErrStateTooLarge`, naming the bytes, the hot ids and the segments, so a state that cannot fit is a clear stop and never a loop on a refused put.

The 114,761 bytes do not hold in three cases: ids out of time order (an hour may hold 8 small segments besides its full ones before it is merged, so the state is at most about 265 KB), a clock that is not trusted, and a rate above the top rate. The merges the bound needs are not capped, so one spill can rewrite up to 338 hours of 2 MiB (0.7 GB). Only the compaction of closed hours that no bound forces is capped. The recovery at 5,160 messages a second and more is a second level of segments, which this design does not build because nothing needs it.

Requests: a segment is one create and, when it is not in memory, one read. At the top rate that is about 180 creates an hour, about 4% more requests than the seals already make. On S3 a create is a `PutObject` with `If-None-Match: *` and a read a `GetObject`. The `fs` driver takes a temporary file, an fsync and a `link()`.

The state size is measured with the real JSON of the writer at that bound, and the bound on the references of an hour by a run of the real merges at a hundredth of the scale. The bytes put in segments come from a simulation of one hour at this rate and are **estimates** until an installation runs at it.

## Built

All of this is built in the writer in sluis. The segment-rebuild tool is planned.
