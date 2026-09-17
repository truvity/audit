# Verification

```
audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
  --bucket <name> --public-key digest-signing.pem [--prefix <p>] [--json]
```

A date, an hour (`2026-09-17T10`) or a full timestamp are all accepted. The
command needs the archive and the public key and nothing else: run it with
read-only credentials and your own copy of the binary, because an answer that
depended on the operator of the archive would not be worth having.

Walks the digest chain newest-first, then for each digest:

1. Verifies the signature against the public key.
2. Confirms the previous digest exists and its hash and signature match
   what this digest names.
3. Fetches each listed object, computes its SHA-256, compares.
4. Reads each object's retention and confirms it is not shorter than the
   profile requires.
5. Confirms that no object in the range is unaccounted for. This is the half a
   chain alone does not cover: the chain shows that what it names is unaltered,
   and this shows that nothing was added beside it.

Output: one line per digest and per object, `valid` or `INVALID: <reason>`,
and a summary. Exit code non-zero on any invalid entry.

Auditors run it with read-only credentials scoped to the prefixes. The
nightly run inside the cluster does the same for the previous day and
records the outcome as `audit.digest.verified` or `audit.digest.failed`.
