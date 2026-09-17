# Authentication and authorization

```go
type Authenticator interface {
    Principal(ctx, req) (Principal, error)     // issuer, subject, claims
}

type Authorizer interface {
    Grant(ctx, Principal) (Grant, error)
}

type Grant struct {
    Tenants    []string  // nil = all
    Profiles   []string
    Operations []string  // search, facets, get, export, tail, resolve
    Window     *TimeRange
    Rule       string    // stamped into the log_access record
}
```

## Authenticators

- **jwt**: a list of trusted issuers, each with its JWKS URL, audience and
  claim mapping.
- **trusted-upstream**: reads the principal a gateway forwarded. Bound to
  mTLS from the gateway or an internal JWT the gateway signs. Never a bare
  header.
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
