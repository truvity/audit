# 0004. The record format: OCSF, canonical JSON, integer bounds, a product object and a catalogue

Status: accepted, 2026-11-21

## Context

- **OCSF is a maintained and consumed security event schema.** The Open Cybersecurity Schema Framework has written compatibility rules: a minor release only adds, and a major release removes. Its release when this record was written is 1.9.0. It has core classes for authentication, user, group and entity management and API activity. AWS Security Lake and the pipeline vendors read it. OCSF has no class for issuing a certificate or a token, or for using a key, and it has no usage class. Its FAQ advises native producers to write an extension, and that is advice, not a rule.
- **The log is sealed and verifiable,** so a hash must be reproducible by any implementation. That needs one canonical serialisation and numbers that survive it.
- **Several projects emit into one scope,** each with actions of its own and its own releases.
- **Records are kept for years.** A format that is frozen wrongly cannot be corrected in a locked log.

The record format of audit-v1 was its own: a Protobuf core with JSON Schema slots. It is not carried over. Two of its rules were right and are kept: the record carries no floating point, and every free-text field is bounded.

## Decision

### An audit record is an OCSF event

- **A security record is an OCSF event of a core class of the pinned version.** The action is `metadata.event_code`. Product and release are `metadata.product.name` and `metadata.product.version`. The tenant is `metadata.tenant_uid`. The scope is `metadata.log_name`. The record id is `metadata.uid`. The outcome is `status_id` with `status_code`. The trace id is `metadata.correlation_uid`.
- **Each release pins one OCSF version** in `metadata.version`. The first pins 1.9.0, and a `-dev` version is never pinned. An upgrade moves one minor release at a time, in a deliberate pull request. CI compares the old and new generated schemas and fails when the action table or a product object uses an item the new version deprecates, or when a new core attribute takes the name of a field in a product object. A new OCSF major release needs a new record.
- **Where OCSF has no activity for an action, the record uses activity 99 (Other)** with a name. The class mapping of a product is part of its action table ([reference/action-table-and-catalogue.md](../reference/action-table-and-catalogue.md)).
- **A policy decision is part of the record of the action it gates,** in the `security_control` profile, and not a record of its own.
- **The actor** is `actor.user.uid`, the name of the emitting component, with the kind in `actor.user.type_id` (1 user, 2 operator, 3 component of a product, 4 workload or client). sluis writes `<actor>@<scope>`. A person or an external actor is a keyed pseudonym ([0011](0011-evidence-encryption-and-pseudonyms.md)).
- **A health `check` call is not audited. Every other call is,** a refusal included. The health protocol's `check` answers whether a process is up and reads or changes nothing, so a record for it would grow the log without end. A probe is answered by the process itself, so it calls no other component and provokes no refusal at one.
- **A record may point at a trace, and the trace never carries what the record says.** The record holds the trace id. A trace may hold the record id.

### One product object

- **Every fact of a product that OCSF has no attribute for goes in one object, `unmapped.<product>`, and nowhere else in `unmapped`.** `unmapped` is in the base event of every class and version, so the object survives down-mapping. A field that has a core home uses it.
- **The writer refuses a record whose `unmapped` holds any key but its product's name.** One product's facts cannot appear under another's name.
- **The schema of the object is the product's own.** It is generated in CI from the product's action table, closed, with the fields each action requires.
- **No OCSF extension.** The fields are versioned with the product's release. Within a major version a field is only added, never removed or retyped, and a new field is optional. When OCSF gives a field a core home, the field is deprecated, new records use the home and old records keep the field. An extension comes only for a real consumer of typed fields, as a new record. Today no consumer needs one: a SIEM that flattens JSON queries a field of the object as easily, Security Lake takes OCSF 1.3 at most for a custom source and loads no extension, and the vendor mappings read when this was decided keep the rest in `unmapped`.
- **The emitter's own field.** The emitter adds one field to the object of any record: `lost`, the running total of best-effort facts it failed to deliver ([0005](0005-the-security-class.md)). Only the emitter writes it, and the writer refuses it in an event from a caller. In sluis it is `unmapped.sluis.lost` today.

