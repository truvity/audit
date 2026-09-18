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

These are the chart's values (`charts/audit/values.yaml`), which are the
binary's flags with dots.

| setting | meaning |
|---|---|
| `bucket`, `prefix`, `region`, `kmsKey` | the archive |
| `governance` | lets a privileged role shorten a retention. Off, and the chart refuses it on: a deployment that wants it says so in a values file of its own |
| `profiles` | composition of presets and prefixes, the document `audit-writer --deployment` reads |
| `replicas` | writers sharing the stream. Above one needs `database` and a key directory every replica can write |
| `stream.url`, `stream.name`, `stream.consumer` | JetStream. Without a URL the writer only serves its sink, which is what an application embedding it wants |
| `stream.batch` | how many records are taken at once. Default 100 |
| `stream.ackWait` | how long the stream waits for a batch to be taken before offering it again. Default 30s, and it must exceed the longest a write can honestly take: a batch is acknowledged only once its records are in the archive |
| `roll.interval` | how often an object is rolled and put |
| `database.url` or `database.existingSecret` | the index and the shared deduplication table, one database. Without it the writer indexes nothing and deduplicates in process |
| `database.migrate` | apply the schema from a pre-upgrade hook Job. The writer refuses to start on a version it does not know and never migrates itself |
| `keys.provider` | `local` only, until `kms` and `transit` land |
| `keys.local.existingSecret` | the 32-byte root the data keys are wrapped under |
| `keys.local.persistence` | where the wrapped keys live. They are random, not derived, so this is the only copy: back it up, and use ReadWriteMany for more than one replica |
| `catalogues` | catalogue documents registered at start-up, by name |

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

The built `audit-query` reads who may authenticate and what each caller may
see from one file, `--grants`/`AUDIT_GRANTS`:

```yaml
issuers:
  - url: https://id.example.com        # exactly as the tokens' iss claim says it
    audience: audit                    # required
  - url: https://customers.example.com
    audience: audit
rules:                                 # first match wins
  - name: auditors
    issuer: https://id.example.com     # required once more than one issuer is trusted
    claim: groups
    value: all:audit:auditor
    grant:
      all_tenants: true
      profiles: [security, operational]
      operations: [search, facets, get, export]
  - name: acme-viewers
    issuer: https://customers.example.com
    claim: groups
    value: acme:audit:viewer
    grant:
      tenants: [acme]
      profiles: [security]
      operations: [search, get]
```

It refuses to start when:

- the file names no issuer, because then nobody could ever sign in;
- an issuer has no audience. An audit log must not accept a token minted for
  another service, because any workload holding that token could replay it here;
- more than one issuer is trusted and a rule names none. Every issuer can assert
  any claim, so a rule matching a group from anyone gives operator access to
  whoever administers the least-trusted issuer;
- a rule names an issuer that is not listed, or an operation that does not
  exist.

A bearer token in `Authorization` is accepted, and so is the access token the
fleet gateway forwards. Verification is
[gateway-auth](https://github.com/truvity/gateway-auth)'s, one verifier per
issuer: discovery, a key set refreshed in the background, signature, issuer,
audience and expiry. That library allows no clock skew.

## Digest job

| setting | meaning |
|---|---|
| `signer` | `kms`, `transit`, `local` |
| `schedule` | hourly |
| `lookback` | how far before a window to look for objects keyed under an older day. Default 7 days; the verifier's must be at least this |
| `max_windows` | how many windows one run may seal when catching up. Default 168 |
| `verify.schedule` | nightly |
| `anchor` | `none`, `rfc3161` with a TSA URL |

Built: `audit digest --deployment --key --key-id --bucket --sink [--from --to
--lookback --max-windows --instance]` and `audit verify … --sink [--lookback
--instance]`, which takes the same lookback. `--sink` is the writer the job
records itself through (`audit.digest.written`, `verified`, `failed`); a
scheduled run should always have it, and an auditor's run by hand should not.
The signing key and the job's identity are separate from the writer's.

## Clock job

| setting | meaning |
|---|---|
| `ntp[]` | time references; the quickest to answer is believed, and one being unreachable is survivable |
| `max_offset` | the offset beyond which the run fails. Default 1s; 0 records any offset and never fails |
| `sink` | the writer the reading is recorded through |

Built: `audit clock-sync --ntp … --sink … [--max-offset --timeout]`.

## Purge job

| setting | meaning |
|---|---|
| `schedule` | daily |
| `identifying_after` | how long the index keeps who an event happened to. No default: no shipped preset states one, so it is the deployment's own policy |
| `dedupe_window` | how long a written identifier is remembered. Default the widest window the profiles ask for |

Built: `audit purge --deployment --database [--identifying-after
--dedupe-window --dry-run]`. It never touches the archive.
