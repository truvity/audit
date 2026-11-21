# Action table and catalogue

The decision is in [0004](../decisions/0004-the-record-format.md). A product writes a table of its actions. CI generates the JSON Schemas of its records from the table. The pipeline publishes the table with the schemas as the catalogue. This page describes the formats.

The table of sluis is the working example. It is one YAML file in [sluis](https://github.com/truvity/sluis/blob/main/docs/architecture/audit-actions.yaml), with a generator that makes the schemas, example records and the code of the emitters. The format below is the format of that file. The generator moves to this repository, and the format is fixed then. **Open: the exact generator options** are written when it moves.

## The table

A table file has:

| Key | Holds |
|---|---|
| `schema` | The version of the table format |
| `ocsf` | The pinned OCSF version |
| `product` | The product's name. It names the product object. |
| `types` | The named value types ([below](#value-types)) |
| `actions` | One row for each action |

A row of `actions`, with the keys that sluis's table has today:

| Key | Holds |
|---|---|
| `name` | The action, which is `metadata.event_code`. Unique. |
| `actor` | The component that emits it |
| `class` | The OCSF class |
| `activity` or `activities` | The activity (`id`, and `name` for activity 99), or the list of activities an action may have |
| `outcomes` | The outcomes it may have: `success`, `failure` |
| `tenant` | `optional` where the record may name no tenant |
| `subject` | Whether the record may name a person: `optional` or `required` |
| `policy` | Whether the record may carry a policy decision: `optional` or `required` |
| `entity` | For Entity Management rows: the members of `entity` that the row fills |
| `fields` | The members of the product object, each with a `type`, and `required: true` only where every record of the action carries it |
| `example` | An example record body. It becomes a test vector. |

The decisions ask for more keys, which are **planned**:

| Key | Holds |
|---|---|
| `emit` | The call an action uses: best effort, required, or wait without failing ([0005](../decisions/0005-the-security-class.md)). In sluis the component's code chooses it today. A table is the place where a reviewer sees it. |
| `meters` | The meters an action may carry, with the outcomes they record ([0007](../decisions/0007-meters.md)) |
| `subject_path`, `target_path`, `request_id_path` | The paths that the index reads |
| `filterable`, `facetable`, `searchable` | The fields the index may filter, count and search |

A field that is not in a row is refused: the object is closed. `lost` is in no row, because the emitter adds it to any record, and the schema allows it everywhere.

**Rules for a change.** Within a major version of a product, a member is only added, never removed or retyped, and a new member is optional. A meter is never renamed, retyped or dropped without being retired. CI compares the new table with the previous release's and fails on a breaking change, and on any use of an item that the pinned OCSF version deprecates.

**The check.** CI fails while a generated file differs from what the table makes. It also fails when a component emits an action that the table lacks, when a row has no emitter, and when the catalogue in an installation differs from the table.

## The class mapping

Each product maps its actions to OCSF classes. These are the rules that sluis applies, checked against OCSF 1.9.0. They show how a product chooses a class.

| Kind of action | OCSF class and activity |
|---|---|
| Sign-in, recovery sign-in | 3002 Authentication, 1 Logon |
| Sign-out, end of a session | 3002 Authentication, 2 Logoff |
| Step-up | 3002, 1 Logon, `is_mfa` true, `activity_name` "Step-Up" |
| Token grant, refresh, exchange, revocation | 3002, 99 with `activity_name`. An impersonating exchange is 7 Account Switch. |
| A grant of scopes | 3003 Authorize Session, 1 Assign Privileges |
| Certificate issued or revoked | 3002, 99 "Certificate Issue" or "Certificate Revoke" |
| Credential added or removed | 3007 User Management, 18 and 19 |
| Enrolment | 3004 Entity Management, 6 Enroll and 7 Unenroll |
| Passkey registered or removed | 3007, 12 and 13 |
| Person first seen, role change | 3007 User Management, 3006 Group Management |
| Configuration or registration write | 3004, 1, 3 and 4; the value in `updated_entity.data` |
| A request at the edge | 6003 API Activity: 2 Read for `GET` and `HEAD`, 1 Create for `POST`, 99 other |
| A read of the log | 6003, 2 Read |
| An export of the log | 1008 Event Log Activity, 3 Export |
| Key use | 6003, 99 "Sign" or "Open" |
| Key created, rotated or destroyed | 3004, 1, 3 and 4 |
| Start of a component | 6002 Application Lifecycle, 3 Start |
| Reload, refused reload | 6002, 99 "Reload" and 99 "Refuse" |
| Writer and log lifecycle | 1008 Event Log Activity |

- **A policy decision** is part of the record of the action it gates, in the `security_control` profile.
- **Token stages stay activity 99.** The ticket activities of Authentication are described as Kerberos, and no vendor mapping uses them for OAuth.
- **The actor chain is not `delegation`,** which exists only through the `ai_operation` profile. The acting party is `actor.user`, the subject `user`. A record never uses the `delegation` object or declares `ai_operation`.
- **An issued certificate is not Authentication's `certificate` object,** which means a presented certificate. The field moves if OCSF adds a home for issuance.

## Value types

The types of the table keep patterns plain, so that RE2, Rust's `regex` crate and ECMAScript read them alike. A pattern is an anchored character class with a length, and the generator refuses more.

| Type | Form | Holds |
|---|---|---|
| `word` | `^[a-z][a-z0-9_.-]{0,63}$` | A closed word: a reason, an operation, a state |
| `reason` | `^[A-Za-z][A-Za-z0-9_.:-]{0,63}$` | A `status_code`: the closed reason of a refusal. It names a rule and never a value. |
| `ident` | `^[A-Za-z0-9._:/+=@~*-]{1,256}$` | A name or id of a registry, an address or a key. Never a value. |
| `tenant` | `^(\*\|[A-Za-z0-9._-]{1,128})$` | A tenant id, or `*` for all the tenants of a grant |
| `text` | at most 1024 characters | Bounded text. The emitter refuses an e-mail shape in any string. |
| `hex96`, `hex64` | 96 or 64 lower-case hex digits | A SHA-384 or SHA-256 digest |
| `uint` | integer, 0 to 2^53 - 1 | A non-negative count |
| `bool` | boolean | |
| `source` | `^[A-Za-z_][A-Za-z0-9_.-]{0,63}$` | `src_endpoint.name`. It holds no `:`, `/` or `@`, so no address reaches it. |
| `operation` | `^[A-Za-z][A-Za-z0-9_.-]{0,63}$` | `api.operation`: a closed word of the component's, never text of a request |

A product may add types for its own needs, such as a certificate serial. The domains of the types are in the shared test vectors, which every emitter validates.

## The catalogue

The catalogue is the table with its generated schemas, signed as one item of the `AuditCatalogue` kind ([config kinds](config-kinds.md)).

| Part | Holds |
|---|---|
| `release` | The newest release it covers |
| `policy` | The version of the schema policy it was made to (1 today) |
| `table` | The action table |
| `schemas` | One shared definitions document, and a slim schema for each action |

**The policy of the schemas** keeps both validation engines in agreement. A schema may use only a closed list of keywords, `$ref` to the shared definitions only, one grammar for patterns, nesting of combinators at most three deep, and at most 4000 schema objects visited in the worst case for one record (the heaviest real record visits 885). A reader refuses a catalogue with a higher policy version.

**Validation by the writer.** It validates each record against the schema of its action in `default`, refuses a record whose `metadata.product.version` is newer than `release` or is not a version, gives each validation five seconds, and refuses only a record that times out three times in one process.

## Conformance vectors

One set of vectors runs through the Go, Rust and TypeScript validators, and the build fails when they differ.

| Set | Holds |
|---|---|
| Action vectors | One example record for each action |
| Domain vectors | The domains of the value types |
| Schema vectors and schema-policy vectors | Records that must validate, and documents that the policy must refuse |
| Record vectors | Canonical forms of whole records |
| JCS vectors and the RFC 8785 corpus | The inputs and outputs of the canonicalisation tests of the RFC's author, unchanged (Apache-2.0), with the canonical text that both languages must produce. A byte that changes in the differential file is a changed record hash and needs a decision, not a regeneration. |
| Merkle vectors | RFC 6962 trees, for the Go, Rust and Python implementations |
| Semver vectors | Release comparison |

The corpus files are Anders Rundgren's `testdata` of the JSON Canonicalization Scheme, kept as copies with their licence. The sets live in sluis today and move here with the emitters.

## Built and planned

Built: the format, the generator, the schemas and the vectors, for sluis's table. Planned: the move to this repository and a table for each further product.
