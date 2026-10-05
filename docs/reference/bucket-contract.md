# Bucket contract

The v1 layout of the archive: every prefix, the format of every object, the
metadata each carries and the seals that vouch for them. The three parts of
an installation share nothing else
([0016](../decisions/0016-three-parts-installed-independently.md)), so this
page is the whole of what a second implementation of any part must know.

The records, catalogue, seals and keys parts of this contract are built and
tested: the writer writes records and catalogues, the notary (`audit-notary`)
writes seals, `audit verify`, `audit reindex` and the scan searcher read them,
and a conformance suite holds all of it to the contract. Signing a delegation
is the one thing here that is specified and not built: a verifier checks one,
and nothing in this repository writes one. The
[capabilities](capabilities.md) page says what exists. The reasons are in
[0018](../decisions/0018-v1-bucket-layout.md) (the layout),
[0019](../decisions/0019-seals.md) (seals) and
[0020](../decisions/0020-observe-follows-the-bucket.md) (reading it).

## Layout

An installation owns one prefix of a bucket, written here as the bucket's
root. Times are UTC. `<profile>` and `<tenant>` are the profile's name and
the application's own tenant identifier, as keys, with no `/` in either.

| prefix | holds | written by | locked |
|---|---|---|---|
| `records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>` | one object per ingest batch | ingest | yes, the profile's retention |
| `catalogue/<app>/<version>` | the application's catalogue at that version | ingest | yes |
| `seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws` | one seal per profile, tenant and hour | notary | yes, as the records it covers |
| `keys/roots.jwks` | the trust anchors, as a JWK Set | the operator | no, versioned |
| `keys/delegations/<thumbprint>/<ULID>.jws` | delegations to a signing key | a root | no |
| `keys/revocations/<ULID>.jws` | revocations of a signing key | a root | no |

The profile is the first component of `records/` and `seals/` on purpose:
retention, lifecycle and replication are written against a prefix, and each
differs by profile.

## Records

**Key.** `<hh>` is the hour of the **ingest time**: the moment ingest took
the batch, not the time of any record in it. `<ULID>` is generated when the
put starts, from the same clock, so within an hour keys sort in the order
batches arrived. Two batches never share a ULID. A key whose ULID was made in
another hour than the key names is not a key of this contract.

**Body.** One batch is one object: newline-delimited JSON, zstd-compressed
(`Content-Encoding: zstd`), one record per line. Each line is

```json
{"hash": "<hex sha256>", "record": { ... }}
```

where `record` is the record as `audit.v1.Record` in its canonical JSON form
(RFC 8785 over the protobuf JSON mapping) and `hash` is the SHA-256 of that
canonical encoding, in lower-case hex. A writer embeds the canonical bytes as
they are; a reader checks a line by putting `record` in canonical form and
hashing that, so a line written with other whitespace still checks. A line is
never changed, merged or reordered, and the line numbers of an object, counted
from one, are how an index addresses a record.

`<profile>` and `<tenant>` are one key component each, so a record whose
tenant has a `/` in it cannot be written; the writer keeps it aside with the
reason. The platform's own tenant, `@platform`, is a valid component.

**Metadata.** Every object carries these user-defined keys (`x-amz-meta-`),
so that a reader needs a listing and a `HEAD` and never a body:

| key | value |
|---|---|
| `format` | `1` |
| `sha256` | hex SHA-256 of the object's stored bytes |
| `count` | the number of records in the object |

**Retention.** The object's lock is set on the put, from the profile, as
[0003](../decisions/0003-s3-object-lock-as-the-record.md) and
[0014](../decisions/0014-lock-modes-and-store-tiers.md) say.

## Catalogue

`catalogue/<app>/<version>` holds the catalogue document exactly as the
application registered it. It is written **once**, with a conditional put
(`If-None-Match: *`). A put that finds the key present compares the bytes: the
same bytes are success, different bytes for the same version are an error
and the writer refuses to run.

## Seals

A seal is a JWS in compact serialisation. Its protected header is

| field | value |
|---|---|
| `alg` | `ES384` |
| `typ` | `audit-seal+jws` |
| `kid` | the RFC 7638 thumbprint of the signing key |

and its payload is `audit.v1.Seal` in proto JSON with the proto field names:

| field | meaning |
|---|---|
| `tenant`, `profile` | what the seal covers |
| `hour` | the start of the hour covered, RFC 3339, UTC |
| `count` | the number of records in the seal's scope; zero for an empty hour |
| `root` | the root of a binary Merkle tree (RFC 6962 §2.1), encoded as hex; for an empty hour, the SHA-256 of the empty string |
| `first`, `last` | the first and last object key of the hour; empty for an empty hour |
| `prev` | hex SHA-256 of the previous seal's bytes for the same profile and tenant; empty for the first seal |
| `sealed_at` | when the seal was made, RFC 3339 |
| `meters` | optional: a map of counters for the hour (`objects`, `records`, `bytes`) that metering may read without a body; absent for an empty hour |