### Canonical JSON and bounds

- **A record is RFC 8785 canonical JSON, and the same bytes travel end to end.** The emitter makes the bytes. The transport carries them. The writer checks they are canonical and stores them unchanged inside the sealed entry. The reader returns them as opaque bytes beside typed envelope fields. Nothing re-encodes a record. (Protobuf's JSON mapping would turn 64-bit integers into strings.)
- **Canonical rules:**
  - UTF-8 strings, never normalised by the writer;
  - integers within plus or minus (2^53 - 1), and **no floating point**: a value that can exceed the bound is a string;
  - times in integer milliseconds;
  - no duplicate keys;
  - bounded free text.
- **The writer parses, canonicalises again and refuses a record whose bytes differ,** then stores the bytes it received. A verifier hashes the stored bytes and never re-serialises.
- **The stored record is lean:** enum ids without captions (`activity_name` only for activity 99 or where the table names one), no `observables`, enrichments or `raw_data`, and no `unmapped` but the product's object.
- **A record never carries a secret, an e-mail address, a name, a phone number or a token.** A secret appears as a fingerprint and a revision. A person appears as a keyed pseudonym. The emitter refuses an e-mail shape in any string.
- **Never an address, never text a caller wrote.** The source of a call is a name the transport gave, a node name or a constant. No IP address, host and port, path, query, header or parameter value reaches a record.
- **Records are never rewritten.** A log holds records of several versions, and readers, the indexer and exporters handle each by the versions it names.

### The action table, the catalogue and validation

- **Each project's actions are a table in code,** in the project's own release. For each action: its class and activity, required fields, the treatment of each field, the meters it may carry ([0007](0007-meters.md)), its subject, target and request-id paths, and which fields are filterable, facetable or searchable. An action has one event code, so no field carries a sub-type.
- **The JSON Schemas of the records are generated from the table in CI,** published with the release and compiled into the emitters. Go, Rust and TypeScript types come from the same source.
- **The catalogue is the table with its schemas, published as configuration.** The publishing pipeline signs each release's table and its generated schemas as one item of the `AuditCatalogue` kind ([0015](0015-configuration-kinds.md)).
- **The catalogue of a release is published before its emitters run.** The writer validates each record against the catalogue that its product names, and refuses a record whose release has no catalogue. A product's new release therefore needs no new writer.
- **One item holds for every release of a major version.** A project has one item, `default`, the table of its newest release. The table only grows within a major version, so the newest catalogue validates the records of every older release of the same major version. The writer validates each record against the schema of its action in `default`, and refuses a record whose `metadata.product.version` is newer than the item's release, or is not a version. A new major version publishes its catalogue before its emitters run, and a writer that reads the older one refuses its records until then. The seal entry of the batch says how many were refused and why, and the quarantine keeps them whole.
- **The catalogue is held to a policy, and the policy has a version.** The reader of a catalogue (the state registry, the writer, the indexer) accepts only documents the generator makes: a closed list of keywords, `$ref` to the shared definitions only, one grammar for patterns, bounded depth, and a bound on the work one record can cost, counted in schema objects visited in the worst case. The catalogue says the version of the policy it was made to (1 today), and a reader refuses a higher one by name, at load. A generator change that needs something outside the policy raises the version in the same change, and readers are upgraded first.
- **The writer gives each validation five seconds,** and a record that exceeds them is not refused at once: the batch is given back with an error and delivered again, and only a record that times out three times in a process is refused (as `schema`, kept whole in the quarantine).
- **No catalogue is stored in the log, and no record carries a catalogue field.** Product name, release and event code identify the entry in force. Witnesses cosign the log, not schemas.
- **A record validates in three places.** The writer enforces the JSON Schema 2020-12 of the project's catalogue and is authoritative. Go and Rust emitters validate before they emit. A browser emitter does not. The OCSF toolkit runs in CI only, on the example records of each action. One set of test vectors runs through the Go, Rust and TypeScript validators, and the build fails when they disagree.

### The attributes a class requires

An OCSF class requires attributes that a product object cannot stand in for. The emitter fills every one from what it knows, by class, in the kit, and CI fails on any error in an example record. A constant that says "not known" is used where nothing is known, and never a made-up value.

| Class | Attribute | What it holds |
|---|---|---|
| 6002 Application Lifecycle | `application` | `name`, the emitting component |
| 3002 Authentication | `service` | `name`, the emitting component |
| 3002 Authentication | `user` | `uid` (a pseudonym) and `type_id`. A record that established no subject says `uid` `unknown` and `type_id` 0. |
| 3004 Entity Management | `entity` | `type`, `type_id` 99, and the `name`, `uid` and `version` of the thing changed |
| 6003 API Activity | `api` | `operation`: a closed word the component chooses, never text of a request |
| 6003 API Activity | `src_endpoint` | `name`: the peer the transport named, a node name, or `unknown` |

`unknown` means this record established no person. It is not a claim that nobody is behind the step. It cannot be a pseudonym, which begin `p1.`. The shape of a class is part of the format: changing it needs a new major version of the product.

### Built and planned

Built: the format, the table and the catalogue generator, the Go and Rust emitters, the writer's checks, and the conformance vectors (in sluis). Planned: the neutral product object name for the `lost` field in products other than sluis, and the move of the generator and vectors here.

## Consequences

- A decrypted record is an OCSF event of a published version. An OCSF-only tool finds every core field and sees the product object as source data. OCSF tells consumers not to rely on structure there, so keeping it stable and publishing its schema with each release is the product's job.
- A field that gains a core home moves at an upgrade, and one log then holds two shapes. OCSF keeps deprecating classes, and the CI comparison catches it.
- Some actions fit OCSF only approximately (token and certificate stages at activity 99, key use as API Activity, step-up as a logon). A security tool sees them in OCSF terms.
- An OCSF record is about 2.8 times a minimal record of its own. A sign-in is about 1.1 KB, and sealed records do not compress.
- The integer bound and the canonical check refuse records other OCSF producers could send. Emitters write large counters as strings.
- A product's release publishes its catalogue before it emits. A record whose catalogue is missing is quarantined, not lost.

## Roads not taken

- **A format of audit's own** (audit-v1's Protobuf core, or a minimal record plus an OCSF exporter), **CADF, the OpenTelemetry log model or ECS.** Invented formats need a mapping in every exporter. CADF is unmaintained, OpenTelemetry has no audit conventions, and ECS is merging into OpenTelemetry.
- **An OCSF extension, full or only for the homeless fields.** It is what the OCSF FAQ advises, but it needs a registry entry, a source tree and a compile step in every strict consumer, and Security Lake drops it, for typed fields no consumer reads yet. It is possible later, in a minor release.
- **Plain OCSF with the facts spread over `policy.data`, `resources[].data` and `entity.data`.** Complete, but untyped slots that only the table explains, and no one place for a SIEM to look.
- **New event classes for token, certificate and key activity.** An exact fit, but an extension class is invisible to rules keyed on core classes.
- **Core tags and labels for the homeless fields.** Down-mapping-safe, but strings meant for classification.
- **A catalogue document in the log, versioned in every record.** A self-describing log, at the cost of a second versioned artefact and a field in every record. The release already identifies the catalogue.
- **The catalogue as a release artefact installed beside the writer.** Every product release would need an operator to install a file and restart the writer. As a signed configuration document it reloads with the rest.
- **No canonical form, deterministic Protobuf or CBOR.** Without RFC 8785 two implementations hash differently. The binary forms are not canonical across languages, and no OCSF tool reads them.
- **Floating-point numbers with a formatter.** The one place a stored byte could read back differently from how it was written.
- **A made-up value for an attribute a class requires.** A loopback address or an empty user would put something in the log that no emitter knew.
- **Start and reload as API Activity.** It keeps one class for everything a component does, at the cost of an `api.operation` of "Start" for something that is not a call. Application Lifecycle is the class OCSF made for it.
