# `audit verify`

The decisions are in [0009](../decisions/0009-witnesses-and-verification.md) and [0014](../decisions/0014-the-front-the-console-page-and-audit-verify.md). `audit verify` checks a log from the log store alone, with no front, reader or sign-in, and with read-only credentials. It needs no key to read: it needs the keys that the auditor pins.

**What is built.** Today the command is `sluis audit verify`, in sluis's CLI, and it checks a log in a local directory with one log key. The rest of this page is the target. A row marked *planned* is not built yet. The command moves here and is renamed.

## Use

```
audit verify [--key VERIFIER_KEY | --key-file FILE] [--origin ORIGIN] DIRECTORY
```

| Argument | Meaning |
|---|---|
| `DIRECTORY` | The log's directory: the prefix `<kind>/<log>/` of the project's bucket, as files. *Planned:* a bucket and a prefix. |
| `--key`, `--key-file` | The verifier key of the log, as a C2SP signed-note verifier key: `name+hex(key ID)+base64(0x02 \|\| SubjectPublicKeyInfo)`. The key must be a P-256 key of signature type 0x02. *Planned:* more than one key, each with the checkpoint size from which it applies. |
| `--origin` | The origin line that the checkpoints must name. Any when empty. |
| `--witness` | *Planned:* a witness key to pin, and the number of cosignatures required |

The output is one line: the origin, the entries, the root and the number of checkpoints. The same result is available as data. The exit code is not zero when a check fails.

## What it checks

1. **The key** parses, is P-256, has signature type 0x02, and its key ID matches the key.
2. **The history** of `checkpoints/<size>`, with the closed reasons of [log layout](log-layout.md#what-a-reader-checks).
3. **The newest checkpoint** is the largest size of the history, and the `checkpoint` object is one more checkpoint of it.
4. **The tree.** The tiles give the tree hash of the first `<size>` entries for each checkpoint, and each checkpoint is consistent with the next.
5. **The signatures.** Every line of the log key on a note is checked, not only the first.
6. **The cosignatures** (*planned*): each is checked against a pinned witness key, and the number required is met.

It never reads the search index, the writer's state or the store.

## What it does not do

- It does not open a record. A record opens only for a caller that the `seal` block lets open it ([0011](../decisions/0011-evidence-encryption-and-pseudonyms.md)). A log verifies after its records stop opening.
- It does not trust a key because it is in the bucket. The auditor gives the key.
- It does not claim that an installation meets a standard.

## Related commands

| Command | State | What it does |
|---|---|---|
| `audit read` | planned | Reads and opens records from a bucket, for a caller that the `seal` block allows |
| `audit search` | planned | Builds a local SQLite index from the log and searches it |
| `audit reindex` | planned | Rebuilds the index of the scope or of some of its partitions |

## Errors

A usage error names the argument. A refusal of a log names a closed reason and the path of the file at fault, and never a value of a record. The same log always gives the same refusal.
