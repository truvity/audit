# Record reference

Source of truth: [record.proto](../../proto/audit/v1/record.proto). The
generated JSON Schema is published at build time under `gen/jsonschema/`.

Which profile copies carry a field is decided by the profile's presets
([presets](presets.md)); `audit profile explain <name>` prints the result. The
record does not say.

| field | set by | notes |
|---|---|---|
| `id` | emitter | UUIDv7; idempotency key at every hop |
| `occurred_at` | emitter | RFC 3339 UTC |
| `recorded_at` | writer | drives the tail cursor |
| `schema_version` | emitter | `major.minor` |
| `catalogue_version` | emitter | resolves extension schemas |
| `source` | emitter | namespace of `action` |
| `observer` | writer | verified publisher identity, version, instance |
| `sequence` | emitter | monotonic per instance |
| `action` | emitter | `resource.verb` under `source` |
| `operation` | emitter | seven values |
| `outcome` | emitter | result, reason, code |
| `tenant_id` | emitter | legal entity; `@platform` for the installation's own records |
| `subject` | emitter | kind and id; treated by kind category |
| `actor` | emitter | kind, id, session, auth method, credential hash, attributes |
| `targets[]` | emitter | type, id, name (non-person only), attributes |
| `context` | emitter | address chain, user agent, request, trace, span, areas |
| `capture` | emitter | level-gated request and response, truncated flag |
| `previous_attributes` | emitter | update actions only |
| `data` | emitter | extension slot keyed by action; each property carries its own class |
| `meter` | emitter | name, quantity, unit, kind, dimensions |
| `attributes` | emitter | bounded map |
| `unmapped` | emitter or adapter | what could not be mapped |
| `origin_hash` | writer | SHA-256 of the wide record |
| `profile` | writer | which copy this is |

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
