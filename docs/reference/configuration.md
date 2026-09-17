# Configuration reference

Draft. Names are stable; defaults are the presets' where they exist.

## Emitter library

| setting | meaning |
|---|---|
| `source` | catalogue source name |
| `catalogue` | path or embedded catalogue |
| `transport` | `inprocess`, `s3`, `nats` |
| `delivery_default` | when the catalogue does not say |
| `outbox.path` or `outbox.postgres` | durable local store for outbox mode |
| `capture.max_bytes`, `attributes.max_keys` | bounds |
| `trusted_hops` | how many forwarded-for entries belong to your own edge |

## Split writer

| setting | meaning |
|---|---|
| `stream.url`, `stream.consumer` | JetStream |
| `bucket`, `region`, `kms_key` | object storage |
| `profiles` | composition of presets and prefixes |
| `roll.interval`, `roll.max_bytes` | object rolling |
| `payload.threshold_bytes` | detach above this |
| `dedupe.window` | from the strictest preset by default |
| `keys.provider` | `kms`, `transit`, `local` |
| `index.postgres` | connection for the facet index |
| `dlq.prefix` | dead-letter prefix |

## Query service

| setting | meaning |
|---|---|
| `searcher` | `postgres`, `s3scan`, `memory` |
| `auth.authenticator` | `jwt`, `trusted_upstream`, `none` |
| `auth.issuers[]` | issuer URL, JWKS, audience, claim mapping |
| `auth.authorizer` | `declarative`, adapter name |
| `auth.grants[]` | claim value → tenants, profiles, operations, window |
| `limits.max_range`, `limits.export_max_records` | caps |

## Digest job

| setting | meaning |
|---|---|
| `signer` | `kms`, `transit`, `local` |
| `schedule` | hourly |
| `verify.schedule` | nightly |
| `anchor` | `none`, `rfc3161` with a TSA URL |
