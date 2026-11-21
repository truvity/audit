# Reader API

The decision is in [0012](../decisions/0012-search-and-the-reader-api.md). The reader is the audit API. Its contract is Connect, with unary calls only, and a TypeScript client from connect-es. This page fixes the contract that the proto must express. **The proto is written with the reader's build,** and this page is its specification. The filter grammar is carried over from the search contract of [audit-v1](https://github.com/truvity/audit-v1/blob/master/docs/decisions/0006-search-contract.md), re-based on OCSF paths.

Every call:

- is authenticated by the reader itself;
- is decided by Cedar ([0013](../decisions/0013-access.md)), and the grant is a mandatory filter;
- is itself audited, with the rule that granted it;
- carries the deadline and the correlation id as every call of an atoom port does ([call context](https://github.com/truvity/atoom/blob/master/docs/call-context.md)).

## Calls

The names `Search`, `Facets`, `Get` and `Export` carry over from audit-v1. The names `Tail`, `Reverse` and `Health` are proposed.

| Call | What it does |
|---|---|
| `Search` | Records that match a filter, a page at a time |
| `Facets` | Counts over facetable fields, for the same filter |
| `Get` | One record, as its canonical bytes, with its inclusion proof |
| `Export` | An asynchronous job that writes NDJSON and ends in a short-lived signed URL |
| `Tail` | A repeated `Search` with the cursor |
| `Reverse` | The two questions of pseudonym reversal ([0011](../decisions/0011-evidence-encryption-and-pseudonyms.md)) |
| `Health` | The age of the latest checkpoint of each log, the witness cosignatures, the index lag |

## Search

A request has:

| Field | Meaning |
|---|---|
| `partitions` | The projects and log kinds to read. Mandatory. Only `audit` has an index. |
| `tenants` | The tenants to read. Mandatory. `*` alone means every tenant the grant allows. |
| `filter` | Up to four conjunctions, joined by OR. Each is a list of typed predicates. |
| `q` | Free text over the fields that the catalogue marks searchable |
| `sort` | Up to four fields and directions, limited to indexed columns. The default is time, newest first, then id. |
| `limit` | A suggestion. A page has at most 200 records. |
| `cursor` | Opaque |
| `view` | `full` or `envelope`, if the grant allows it |

The grant is intersected with `partitions`, `tenants`, the time window and the view. A request that names a partition or tenant outside the grant is refused, and a row of another partition or tenant is never returned.

### Predicates

A predicate has a field, an operator and a value, discriminated on the operator. The operators depend on the type of the field and are fixed:

| Field type | Operators |
|---|---|
| String | equal, not equal, in, not in, prefix, is null, is not null |
| Time | between, and the four comparisons |
| Integer | equal, not equal, in, not in, between, and the four comparisons |
| Target | in, not in |
| Attribute marked filterable by the catalogue | path predicates: the field is the OCSF path as a JSON Pointer |

The fixed columns that every driver has are: time, recorded at, project, log kind, log, scope, tenant, action, class, activity, outcome (`status_id`, `status_code`), actor, the subject, target and request-id paths, trace id and record id.

### The page

The response is a page object:

| Field | Meaning |
|---|---|
| `items` | The records. Each carries the envelope fields and the canonical bytes. |
| `query` | The normalised query |
| `cursors` | `self`, `first`, `prev`, `next`. There is no `last`, because counting is expensive. |

**Cursors** are opaque. They encode the keyset boundary, the direction and a hash of the normalised query, and are rejected with a different query. A changed grant invalidates old cursors by construction. **`next` never disappears:** on the last page it yields new records later, advancing on recorded order, so late events are never missed. This is the tail.

### Free text

`q` matches words. Words are the alphanumeric runs of a field, folded for case and diacritics, with no stemming and no substring search. All the words of a query are required. A trailing `*` makes a prefix. Only fields that the catalogue marks searchable are reached. A personal field is not searchable unless the catalogue says so.

## Facets

`Facets` takes the same filter plus the facetable fields, and reads from the attribute counts. It returns, for each field, its values and their counts.

## Get

`Get` takes a record id and returns:

| Part | Holds |
|---|---|
| The record | The canonical bytes the emitter produced, opened under the tenant's `evidence` key and the grant of the call |
| The envelope | The clear envelope |
| The proof | The inclusion proof against a checkpoint that a witness cosigned, and the checkpoint with its cosignatures |
| The provenance | The log, the entry's index and the seal entry's counts |

A record in a cold storage class answers `pending` with a retry time, and asks for the retrieval when the grant allows it. It never holds the request.

## Export

`Export` starts a job for one project and log kind. The job writes NDJSON of OCSF events, opened for the grant, and ends with a short-lived signed URL. The export:

- adds enum captions, `observables` and the `record_integrity` attestation, whose fingerprint (SHA-256 over the RFC 8785 bytes) points back to the log entry;
- keeps `metadata.log_name` and the product object as written;
- is itself recorded as an Event Log Activity event, activity 3 Export.

Down-mapping to another consumer's version (a security data lake that reads an older OCSF) lives in a target-specific exporter, a driver, and never in the record.

## Refusals

Every refusal is a Connect error with a reason from a closed list and the path of the field at fault, and never a value, as in atoom's [refusal detail](https://github.com/truvity/atoom/blob/master/common/docs/refusal.md).

## Built and planned

Built: the first reader that asks for a grant for every call and applies it (in sluis), and the index port's `Search` (atoom). Planned: everything else on this page.
