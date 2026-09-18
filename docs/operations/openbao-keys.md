# Pseudonymisation keys in OpenBAO

The `transit` key provider keeps every tenant's key for every purpose in an
OpenBAO (or Vault) transit engine. It is the provider for a deployment of more
than one writer:

- **The key material never leaves the engine.** A pseudonym is the engine's
  HMAC of the identifier; a sealed identifier (for [resolve](../reference/api.md#resolve))
  is the engine's encryption of it.
- **Every replica asks the same engine**, so two writers cannot mint different
  keys for one tenant. `local` needs a `ReadWriteMany` directory for that.
- **There is no directory to lose.** Losing a `local` key directory re-keys
  every tenant without a word.

The cost is one round trip to the engine for each pseudonym.

## Keys

One transit key per purpose and tenant, named `<prefix>.<purpose>.<tenant>`
(`audit.security.acme` with the default prefix). The writer creates a key the
first time it sees a tenant for a purpose. Creating is idempotent in the
engine, so replicas meeting a new tenant at once share one key.

A purpose is a profile's name. Under this provider it may hold letters,
digits, `_` and `-`, and no dot, so that no two (purpose, tenant) pairs can
name the same key.

Every call is pinned to the key's **first version**. Keys are never rotated
(rotating would break linkability for one person across time), and pinning
means a rotation by somebody else changes nothing.

## Policies

What each role may do is the engine's policy, not this code's. A glob on the
prefix and purpose is what scopes a role. The dot after the purpose keeps
`billing` from also matching `billing2`.

The **writer** pseudonymises and seals for every profile it writes, and
creates keys through the encrypt endpoint:

```hcl
path "transit/hmac/audit.security.*"    { capabilities = ["update"] }
path "transit/encrypt/audit.security.*" { capabilities = ["create", "update"] }
path "transit/hmac/audit.billing.*"     { capabilities = ["update"] }
path "transit/encrypt/audit.billing.*"  { capabilities = ["create", "update"] }
# … one pair per profile
```

It has **nothing on `transit/keys/`**. A grant there would reach `rotate`,
`config` and `trim`, which together are erasure. That is why keys are created
through `encrypt` (the engine creates a missing key there when the policy
grants `create`) and never through `transit/keys`.

A **metering** or any other single-purpose role gets its own purpose only:

```hcl
path "transit/hmac/audit.billing.*" { capabilities = ["update"] }
```

The **query service**, if it may resolve, opens sealed identifiers for the
profiles it resolves:

```hcl
path "transit/decrypt/audit.security.*" { capabilities = ["update"] }
```

The **erasure operator**, who runs `audit key destroy`:

```hcl
path "transit/keys/audit.*"    { capabilities = ["read", "update"] }
path "transit/encrypt/audit.*" { capabilities = ["create", "update"] }
```

The glob reaches `rotate`, `config` and `trim` under each key, which is what
destroy uses. (A `+` wildcard only matches a whole path segment, so it cannot
be combined with the prefix to name those three alone.) The encrypt grant is
for a tenant destroyed before it was ever seen: its key is created and
destroyed at once.

**Nobody** gets `delete` on `transit/keys/audit.*`, and no key is configured
with `deletion_allowed`. A deleted key would be created afresh the next time
the tenant appears. The same person would then get a second identity, and
nothing would say so.

## Tokens

The writer reads its token from a file on every call (`--transit-token-file`),
so an agent can keep it renewed. With the chart that is either a Secret
(`keys.transit.token.existingSecret`) or a path where an agent writes the
token (`keys.transit.tokenFile`). `audit key destroy` run from an operator's
shell takes `BAO_ADDR` and `BAO_TOKEN` (or the `VAULT_` names) when no flags
are given.

## Destroying a key

```
audit key destroy --tenant <id> --purpose <p> --by <who> --reason <why> \
    --bucket <b> --sink <writer> --key-provider transit
```

The provider rotates the key once, raises its minimum decryption and
encryption versions past the first, and then trims the first version away.
What is left is a key whose only version nothing uses:

- HMAC, encrypt and decrypt under version 1 are refused, so no pseudonym can be
  recomputed and no sealed identifier opens;
- the engine refuses to lower the minimum version again once it is trimmed;
- creating the key again changes nothing, because it exists.

The key is its own erasure marker, the one the `local` provider writes as a
file. A tenant destroyed before it was ever seen gets a key made and destroyed
at once, so it stays erased when it does appear.

**Backups.** Trimming removes the version from the engine's storage, but a
snapshot taken before still holds it, and restoring that snapshot brings the
key back. Erasure is complete once the last such snapshot has aged out.
Publish that period with the retention terms.

## Testing

The provider's tests run against a real dev server and skip without one:

```sh
docker run -d --rm --name bao -p 8200:8200 -e BAO_DEV_ROOT_TOKEN_ID=root \
    openbao/openbao:2.4.1 server -dev -dev-listen-address=0.0.0.0:8200
AUDIT_OPENBAO_URL=http://127.0.0.1:8200 AUDIT_OPENBAO_TOKEN=root go test ./keys/ ./internal/writer/
```

They cover two writers with separate stores agreeing on a pseudonym, purposes
and tenants not joining, and destroy leaving a tombstone that a fresh provider
respects. Every policy on this page is also run as its own token: a
single-purpose role refuses another purpose and cannot destroy, the writer
seals but cannot open, resolve opens and does nothing else, and the eraser
destroys but cannot delete.
