# Integrating an application

How an application joins an audit installation: it describes what it records,
sends records to the writer, and shows its trail in its own console. The
installation is deployed on its own ([deploying](deploy.md)); the application
connects to it. With nothing to connect to, an application can instead carry
the whole trail inside itself ([embedding](embed.md)); everything below except
the URLs still applies.

What the application ends up with:

| piece | where | what it takes |
|---|---|---|
| a **catalogue** | in the application's repository, next to the code | a YAML document and JSON Schemas |
| an **emitter** | in the application's process | the registry and writer URLs, and its service account token |
| a **check** | in the application's CI | `audit validate`, `audit check-emitters` |
| the **audit page** | in the application's console | `@truvity/audit/react`, the query service's URL behind the console's own sign-in |

The application holds no keys and no archive credentials. The installation
pseudonymises, locks, signs and indexes; the application only says what
happened.

## 1. The catalogue

The catalogue is the contract between the application and the trail: every
action the application records, and for each one what it is, what it
carries, who it concerns, how long it is kept and how it reads as a sentence.
It is where the application's audit model lives, and writing it is most of
the work of integrating.

```yaml
source: shop            # every action is under this namespace
version: "1.0.0"        # a new version for any change to what a record carries
locales: [en]

actor_kinds:            # who can act, and how profiles treat them
  customer: { category: external }   # pseudonymised where a profile says so
  clerk:    { category: internal }   # kept in clear for accountability
  service:  { category: machine }

target_types:           # what an action can be about
  order: { description: "An order." }

actions:
  shop.order.placed:
    summary: A customer placed an order.
    operation: create                     # create, access, modify, remove, authentication, transfer, restore
    categories: [data_change]             # what frameworks call it
    profiles: [security, history]         # which copies are kept
    target_types: [order]
    delivery: block                       # see below
    data_schema: https://schemas.example.com/shop/order-placed.json
    message: { en: "{actor} placed order {targets_0_id}" }
```

Rules that shape the model — the validator enforces each:

- **An action is a fact, named `source.thing.verb`** in the past tense, one
  per thing that can happen. Not one action with a free-text "type" field.
- **The actor is who acted; the subject is who it concerns;** they differ as
  often as not (a clerk refunds a customer). The actor's **kind** decides how
  each profile treats its identifier, so kinds are about who a party is, not
  what they did.
- **Targets are typed.** An action declares the target types it names; a
  record names each target by type and id. A string that sometimes means a
  client and sometimes an organisation does not fit.
- **Extra data is a schema, not a map.** Every property says its class
  (which profiles keep it) and whether it is personal data. Direct identity
  attributes — names, e-mail addresses — are refused: records carry
  identifiers, and a reader resolves them.
