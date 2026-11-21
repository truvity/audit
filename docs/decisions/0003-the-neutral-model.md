# 0003. The neutral model: log, project, tenant, scope

Status: accepted, 2026-11-21

## Context

The design grew inside an identity product, and it used that product's words: a boundary, a tenant, an actor, a sluis field. Other products emit too: a session service, a certificate tool, and any product that sells by use. They must not have to speak another product's language to use audit.

## Decision

audit has its own terms. A product maps its own words to them.

| Term | Meaning |
|---|---|
| **Scope** | The unit of keys and policy. One audit serves one scope. Evidence of one scope is never sealed, indexed or read with the keys of another. |
| **Project** | One emitting unit of a scope: a product, or the infrastructure the scope runs on. A project has its own bucket, writer, log keys, stream and bindings. |
| **Log kind** | A kind of log a project keeps. This design defines `audit` (the security class) and `usage` (the usage class). |
| **Log** | One tiled log of one kind, written by one writer instance. "The log" means one log of one kind. |
| **Tenant** | The party whose records and keys they are. A tenant may have records in several projects of a scope. Its keys are the scope's, whatever the project. |
| **Product** | The software that emits. Its name is `metadata.product.name` in each record. |
| **Component** | A part of a product that emits: an actor, a service. Its name is the actor of a record. |

### Rules of the model

- **A scope has one audit.** A product that emits in two scopes has a project in each.
- **A tenant belongs to one scope.** A record that concerns no tenant belongs to the scope's own tenant, the operator's.
- **The scope, the project and the log are the log's own, not fields of the record.** The scope appears once in the record as `metadata.log_name`, which the writer checks against the log. The project and the log appear in no field. The writer refuses a record whose `metadata.product.name` its project does not declare.
- **The origin line of a checkpoint is `<host>/<scope>/<project>/<kind>/<log>`.**
- **A bucket belongs to one project, with one prefix per log kind:** `audit/` and `usage/`, written by the project's writer. A product may keep a `billing/` prefix in the same bucket. No piece of audit writes it.
- **A product's own facts go into one OCSF object, `unmapped.<product>`,** where `<product>` is the product's name. [0004](0004-the-record-format.md) says how.

### How sluis maps

sluis maps its **boundary** to a scope, its **actors** to components, and its `unmapped.sluis` object stays as it is, because `sluis` is the product's name. sluis is a project of each scope it runs in. A product that keeps its audit trail beside sluis is a project of the same scope. [sluis](https://github.com/truvity/sluis) records the mapping on its side.

### Open

- **Open: the tenant registry.** A tenant must be known to the scope before its first record, so that a mistyped tenant id never creates one. In sluis a tenant is created by its exchange actor, on a product's push. audit alone has no such actor. Which component keeps the registry, and the call a product makes to push a tenant, are not decided. The rule that a record for an unknown tenant is held in the transport and raises an alarm stands ([0013](0013-access.md)).
- **Open: further log kinds.** sluis keeps an issuance log of its certificate tool under its own project, in another C2SP format. Whether such a log is declared through the same configuration kind ([0015](0015-configuration-kinds.md)) is not decided. It stays sluis's until then.

## Consequences

- A new product needs a name, a project and a catalogue ([0004](0004-the-record-format.md)). It does not need a change in audit.
- sluis changes a few words in its mapping. Its records keep their shape, since the product object was already named after the product.
- The glossary ([reference/glossary.md](../reference/glossary.md)) holds the terms, and the other pages use no others.

## Roads not taken

- **Keep the identity product's terms.** Other products would have to speak a boundary and an actor to use an audit.
- **Name the product object `unmapped.audit` or `unmapped.extension`.** Facts of two products in one object, or a name that says nothing about whose schema it is.
- **A project per tenant.** A writer and a key for each tenant, and no home for a record that concerns two tenants.
