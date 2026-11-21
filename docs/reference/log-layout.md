# Log layout and checkpoints

The decisions are in [0008](../decisions/0008-the-log.md) and [0009](../decisions/0009-witnesses-and-verification.md). A log is a C2SP [`tlog-tiles`](https://c2sp.org/tlog-tiles) log. Its entry bundles, its hash tiles and its `checkpoint` object are the standard ones, written and read by libraries. Three parts are audit's own: the layout of the prefix, `checkpoints/<size>`, and the pointers `state/writer-state` and `quarantine/<sha384>`.

## The bucket of a project

One bucket for each project, one prefix for each log kind.

| Prefix | Holds | Written by |
|---|---|---|
| `audit/<log>/` | The security log | The project's writer |
| `usage/<log>/` | The usage log | The project's writer |
| `billing/` | The product's billing log, if it keeps one there | The product. No audit piece writes it. |
| `state/writer-state` | The writer's state: a pointer, replaced by a conditional put | The writer |
| `state/seen/<hour>-<digest>` | Write-once segments of remembered record ids ([writer state](writer-state.md)) | The writer |
| `quarantine/<sha384>` | A message the writer refused, kept whole, sealed under the evidence key of its tenant | The writer |

`<log>` names one writer instance's log: a cluster keeps one log per node.

## Inside a log prefix

The prefix is `<kind>/<log>/`. Below it are the paths of `tlog-tiles`, and audit's `checkpoints/`.

| Object | Holds | Written |
|---|---|---|
| Entry bundles | The entries, in bundles of 256 | Once, create-if-absent. A partial bundle is also created once, under its own name. |
| Hash tiles | The tree hashes by level | Once, create-if-absent |
| `checkpoint` | The newest checkpoint. A convenience pointer. | Replaced by a conditional put where the store supports it |
| `checkpoints/<size>` | Every checkpoint the writer ever signed, by tree size | Once, create-if-absent |

The origin line of a checkpoint is `<host>/<scope>/<project>/<kind>/<log>`.

## A checkpoint

A checkpoint is a C2SP [signed note](https://c2sp.org/signed-note) of signature type 0x02 (ECDSA over SHA-256, key curve P-256), whose text is a [tlog-checkpoint](https://c2sp.org/tlog-checkpoint): the origin line, the size and the root hash. The writer writes no extension line. A reader accepts the extension lines that the specification allows. A witness adds a [cosignature](https://c2sp.org/tlog-cosignature) line.

The key ID of a type 0x02 key is the first four octets of the SHA-256 of the DER `SubjectPublicKeyInfo`. The verifier key is `name+hex(key ID)+base64(0x02 || SubjectPublicKeyInfo)`.

## Checkpoint history

The `checkpoint` object is rewritten with every batch, so it shows only the newest state. `checkpoints/<size>` keeps every checkpoint, so that a verifier can check that the log only grew.

- **One object for each checkpoint, at `<prefix>checkpoints/<size>`.** `<size>` is the tree size of the checkpoint it holds.
- **The name is a size in the strict decimal form:** `0`, or a digit `1` to `9` followed by at most 18 digits, and at most 2^63 - 1. A sign, a leading zero, a space, a non-ASCII digit or any other character makes the name wrong.
- **The content is a signed note** signed with the log key, whose text is a tlog-checkpoint of the same log. The size in the text is the size in the name.
- **It is created once and never changed,** after the tiles and before the writer's state. A crash that leaves a checkpoint without its state is finished by the next start, which reads the checkpoint it finds, checks its signature and root, and uses it.
- **The `checkpoint` object is a convenience copy of the newest one.** A verifier does not trust it on its own: it is one more checkpoint of the history, and the largest size wins.

### What a reader checks

A reader refuses the log with a closed reason:

| Reason (suffix of the file's path) | Meaning |
|---|---|
| `.name` | The name is not a size in the strict form, or the entry is a directory |
| `.length` | The file is longer than the limit |
| `.signature` | The note does not open with the log key |
| `.format` | The text is not a tlog-checkpoint with a non-empty origin |
| `.size` | The size in the text is not the size in the name |
| `.origin` | The origin differs from the one in the `checkpoint` object, or from the one the auditor gave |
| `.fork` | Two checkpoints of one size name different roots |
| `.inconsistent` | The root of the checkpoint is not the tree hash of the first `<size>` entries, computed from the tiles |

An absent directory is an empty history. The tiles are checked against the newest checkpoint, and each older checkpoint against the tiles, so a history that does not lead to the newest root is refused. A repeated signature line of the log key is checked on every line, and the note is refused if any fails: the note library checks only the first.

**The same log always gives the same refusal.** A name that is not a size is reported first, the smallest in byte order if there are several. Then the files are read in ascending order of size, and the first that fails is reported.

### Limits

**The count of files has no limit.** The writer makes one checkpoint for each sealed batch for the life of the log. A busy log holds one every 5 seconds (17,280 a day) up to 51 messages a second, and more above that, up to the rate the log key can sign. A compliance lock forbids deleting any of them. A count limit would make a healthy log fail to verify one day, so the reader streams: it lists the directory in batches of 1,024 names, keeps the sizes (8 octets each) and reads one file at a time, in ascending order.

| Limit | Value | Why |
|---|---|---|
| Name | 19 octets | The digits of the largest size, 2^63 - 1 |
| File | 1,048,576 octets | A checkpoint is a few hundred octets |

The memory of a verification grows with the number of checkpoints (8 octets each) and with nothing else. Its time grows with it too, because each checkpoint is checked against the tiles.

### Tests

- **The format keeps its bytes.** A log written by an earlier writer is verified by the current reader, and a test builds a signed note with a fixed signature and compares it byte by byte.
- **An independent reader.** The reference implementation that signed-note names for type 0x02 (`github.com/transparency-dev/formats`) opens every checkpoint the writer makes and parses its text with its own parser.
- **Fuzz targets** for the checkpoint text, the name and a directory with one file of any name and content. Each asserts that nothing panics and that what is accepted is well formed.
- **A test for each refusal reason,** for the unbounded count (a history that needs more than two reads of the directory), for the order of refusals, and for the repeated signature line.

## Built and planned

Built: the layout, the checkpoint history and its reader, in sluis (with `blob/fs` from atoom). Planned: the layout on S3, the cosignature lines, the move of the tests here.
