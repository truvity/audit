# Authentication and authorization

```go
type Authenticator interface {
    Principal(ctx, req) (Principal, error)     // issuer, subject, claims
}

type Authorizer interface {
    Grant(ctx, Principal) (Grant, error)
}

type Grant struct {
    AllTenants bool      // an operator's grant, said out loud
    Tenants    []string
    Profiles   []string
    Operations []Operation // search, facets, get, export, tail, resolve
    From, Until time.Time
    Rule       string    // stamped into the log_access record
}
```

**The zero value grants nothing.** This sketch first had a nil tenant list mean
every tenant. That is the wrong default here: a `Grant` nobody filled in would
then be a grant over the whole archive, and the mistake would look like an empty
struct rather than like a decision. Every tenant is something an operator's
grant says out loud.

The grant reaches a query as one more filter term, not as a check beside it. A
check can be forgotten; a term cannot be, because without it there is no query.

`resolve` is not implied by `get`. It is the operation that undoes the
pseudonymisation, and a grant to read must not carry it.

## Authenticators

- **jwt** (built): a list of trusted issuers, each with a required audience.
  A token's `iss` is read unverified only to choose whose keys to check it
  against; that issuer's verifier then requires the same issuer, so a token
  claiming a trusted issuer on another key's signature fails. Verification is
  gateway-auth's, the same as every fleet service's.

  More than one issuer creates a problem one issuer does not have: two issuers
  can assert the same claim. The staff identity provider and a customer's can
  both put `all:audit:auditor` in `groups`. So a rule carries the issuer it
  trusts that claim from, and with several issuers every rule must name one.
  Claim mapping is therefore per rule, not per issuer: a rule names the claim
  it reads, from the issuer it trusts.
- **trusted-upstream**: reads the principal a gateway forwarded. Bound to
  mTLS from the gateway or an internal JWT the gateway signs. Never a bare
  header — which is exactly what the registry reads today (`Audit-Source`),
  and why the chart refuses to deploy it without a proxy in front. That
  placeholder goes when this lands.
- **none**: tests only.

## Authorizers

- **declarative** (default): configuration mapping claim values to grants.
  A named preset maps the access-roster groups vocabulary.
- **policy-engine adapter** (optional): for an engine whose query plan
  returns a filter and whose decision log names the rule.

## Console

The standalone console is an OIDC relying party (authorization code with
PKCE, cookie session) and also runs behind a gateway in trusted-upstream
mode. Embedded viewers pass their host's bearer.

## Resolution

`resolve` is a separate operation: mapping a pseudonym back to a person
through the identity map. It is granted to as few roles as possible and
emits `audit.get`-class records naming the rule.
