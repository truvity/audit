# Record format

The decisions are in [0004](../decisions/0004-the-record-format.md) and [0011](../decisions/0011-evidence-encryption-and-pseudonyms.md). This page lists the fields and the rules.

## A security record

A security record is an OCSF event of a core class of the pinned version, written as RFC 8785 canonical JSON. The fields every record has:

| Field | Holds | Set by |
|---|---|---|
| `class_uid`, `activity_id`, `type_uid` | The OCSF class and activity | The action table |
| `metadata.version` | The pinned OCSF version, such as `1.9.0` | The release |
| `metadata.product.name` | The name of the product. It is also the name of the product object. | The release |
| `metadata.product.version` | The product's release, a semantic version | The release |
| `metadata.event_code` | The action, one code per action | The action table |
| `metadata.uid` | The record id, a UUIDv7 in lower-case text form | The emitter |
| `metadata.tenant_uid` | The tenant | The emitter |
| `metadata.log_name` | The scope of the log. The writer refuses a record that names another. | The emitter, checked by the writer |
| `metadata.correlation_uid` | The W3C trace id, when there is one | The emitter |
| `metadata.profiles` | Every OCSF profile whose attributes the record uses | The action table |
| `time` | The time of the event, in integer milliseconds | The emitter |
| `status_id`, `status_code` | The outcome and its closed reason | The component |
| `actor.user.uid`, `actor.user.type_id` | The emitting component and its kind (1 user, 2 operator, 3 component, 4 workload or client) | The emitter |
| `unmapped.<product>` | The product's own facts, in one closed object | The action table |

- **Pseudonyms.** A person is `p1.<YYYYMM>.<value>`, never a name. See [pseudonyms](#pseudonyms).
- **The project and the log are not fields.** They belong to the log.
- **Lean.** Enum ids carry no captions, except `activity_name` for activity 99 or where the table names one. There are no `observables`, enrichments or `raw_data`. The only `unmapped` key is the product's name.
- **Class attributes.** The attributes that a class requires, and the constants used where nothing is known, are in [0004](../decisions/0004-the-record-format.md#the-attributes-a-class-requires).

## The emitter's field

The emitter adds `lost` to the product object of any record: `{"process": <16 hex digits>, "total": <integer>, "since": <ms>}`. It carries the running total of best-effort facts that one process failed to deliver for the record's tenant ([0005](../decisions/0005-the-security-class.md)). The writer refuses it in an event from a caller. A reader takes the highest `total` per component, tenant and process, and never sums.

## Canonical form and bounds

| Rule | Value |
|---|---|
| Serialisation | RFC 8785 (JSON Canonicalization Scheme). The same bytes travel end to end. |
| Strings | UTF-8, never normalised by the writer |
| Integers | Within plus or minus (2^53 - 1). A value that can exceed the bound is a string. |
| Floating point | Never. The emitter and the writer refuse a number that is not an integer. |
| Times | Integer milliseconds |
| Duplicate keys | Refused |
| Free text | Bounded. The value types of [the action table](action-table-and-catalogue.md#value-types) give each field its bound. |
| Record size | At most 16 KiB. The largest real record is about half of it. |
| Check | The writer parses the record, canonicalises it again and refuses it if the bytes differ. It then stores the bytes it received. A verifier hashes the stored bytes and never re-serialises. |
| Hash | SHA-384 over the canonical bytes. The record's own hash is kept inside the sealed part of its entry. |

What a record never carries: a secret, an e-mail address, a name, a phone number, a token, an IP address, a path, a query, a header or a parameter value.

## The entry in a log

A log entry is an envelope in clear plus the sealed record.

| Part | Holds |
|---|---|
| Envelope | The id (`metadata.uid`), the time, the tenant, `class_uid`, `activity_id`, `status_id`, `actor.user.type_id`, and the writer's stamps: the time it recorded the entry and its own instance |
| Sealed record | The canonical record, sealed under the tenant's `evidence` key, bound to the scope, the tenant, the project, the log and the record id |

The envelope leaves out `metadata.event_code`. Apart from the stamps, it is a projection of the record that the writer computes before sealing. A component that opens an entry checks that the two agree. A usage entry has the same form with the event `type` in clear.

The leaf of the tree is the hash of the whole entry, ciphertext included. **Open: the byte layout of an entry** (envelope, nonce, ciphertext) is defined by the writer's code in sluis today. It is written down here when the writer moves.

A **seal entry** ends each batch. It is in clear and is not a tenant's record: the record count, the count of refused messages, and for each reason how many (`refused_by`), the writer instance, the observed clock offset, and `time_checks`: `{"run": true}`, or `{"run": false, "why": "unobserved" | "unsynchronised" | "stepped"}`.

## Pseudonyms

`p1.<epoch>.<value>`:

- `<epoch>` is the UTC month as `YYYYMM`.
- `<value>` is the first 128 bits, in base64url without padding, of `Mac` with HMAC-SHA384 under the tenant's `pseudonym` key of that epoch.
- The input is the domain-separation label, the scope, the tenant, the kind of identity and its stable identifier, each part prefixed with its length as a 4-byte big-endian integer.
- `unknown` is not a pseudonym. A record that established no person says `uid` `unknown` and `type_id` 0.

## Identifiers

| Id | Form |
|---|---|
| Record id, `metadata.uid` | UUIDv7 (RFC 9562), lower-case text. Set once by the emitter for the whole message. |
| Usage event id | `<metadata.uid>/<n>`, `n` from 0 |
| Process id in `lost` | 16 lower-case hex digits, random for the life of one emitter |
| Quarantined object | `quarantine/<sha384 of the message, lower-case hex>` |

## Versions

A log holds records of several releases and several OCSF versions. Readers handle each by the version it names. Within a major version of a product, a field of its object is only added, never removed or retyped.
