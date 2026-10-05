# Erase a tenant's pseudonymisation keys

## Purpose

Crypto-shred a tenant's pseudonyms for the purposes not under a legal duty, when a
tenant asks for erasure.

## Preconditions

- The installation configured a key provider. With `keys.provider: none` (the default)
  there is nothing to destroy: erasure of an end user is the application's, in its own
  database, and after that the archive's opaque identifier resolves to nobody
  ([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)).
- You act as the person allowed to erase: under `transit` a human role whose policy
  reaches `transit/keys/<prefix>.*`; the writer's cannot.
- A writer to record through (`--sink`).

## Before you start

- **Check holds.** The command checks them itself and refuses while one covers the
  tenant's copies, naming the hold and why it was placed; `audit hold list` is still how
  you look before you start. Crypto-shredding a tenant under legal hold destroys
  evidence that may not be destroyed.
- **The order is deliberate: the key is destroyed, then the erasure is recorded.** A
  record written first could claim an erasure that then failed. If the record cannot be
  written the command says, loudly, that the key is already gone and must be accounted
  for by hand.
- **Billing and evidence copies stay** under GDPR Art. 17(3)(b); the security and
  history copies become unlinkable.
- **Keys are never rotated on a schedule**, so there is no "rotate instead" answer
  ([key custody](../explanation/key-custody.md#never-rotated)).

## Steps

1. **List holds.** `audit hold list --bucket <b>`.

2. **Destroy.**

   ```
   audit key destroy --tenant <id> --purpose <p> --by <who> --reason <why> \
       --bucket <b> --sink <writer> \
       --key-provider transit                  # BAO_ADDR, BAO_NAMESPACE, BAO_TOKEN from your shell
       # or: --key-root <file> --key-dir <dir> # the local provider
   ```

   Expected: the key is gone and `audit.key.destroyed` is recorded automatically.
   Verify: resolve for that tenant and purpose is refused. Roll back: **none**; a destroyed
   key cannot be restored, which is the point.

## Afterwards

Tell the tenant what was destroyed and what stays and why. Run the destroy per purpose
that is not under a duty.
