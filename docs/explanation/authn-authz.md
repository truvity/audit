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
- **workload tokens** (built, replacing trusted-upstream): a workload in the
  application's namespace presents its projected service-account token. The
  receiver verifies it as a JWT from the cluster's own OIDC issuer. The subject
  is the service account, which the kubelet vouches for and the workload cannot
  choose. The receiver stamps it on each record as the observer, and it is also
  what `RegisterCatalogue` is authenticated by: there is no registry service,
  because an installation hears from one application
  ([0011](../decisions/0011-one-installation-per-service-or-product.md)).

  The earlier sketch had a gateway forward the principal, bound to mTLS or a
  gateway-signed token, and a registry service read an `Audit-Source` header
  meanwhile. The header is gone. mTLS needs a certificate for every caller and a mesh or
  cert-manager to issue them; a projected token needs neither, and is verified
  by the same code as a person's token. mTLS remains possible behind the same
  interface for a deployment that already runs a mesh.
- **none**: tests only.

### On the SQS path

A record that arrives through the ingest queue (the writer Lambda; a receiver or
a job with `sink.sqs`) carries **no verified identity of its caller**. There is
no token on that path: SQS delivers the message body and attributes the sender
chose, and the writer, which has no caller to verify, attributes the record to
itself (the observer is the writer's own identity, as for a consumer reading
a stream). Whoever may send to the queue may therefore put a record into the
trail under any `source` and `actor` a catalogue admits. The writer's
authenticity on this path rests on **the queue policy's sender principals**
and on nothing else.

- The Pulumi library makes that list required: `Ingest.Senders`. The queue policy
  allows `sqs:SendMessage` to those principals and **denies it to every other**
  (`aws:PrincipalArn`), so an identity policy elsewhere in the account is not
  enough to send. `Ingest.AnySenderInAccount` is the spelling of "any principal
  of this account with `sqs:SendMessage` may", for a trial; with it the trail
  cannot say which of them sent a record. The two are refused together.
- Name one principal per sender and no more: a role per workload that sends
  (the receiver, the query service, the notary), each with `sqs:SendMessage` on
  the queue and nothing else of it. A principal in another account is named the
  same way.
- **Not done, and why.** The observer is not taken from a message attribute. A
  message attribute is the sender's own claim, so stamping it would let any
  permitted sender record as any other. SQS does stamp a `SenderId` (the sending
  role's unique id and the session name) that the sender cannot choose, and a
  sound version would map each named sender's role id to an observer name. The
  library cannot do that: a role's unique id exists only once the role does and
  is not readable for a role in another account, and the session name is chosen
  by the caller. Until a sender map can be tied to the queue policy's own list,
  the records of this path name the writer, and a deployment that must tell its
  senders apart gives each its own queue.
- The HTTP and stream paths are different: there the receiver verifies the
  caller's token and stamps the observer
  ([workload tokens](#authenticators)).

## Authorizers

- **declarative** (default, built): configuration mapping claim values to
  grants, plus presets. An authorizer answers with every grant the caller
  holds, and `auth.Effective` picks per request the ones covering the profile
  and operation asked for: tenants union within that profile and never
  across profiles, a time window never unions. The sketch had the first
  matching rule win; that made a later rule a grant the file says exists and
  the service ignores.
- **access-roster preset** (built): reads `<scope>:audit:<role>` from the
  groups claim — the estate's grant grammar, split by the roster's own
  `policy.SplitGroup`, so there is one parser. Roles bind to the presets a
  profile is composed from, not to profile names; `viewer` must be
  tenant-scoped; `resolve` and time-boxed grants are explicit rules only.
  The reference lists the role table.
- **policy-engine adapter** (optional): for an engine whose query plan
  returns a filter and whose decision log names the rule.

## The Audit page

The page is hosted by the application's own console and passes the token that
console already holds — normally the one its gateway issued for the person's
session. There is no console of this component's own to authenticate anybody:
see [the Audit page](audit-page.md).

## Resolution

`resolve` is a separate operation: mapping a pseudonym back to a person
through the identity map. It is granted to as few roles as possible and
emits `audit.get`-class records naming the rule.

Where the deployment runs no pseudonymisation keys — `keys.provider: none`
(or no `keys` block), the default — there is nothing to resolve, and the operation is refused as
unimplemented rather than answered emptily
([0013](../decisions/0013-no-pseudonymisation-keys-by-default.md)). A grant
may still name it; it will never be exercised.
