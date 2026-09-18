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

- **jwt**: a list of trusted issuers, each with its JWKS URL, audience and
  claim mapping.
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
