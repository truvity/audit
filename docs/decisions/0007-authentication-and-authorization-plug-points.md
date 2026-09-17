# 0007. Pluggable authentication and authorization with declarative defaults

- Status: accepted
- Date: 2026-09-17

## Context

Readers arrive with tokens from different issuers: an internal access
issuer for staff, a product identity provider for tenant administrators,
possibly a customer's own provider. Authorization is not only allow or
deny: it must produce a filter term (tenants, profiles, time window,
operations) that is added to every search. Policy engines were considered:
one with a query-plan API that emits such filters, one embedded library
without partial evaluation, and a relationship-based one.

## Decision

The query service never knows about issuers. An `Authenticator` yields a
normalised principal. Three ship: a multi-issuer JWT verifier with per-
issuer JWKS, a **trusted-upstream** mode that reads what a gateway already
verified and is bound to a real trust boundary (mTLS from the gateway or an
internal JWT it signs), and none for tests. In a fleet with an Envoy-based
gateway the JWT providers there do the work and the service runs in
trusted-upstream mode.

An `Authorizer` takes the principal and returns a **grant**: tenants (all
or a list), profiles, operations (search, get, export, tail) and a time
window. The grant becomes a filter term in every query. The default
authorizer is a declarative mapping in configuration from a claim path to
grants, with a named preset for the access-roster groups vocabulary. One
optional adapter is provided for a policy engine whose query plan can hand
the searcher a filter and whose decision log names the rule.

The granting rule is stamped into the `log_access` record every read emits.

The standalone console is a native OIDC relying party (authorization code
with PKCE, cookie session) and also runs behind a gateway in trusted-
upstream mode. No bundled proxy.

## Consequences

- Any OIDC provider works without code.
- An external assessor gets a time-boxed grant that expires by itself.
- Embedded viewers pass their host's bearer and do no authentication of
  their own.

## Alternatives considered

- **An embedded policy library.** No partial evaluation, so tenant scope
  must be enumerated per request; no decision record; weak testing story.
- **A relationship-based engine.** Answers which tenants well, not time
  windows or profile rules; another service and store.
- **A bundled proxy.** Ties the public repo to one deployment shape.
