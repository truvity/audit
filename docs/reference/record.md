# Record reference

Source of truth: [record.proto](../../proto/audit/v1/record.proto). The
generated JSON Schema is published at build time under `gen/jsonschema/`.

The class column is how the shipped presets read each core field. The presets
are authoritative: a profile keeps a core field because a preset names it, not
because of its class (see [presets](presets.md)).

| field | class | set by | notes |
|---|---|---|---|
| `id` | shared | emitter | UUIDv7; idempotency key at every hop |
| `occurred_at` | shared | emitter | RFC 3339 UTC |
| `recorded_at` | shared | writer | drives the tail cursor |
| `schema_version` | shared | emitter | `major.minor` |
| `catalogue_version` | shared | emitter | resolves extension schemas |
| `source` | shared | emitter | namespace of `action` |
| `observer` | shared | writer | verified publisher identity, version, instance |
| `sequence` | shared | emitter | monotonic per instance |
| `action` | shared | emitter | `resource.verb` under `source` |
| `operation` | shared | emitter | seven values |
| `outcome` | shared | emitter | result, reason, code |
| `tenant_id` | shared | emitter | legal entity; `@platform` for the installation's own records |
| `subject` | audit, history | emitter | kind and id; treated by kind category |
| `actor` | audit, history | emitter | kind, id, session, auth method, credential hash, attributes |
| `targets[]` | audit, history, evidence | emitter | type, id, name (non-person only), attributes |
| `context` | audit | emitter | address chain, user agent, request, trace, span, areas |
| `capture` | audit | emitter | level-gated request and response, truncated flag |
| `previous_attributes` | history | emitter | update actions only |
| `data` | per property | emitter | extension slot keyed by action |
| `meter` | metering | emitter | name, quantity, unit, kind, dimensions |
| `attributes` | audit | emitter | bounded map |
| `unmapped` | audit | emitter or adapter | what could not be mapped |
| `origin_hash` | shared | writer | SHA-256 of the wide record |
| `profile` | shared | writer | which copy this is |

## Bounds (enforced by the emitter library, defaults)

| what | bound |
|---|---|
| `attributes` | 50 keys, key 64 chars, value 512 chars |
| `outcome.reason` | 512 chars |
| `context.user_agent` | 256 chars |
| `context.client_addresses` | 8 entries |
| `capture.request`, `capture.response` | 64 KiB each; a larger body is dropped by the emitter. The writer detaches bodies above its own, smaller, threshold to the payload prefix |
| `targets` | 32 entries |
| truncation order | response, request, unmapped, attributes, reason |

## Negative list (refused by the emitter library)

Secrets, tokens, passwords, private keys, connection strings, card numbers,
bank account numbers, session identifiers in clear, presented attribute
values, user content, names, e-mail addresses. Property names containing
`password`, `secret`, `token`, `authorization` are refused in extension
schemas unless annotated `x-audit-sensitive: redact`.