The payload is the proto JSON mapping, so a 64-bit integer (`count`, each
meter) is a decimal **string**, and every field is written, an empty `prev` as
`""`. The members are in canonical order (RFC 8785), but a verifier checks the
signature over the payload bytes as they are and never re-encodes them.
`sealed_at` is whole seconds and is never before the end of the hour the seal
covers.

A seal is put with a conditional put (`If-None-Match: *`) and locked as long as
the records it covers: its retention is the latest of the hour's objects, and for
an empty hour that of the seal before it. The notary reads the hour's objects
before it signs and refuses to seal an hour whose objects do not match their own
metadata (the sha256 of their bytes, their count, the hash of every record):
it vouches only for what it has checked, and seals nothing past an hour it
cannot. The chain therefore stops at a fault, and the age of the newest seal says
so.

A seal for an hour is written when the hour has settled
([0020](../decisions/0020-observe-follows-the-bucket.md)). An hour with no
objects still gets a seal, so a missing seal is a fault and never silence.

### Merkle tree construction

The tree is computed from all records in the hour, in key order by object
key (ULID), then line order within each object. Each record's `hash` field
is the hex-encoded SHA-256 of its record; it is decoded to its 32 raw bytes
before hashing.

**Leaf:** SHA-256(0x00 || leaf input), where leaf input is the 32 raw bytes of
a record's hash.

**Interior node:** SHA-256(0x01 || left || right), where left and right are
the 32-byte hashes of the child nodes.

**Tree shape:** Following RFC 6962 §2.1, the split point is the largest power
of two smaller than n (the number of leaves). The tree is unbalanced when n is
not a power of two; no leaf is duplicated.

**Empty hour:** When count is zero, the root is SHA-256(""), the hash of an
empty byte string.

