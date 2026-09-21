# 0005. Identity tiers and per-purpose pseudonymisation in the split writer

- Status: accepted
- Date: 2026-09-17

## Context

Accountability requires knowing who acted. Data protection requires
minimisation, storage limitation and erasure. Digital-identity regulation
forbids a wallet provider combining usage data about a person across
purposes. These pull in different directions on the same field
(see the regulation survey in this repository's history).

## Decision

The record carries **identifiers, never identity attributes**. Names and
e-mail addresses are resolved at read time by whoever may see them.

Three identifier tiers with different rules:

- **Tenant** identifiers are legal entities and stay in clear in every
  profile for the full retention.
- **Internal** actors (staff, operators) and **machine** actors (services,
  API keys, the system) stay in clear for the security retention.
  Accountability is a legal obligation and erasure does not apply while it
  runs.
- **External** actors and subjects (end users, customers' people) become a
  keyed pseudonym **per tenant and per purpose**, so the security copy and
  the billing copy cannot be joined on a person. The tenant-facing history
  profile may carry the source's own scoped identifier because the tenant
  legitimately knows its users.

Actor kinds are a registry; each kind declares its category, and presets
set the treatment per category. The rule follows the kind, never a field.

Emitters send canonical identifiers and hold no keys. The **split writer**
applies the treatment per profile using per-tenant-and-purpose data keys
from a pluggable key provider, wrapped by KMS or a secret manager's transit
engine, unwrapped into writer memory, never rotated, destroyed by deleting
the wrapped material. Each processor's workload identity may unwrap only
the purposes it needs.

Erasure is key destruction, recorded as `audit.key.destroyed`. Copies under
a legal duty (billing, evidence) are exempt from erasure and say so in the
privacy notice.

## Consequences

- One trusted transform instead of one per writer. The split writer is the
  most privileged component and its own actions are in the security
  profile.
- The wide stream between emitters and the writer holds canonical
  identifiers, so it is encrypted in transit, short-lived and has one
  permitted consumer.
- Resolution of a pseudonym is an API of its own, logged as a security
  event, available to as few roles as possible.

## Alternatives considered

- **Pseudonymise at the emitter.** Keeps identifiers out of the stream, but
  every application would manage keys and the tenant-facing history could
  not show scoped identifiers.
- **Pseudonymise in each projection.** N components with keys and N ways to
  be misconfigured to keep a field.
- **Rotate keys.** Rotation breaks linkability across time for the same
  person, which defeats the security purpose. Destroy, never rotate.