- **Templates are ICU MessageFormat** and name the record's fields with
  underscores: `{actor}`, `{targets_0_id}`, `{data_items}`
  ([the list](../reference/catalogue.md#message-templates)).

The complete vocabulary is the [catalogue reference](../reference/catalogue.md)
and [extension points](../design/extension-points.md).

### Delivery

| delivery | the call | use for |
|---|---|---|
| `block` | returns only once the record is in the archive, or fails | an action that must not happen unrecorded: a privileged sign-in, a key destruction, a billable operation. The caller refuses what it records when this fails. |
| `outbox` | returns once the record is in a local file; the emitter sends it when the writer is reachable | almost everything: nothing is lost across a writer restart |
| `best_effort` | returns at once; may drop under pressure, and says so on a hook | high-volume reads nobody bills or investigates one by one |

### In CI

```sh
audit validate catalogue/shop.yaml
audit check-emitters . --catalogue catalogue/shop.yaml
```

`check-emitters` fails on an action the code emits and the catalogue does not
declare, and on one the catalogue declares and nothing emits. Put each action
name in code once, as a constant or a constructor, so it can see them.

## 2. The emitter

```go
client := auth.TokenFile(os.Getenv("AUDIT_TOKEN_FILE")) // the projected SA token

// Register the catalogue, and do not start if the installation refuses it.
if err := emit.Register(ctx, emit.Registration{
	URL: registryURL, Source: shop.Source, Version: shop.Version,
	Document: document, Schemas: schemas, HTTP: client,
}); err != nil {
	return err
}

outbox, err := emit.OpenFileOutbox("/var/lib/shop/audit-outbox")
if err != nil {
	return err
}
emitter, err := emit.New(emit.Options{
	Source: shop.Source, Catalogue: shop,
	Sink:   sink.NewClient(client, writerURL),
	Outbox: outbox,
})

// Wherever an action happens:
err = emitter.Record(ctx, &record.Record{
	Action:  "shop.order.placed",
	TenantId: tenant,
	Actor:   &record.Actor{Kind: "customer", Id: customerID},
	Targets: []*record.Target{{Type: "order", Id: orderID}},
	Outcome: &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
})
```

- **Identity.** The emitter presents the pod's projected service-account
  token (audience `audit` by default) on every call; the writer verifies it
  and stamps the service account as the record's observer. The installation's
  `workloadIdentity.workloads` maps that service account to the source, which
  is what lets it register the catalogue.
- **Request context.** Wrap the HTTP handler in `emit.Middleware(hops)` and
  every record made while serving a request carries its client address, user
  agent, request id and trace id.
- **A component that reports for another** (a controller acting for the
  service) emits with its own token; the observer then names the component
  that saw it happen.
- **Tenant.** A record belongs to the customer organisation it happened for.
  An installation's own events use `@platform`.

The complete walkthrough, compiled on every run of the gate, is
[`examples/emit`](../../examples/emit/main.go) and [emitting records](emit.md).

## 3. The audit page

`@truvity/audit/react` renders a profile's records as sentences, with search,
counts, detail, integrity and live updates. It holds no credentials: it asks
through a transport the host gives it.

The recommended wiring keeps the query service's token out of the browser:

```
browser ──(the console's session)──▶ console backend ──(a token for the audit installation)──▶ query service
         /audit/audit.v1.QueryService/*                   Authorization: Bearer …
```

- **Console backend**: proxy `/audit/` to the query service, adding a bearer
  token the query service trusts — issued by the identity provider its grants
  file names, for the signed-in person, with the installation's audience.
  Refuse the proxy to anyone not signed in.
- **Grants**: the query service's grants file names that issuer and says who
  may read what; with the `access-roster` preset, groups named
  `<scope>:audit:<role>` grant themselves ([access](read.md#access)).
- **Browser**:

```tsx
import { createConnectTransport } from "@connectrpc/connect-web";
import { createQueryClient } from "@truvity/audit";
import { AuditProvider, AuditView } from "@truvity/audit/react";
import shop from "./audit-sentences.json"; // `audit messages catalogue/shop.yaml`

const audit = createQueryClient(createConnectTransport({
  baseUrl: "/audit", jsonOptions: { useProtoFieldName: true },
}));

<AuditProvider client={audit} sentences={[shop]}>
  <AuditView profiles={profilesThisPersonMayRead} permalink={(p, id) => `/audit/${p}/${id}`} />
</AuditProvider>
```

- **Sentences**: `audit messages catalogue/shop.yaml > audit-sentences.json`
  at build time; the component's own actions (`audit.*`) are built in.
- **Profiles**: the page shows the ones the host passes — normally derived
  from the person's groups, the same ones the grants are.

## 4. Checking the integration

- Against the installation, with a token that may read:
  `audit conformance --query <url> --profile security` holds the query
  service to its contract over the records it holds.
- `audit verify --profile security --last 24h …` shows the chain covers what
  the application wrote.
- Stop the writer and perform a `block` action: it must fail. Perform an
  `outbox` action: it must succeed, and appear once the writer is back.

## What not to do

- Do not pseudonymise or hash identifiers in the application. The writer does
  it per profile with keys the application never holds; an application-side
  hash is either reversible or unlinkable, and is never both correctly.
- Do not build action names at run time. `check-emitters` cannot see them,
  and the catalogue stops describing the code.
- Do not read the archive bucket from the application. Read through the query
  service, which applies grants and records the read.