The vectors, for the leaf inputs `input(i) = SHA-256("<i>")` (the ASCII decimal
`i`, not a record's hash: the vectors are about the tree), `i` from 0:

| n | root |
|---|---|
| 0 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| 1 | `13a77175e35eb1d9da91ee14df0d7772cea71289800206e2b45c882ecb06efbf` |
| 2 | `bbb441530bdded54e6e2bfcdc829819ff39b30768eb9f023071dffc16b410f10` |
| 3 | `8be871f13785b4c81a1700459c76ac2b3ae2caebb7876c376e223c6adff98c47` |
| 5 | `4e23fb40d8876f1299cca9b3c28432a01b11d1a20126606914612892ff2e09a7` |

The inputs and every leaf's audit path are in
[`internal/merkle/testdata/vectors.json`](../../internal/merkle/testdata/vectors.json),
and a test holds the code to them and to RFC 6962 written out as it is.

### Worked example

Two real seals of one tenant, synthetic data and a throwaway key, in
[`internal/seal/testdata`](../../internal/seal/testdata): the hour `10` holds two
objects and three records whose tree is the n = 3 vector above, and the hour
`11` is quiet and chained to it. `keys/roots.jwks`:

```json
{"keys": [{"kty": "EC", "crv": "P-384", "alg": "ES384", "use": "sig",
  "kid": "o2yB9qPIG1yB95LuYok20fSC80yOxCu7VF8rmi5LyGg",
  "x": "CGbXeVcyaJ3FX1fp6aFwTmOd2o4Az9onclXvRWez9zb1zHXkRIatexKJnvf6gVXn",
  "y": "25YgRZW8Wuk4VGnjnSBJpeLYg_5_0_1Ur3tSC3fBjK8h0Fc8wSDqtztpKL_rrWfC"}]}
```

`seals/security/acme/2026/09/17/10.jws`, header and payload decoded:

```json
{"alg": "ES384", "kid": "o2yB9qPIG1yB95LuYok20fSC80yOxCu7VF8rmi5LyGg", "typ": "audit-seal+jws"}
{"count":"3","first":"records/security/acme/2026/09/17/10/01M2QDGD6000000000033XR4JR",
 "hour":"2026-09-17T10:00:00Z","last":"records/security/acme/2026/09/17/10/01M2QEBW3000000000034XR5BY",
 "meters":{"bytes":"1536","objects":"2","records":"3"},"prev":"","profile":"security",
 "root":"8be871f13785b4c81a1700459c76ac2b3ae2caebb7876c376e223c6adff98c47",
 "sealed_at":"2026-09-17T11:17:04Z","tenant":"acme"}
```

and `.../11.jws`, whose `prev` is the SHA-256 of the whole compact serialisation
of the first:

```json
{"count":"0","first":"","hour":"2026-09-17T11:00:00Z","last":"","meters":{},
 "prev":"dc2b3969dd9edd1197a4bf6a8778189508a9104500a2709e14b20bb4c87f9326",
 "profile":"security","root":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
 "sealed_at":"2026-09-17T12:17:03Z","tenant":"acme"}
```

### Inclusion proof

A single record is proven by its leaf index (0-based position among all
records in the hour, in key order by object, then line order within each
object) plus the RFC 6962 audit path: at most ⌈log₂(count)⌉ hashes needed to
recompute the root. Its object can still be verified by that object's sha256
in the object metadata. The conformance suite carries test vectors for n = 0,
1, 2, 3 and 5.

### Trust

`keys/roots.jwks` is a JWK Set of P-384 public keys. It is distribution, not
trust: a verifier trusts only the roots whose thumbprints its own
configuration pins, and ignores any other key in the file or the bucket.

A seal is valid when its signature verifies under the key named by `kid`, and
either that key is a pinned root, or a **delegation** chains it to one. A
verifier computes a key's thumbprint from the key and never takes it from the
file's own `kid`.

### Delegation

A delegation is a JWS signed by a root (`typ: audit-delegation+jws`) whose
payload is `audit.v1.Delegation`:

| field | meaning |
|---|---|
| `iss` | thumbprint of the root |
| `sub` | thumbprint of the delegated key |
| `jwk` | the delegated public key |
| `nbf`, `exp` | the window in which it may sign, seconds since the epoch; `exp − nbf` is at most 25 hours |
| `scope` | `profiles` and `tenants`, each a list of names or `*` |

A seal signed by a delegate is valid only if `sealed_at` lies within the
window and the seal's profile and tenant are in scope. A delegation whose
window exceeds 25 hours is invalid. A key signs its own `sealed_at`, so a
verifier also takes the bucket's time of writing the seal as a witness, within
five minutes of skew: a seal written after the window closed is not valid
however it dates itself. Signing a delegation is not built; verifying one is.

### Revocation

A revocation is a JWS signed by a root (`typ: audit-revocation+jws`) whose
payload is `audit.v1.Revocation`: `iss` (the root), `revokes` (the revoked
key's thumbprint), `revoked_at` (RFC 3339) and an optional `reason`. A seal
signed by the revoked key with `sealed_at` at or after `revoked_at` is
invalid, and so is one the bucket says was written at or after it, within the
same skew. A root is revoked only by removing the pin. A delegation or a
revocation that no pinned root signed is ignored: it is only in the bucket.

## Reading the archive

A follower lists `records/<profile>/<tenant>/` starting after the last key it
handled and processes keys in order, never past a key younger than the
settle window. Tenants are found by listing `records/<profile>/` with a
delimiter. A listing is the source of truth; a notification is only a hint.

## Not in the contract

The dead-letter prefix, the verification marks and the index's own tables
are implementation details of one part and may change without a new version.
No part reads another's.

## Conformance

A conformance suite tests this contract: black-box, against any S3 API, it
writes batches and catalogues through an ingest, seals them through a notary
and reads the result through a follower, and checks every rule above,
including the refusals (a second catalogue put with other bytes, a delegation
of more than 25 hours, a seal signed by a revoked or unpinned key). A part
that passes it may be installed beside the others.

`internal/bucketcontract` is that suite, in two halves, each naming the rule
every finding breaks. The half for records and the catalogue holds a bucket to
the key grammar, the envelope, the metadata, the sha256, the hash of every
record, the uniqueness and order of keys and the presence of the catalogue a
record names; `audit verify` applies the same checker to each object. The half
for seals (`CheckSeals`) holds it to the seal key grammar, the JWS, the
payload, the signature against the roots a checker pins (and delegations and
revocations), the chain, the count and root recomputed from the objects as they
are now, the lock and the missing seals; `audit verify --root` applies it to
the seals of a range. The tests run both against the in-memory store and
against S3 (LocalStack in CI), with the notary signing once with a key on the
machine and once with an `ECC_NIST_P384` key in KMS, run the notary and check
what it wrote, check a seal by hand with the standard library and no code of
this repository's, and hand the checker each broken seal in turn: a key nobody
pinned, a broken chain, a root that is not the hour's, an object added to a
sealed hour, a seal made before its hour ended, a stray key, a delegation outside
its window or scope or of more than 25 hours, and a delegate whose key was
revoked. The Merkle vectors are in `internal/merkle`.
