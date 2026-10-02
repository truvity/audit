# Bucket contract

The v1 layout of the archive: every prefix, the format of every object, the
metadata each carries and the seals that vouch for them. The three parts of
an installation share nothing else
([0016](../decisions/0016-three-parts-installed-independently.md)), so this
page is the whole of what a second implementation of any part must know.

This is a specification of a design that is not yet built; the
[capabilities](../capabilities.md) page says what exists. The reasons are in
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
batches arrived. Two batches never share a ULID.

**Body.** One batch is one object: newline-delimited JSON, zstd-compressed
(`Content-Encoding: zstd`), one record per line. Each line is

```json
{"hash": "<hex sha256>", "record": { ... }}
```

where `record` is the record as `audit.v1.Record` in its canonical JSON form
and `hash` is the SHA-256 of that canonical encoding. A line is never
changed, merged or reordered.

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
| `count` | the number of objects the seal covers; zero for an empty hour |
| `root` | the root of a binary Merkle tree whose leaves are the objects' `sha256` values in key order, hashed as in RFC 6962; the hash of nothing for an empty hour |
| `first`, `last` | the first and last object key of the hour; empty for an empty hour |
| `prev` | hex SHA-256 of the previous seal's bytes for the same profile and tenant; empty for the first seal |
| `sealed_at` | when the seal was made, RFC 3339 |
| `meters` | optional: a map of counters for the hour (objects, records, bytes) that metering may read without a body |

A seal for an hour is written when the hour has settled
([0020](../decisions/0020-observe-follows-the-bucket.md)). An hour with no
objects still gets a seal, so a missing seal is a fault and never silence.

### Trust

`keys/roots.jwks` is a JWK Set of P-384 public keys. It is distribution, not
trust: a verifier trusts only the roots whose thumbprints its own
configuration pins, and ignores any other key in the file or the bucket.

A seal is valid when its signature verifies under the key named by `kid`, and
either that key is a pinned root, or a **delegation** chains it to one.

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
window exceeds 25 hours is invalid.

### Revocation

A revocation is a JWS signed by a root (`typ: audit-revocation+jws`) whose
payload is `audit.v1.Revocation`: `iss` (the root), `revokes` (the revoked
key's thumbprint), `revoked_at` (RFC 3339) and an optional `reason`. A seal
signed by the revoked key with `sealed_at` at or after `revoked_at` is
invalid. A root is revoked only by removing the pin.

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

A conformance suite will test this contract: black-box, against any S3 API,
it writes batches and catalogues through an ingest, seals them through a
notary and reads the result through a follower, and checks every rule above,
including the refusals (a second catalogue put with other bytes, a delegation
of more than 25 hours, a seal signed by a revoked or unpinned key). A part
that passes it may be installed beside the others. The suite is
[designed, not built](../capabilities.md).
