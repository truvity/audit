# Configuration reference

Draft. Names are stable; defaults are the presets' where they exist.

## Emitter library

| setting | meaning |
|---|---|
| `source` | catalogue source name |
| `catalogue` | path or embedded catalogue |
| `transport` | `inprocess`, `s3`, `nats` |
| `delivery_default` | when the catalogue does not say |
| `outbox.path` | directory of the durable local store that outbox delivery needs |
| `publish` | how often the outbox is drained. Default one second |
| `capture.max_bytes`, `attributes.max_keys` | bounds |
| `trusted_hops` | how many entries at the near end of the forwarded chain belong to your own edge. No safe default but zero: with no proxy in front, the peer is the client, and getting this wrong records a load balancer as the actor's address |

## Split writer

| setting | meaning |
|---|---|
| `stream.url`, `stream.consumer` | JetStream |
| `bucket`, `region`, `kms_key` | object storage |
| `profiles` | composition of presets and prefixes |
| `roll.interval`, `roll.max_bytes` | object rolling |
| `payload.threshold_bytes` | detach above this |
| `dedupe.window` | from the widest preset window the deployment's profiles ask for. The table is shared by every profile, so a record one profile remembers for a fortnight must not be re-written because another's window was shorter |
| `keys.provider` | `kms`, `transit`, `local` |
| `index.postgres` | the index and the shared deduplication table, one database. Without it the writer indexes nothing and deduplicates in process, which is why it then refuses to run more than one replica |
| `dlq.prefix` | dead-letter prefix |

The built `audit-writer` takes these as flags or environment variables:
`--database`/`AUDIT_DATABASE` is the index, `--replicas`/`AUDIT_REPLICAS` is how
many writers share the stream. A writer whose database is at a schema version
this build does not know refuses to start; run `audit migrate --database <url>`
first, from one place.

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
