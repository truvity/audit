# Configuration: the jobs

The notary and the four scheduled commands of `audit` that read one `--config` file each.
Shared blocks are in [configuration](configuration.md#shared-blocks).

## Scheduled jobs

Each job is `audit <command> --config <file>` (the notary is its own binary,
`audit-notary --config <file>`), runs to completion and records what it did
through the writer's own sink, so each needs `sink` and, where the writer
verifies callers, a `tokenFile`. Each has its own identity.

### audit-notary

Seals the archive ([0019](../decisions/0019-seals.md)): for every profile and
tenant, and every hour that has ended and settled since the last seal, one
signed seal of what the hour holds, chained to the one before, written to
`seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws`
([bucket contract](bucket-contract.md#seals)). Hourly in the chart. It is the one
part that signs, so its `signer` is a managed key the writer's identity cannot
use ([key custody](../explanation/key-custody.md#signing-key)), and it runs as an
identity of its own, which the chart enforces. A rerun writes nothing new, and an
hour whose objects do not match their metadata is not sealed, nor is any hour
after it.

<!-- generated: config-audit-notary -->
| key | type | default | meaning |
|---|---|---|---|
| `archive` | `archive`, required | | the archive to read and to put seals in (`bucket`, `prefix`, `lockMode`, `kmsKey`); `lockMode` defaults to `compliance`, and a seal is locked as long as the records it covers |
| `signer` | object, required | | exactly one of `kms`, `transit` and `file` |
| `signer.kms` | `{key, region}` | | an AWS KMS key, `ECC_NIST_P384` `SIGN_VERIFY`, by ARN, ID or alias; the credentials are the SDK's ambient ones, the notary's role |
| `signer.transit` | `{key, openbao}` | | an OpenBAO transit key of type `ecdsa-p384`; `openbao` is the [shared block](configuration.md#shared-blocks) |
| `signer.file` | `{path}` | | a P-384 private key in PEM (PKCS#8 or SEC 1), for development |
| `profiles` | list of strings, at least one, unique | every profile the archive has records for | the profiles to seal |
| `settle` | duration | `10m` | how long after an hour has ended it is sealed, so that a batch put late in the hour it is keyed by is in the seal |
| `sink` | `sink` | none | the writer the job records each seal through (`audit.seal.written`) |
| `require` | `logged`, `queued` or `archived` | none | the weakest durability the writer's acknowledgements may carry; needs `sink` and `sink.expect` |
<!-- /generated -->

It reports `audit.seal.age` (the age of the newest sealed hour, per profile, of
the tenant furthest behind), `audit.seal.written` and `audit.seal.failures`
over OTLP ([telemetry](telemetry.md)). The first seal of a tenant is
of the hour of its first object; an hour with no objects is sealed too, from then
on.

### audit verify

Checks every record object of a range of ingest time against the
[bucket contract](bucket-contract.md) and reports what it finds
([verification](../how-to/verify-the-trail.md)). It reads the archive only, so its
`archive` has no `lockMode`, and it needs no key. Nightly in the chart, over the
last 24 hours. One job checks every profile it names, or every profile the
deployment composes.

<!-- generated: config-audit-verify -->
| key | type | default | meaning |
|---|---|---|---|
| `deployment` | path, required | | the profile configuration; each object's lock is held to what its profile demands |
| `archive` | `archive`, required | | the archive to read (`bucket`, `prefix`) |
| `profiles` | list of strings, at least one, unique | every profile the deployment composes | the profiles whose objects to check |
| `last` | duration | `24h` | check the objects ingested in the last this long, ending at the hour that has closed |
| `seals` | object | none | also check the seals of the range: `roots` (list of thumbprints, at least one, required) are the only keys trusted; `settle` (default `10m`, keep it equal to the notary's) and `grace` (default `1h`) say when a seal is due. Without it no seal is checked |
| `sink` | `sink` | none | the writer the job records what it checked through (`audit.seal.verified` or `audit.seal.failed` per ingest hour, recorded only with `seals`) |
| `require` | `logged`, `queued` or `archived` | none | the weakest durability the writer's acknowledgements may carry; needs `sink` and `sink.expect` ([durability](configuration-writer.md#durability-require-forward-consume)) |
<!-- /generated -->

The keys `publicKeyFile`, `lookback` and `record` of the v0 job are gone: the
check needs no key (`seals.roots` are thumbprints), the range is of ingest time
so there is nothing to look back over, and there is no `verified/` prefix to
write.

### audit purge

Brings the index and the deduplication table within the profiles. Daily in the
chart. It never touches the archive.

<!-- generated: config-audit-purge -->
| key | type | default | meaning |
|---|---|---|---|
| `deployment` | path, required | | the profile configuration |
| `database` | `database`, required | | the index and the deduplication table, as the purge job's **own** role (`audit migrate --purge`): delete from both, and add nothing to either |
| `identifyingAfter` | duration | unset: nothing is forgotten early | how long the index keeps who an event happened to. No shipped framework profile states one, so it is the deployment's own policy |
| `dedupeWindow` | duration | the widest window the profiles ask for | how long a written identifier is remembered |
<!-- /generated -->

### audit clock-sync

Compares the clock with UTC and records the answer. Daily in the chart.

<!-- generated: config-audit-clock-sync -->
| key | type | default | meaning |
|---|---|---|---|
| `ntp` | list of strings, at least one, required | | time references, host or host:port; the quickest to answer is believed, and one being unreachable is survivable |
| `sink` | `sink` | none | the writer the reading is recorded through |
| `require` | `logged`, `queued` or `archived` | none | the weakest durability the writer's acknowledgements may carry; needs `sink` and `sink.expect` ([durability](configuration-writer.md#durability-require-forward-consume)) |
| `maxOffset` | duration | `1s` | the offset beyond which the run fails. `0s` records any offset and never fails |
| `timeout` | duration | `5s` | how long to wait for a reference |
<!-- /generated -->

### audit migrate

Applies the schema and grants each part's database role what the part needs
and takes back the rest. In the chart it is a pre-install and pre-upgrade hook
Job; run it by hand from one place otherwise.

<!-- generated: config-audit-migrate -->
| key | type | default | meaning |
|---|---|---|---|
| `database` | `database`, required | | the database, as the **owner** of the tables. No part connects as the owner: an owner is bound by no grant and no row-level security, so the migration refuses to grant a role that is the owner |
| `writer` | string | none | the write path's role: the deduplication table, the catalogue registry and the key directory, and none of the index |
| `observe` | string | none | the indexer's role: read and write on the index and its cursors, and `execute` on `audit_ensure_month(date)`, the function that creates a month's partition as the owner |
| `reader` | string | none | the query service's role: `select` on the index's tables, bound by row-level security to the tenants of each request, and nothing else |
| `purge` | string | none | the purge job's role: delete from the index and the deduplication table |
<!-- /generated -->

Each role must already exist, and no role may be named for two parts: the
separation is that they are different. A part left unnamed is not granted
anything, and one that connects as the owner has no separation at all.

