# 0013. Access: a Cedar library with audit's own schema, checked twice

Status: accepted, 2026-11-21

## Context

Who may read what must be decided on three axes: the project, the log kind and the tenant. A finance reader may read a project's usage and not its security records. A tenant reads its own records. A scope's auditor reads every project and changes nothing. A decision must hold even if one check has a flaw.

In sluis, a policy actor decides and seals a grant that its key service checks. audit must run without sluis, so it cannot always call that actor. atoom already shares how Cedar policies load and how they are tested ([atoom 0011](https://github.com/truvity/atoom/blob/master/docs/decisions/0011-cedar-as-a-library.md)): Cedar is used as a library, with the signed bundle, the loading rules, loaders in Go and Rust and a decision test set. It is the one exception to the rule that a port is always called through its contract.

## Decision

### Audit decides with Cedar as a library

- **audit uses the shared Cedar library and its loaders with its own schema.** It does not call sluis's policy actor. sluis's grants and claims are one possible input: the reader may map them to entities.
- **The policy bundle is atoom's:** the schema, the policies, the templates and the links, in one signed document, read through the `source` port, and loaded by the rules of atoom 0011: verify the signature before parsing, validate in strict mode, compile once and swap atomically, keep the last good set when a reload fails, refuse a cold start with no valid set, no I/O during a decision, audit each decision with the digest of the set it used.
- **The caller gives the entities.** The reader gathers the principal, the resource and their ancestors for each call.
- **The schema is audit's.** It declares the principals (a person, a service), the resource `Log` with its project, kind and tenant, and the actions: search, get, export, tail, reverse a pseudonym, rebuild the index and read the health of a log. **Open: the names of the entity types and actions** are written with the first policy bundle.
- **The reader decides every call on project, log kind and tenant.** The decision yields a **grant**: the projects, the log kinds and the tenants, the operations among search, get, export and tail, a time window, and a full or envelope view. The grant is a mandatory filter of every query, so a query cannot be made wider than it.
- **The writer, the indexer and the witness decide their own calls too,** with the same library and their own rules: the writer's rules let it write its own project's bucket and Seal under `evidence`; the indexer's let it Open under `evidence` and write the index; the witness's let it cosign.

### Who the caller is

- **The reader verifies the caller's token against a trusted token source that the configuration declares:** the issuer of the scope's identity product, or a product's own issuer. It decides from the token's claims, for example the tenant and the role. A token whose audience is not the front's registered client is refused. A token for one scope's audit cannot be used for another's.
- **Emitters are known by their platform identity,** not by a token ([0002](0002-pieces-and-holders.md)).

### Two independent checks

1. **The decision of the reader,** as above. It filters every query and every call.
2. **The access control of the stores below it:**
   - the blob store's own control on the bucket and the prefix: IAM on AWS, scoped temporary credentials where the store has them, and file permissions per uid on a local file system. Every credential that reaches a project's bucket is scoped to it: a project's writer writes only its own bucket, the witness only its cosignatures, and a person's direct read role names the bucket and the prefixes of the log kinds it was granted;
   - the `seal` host's rules and the authenticated data of each seal ([0011](0011-evidence-encryption-and-pseudonyms.md)): the writer may Seal and not Open, and a record bound to one tenant, project and log does not open under another;
   - on a PostgreSQL index, row-level security ([0012](0012-search-and-the-reader-api.md)).

   The reader's grant filter, the database's rules, the seal host's rules and the bucket's control each have to fail before a reader reaches another project's records, and each needs tests with the others switched off.

**Open: the second check on Open.** In sluis the key service refuses an Open under a tenant's `evidence` key unless a grant that a separate decision point signed covers that tenant, so a flaw in the reader alone cannot open another tenant's records. audit without that decision point has the seal host's rules and the bound data, which stop a cross-tenant open of a record bound elsewhere, but do not stop a reader that is compromised and asks for the right tenant. Whether audit carries a signed grant from the Cedar decision to the seal host, and which component signs it, is not decided. The same question covers a per-tenant grant for the writer's Seal.

### Bindings

The bindings are Cedar policies in the bundle:

| Binding | Reads | May change |
|---|---|---|
| **Team** of a project | The project's logs | May ask for operations on them, such as an export or a rebuild of the project's partitions of the index |
| **Auditor** of a project | The project's logs | Nothing |
| **Scope auditor** | Every project of the scope | Nothing |

- **A binding names log kinds as well as projects.** A finance reader may be granted a project's `usage` without its `audit`, in the decision and in the prefixes of its bucket credential.
- **A tenant reads its own records** in the projects whose policy allows it, and only under grants for its own tenant.
- **The operator's own view of a tenant's records is the envelope.**
- **Every read is audited,** with the rule that granted it ([0005](0005-the-security-class.md)).

### Audit alone

audit runs without sluis. In that shape:

- **The reader verifies the product's token** against the issuer that the configuration declares, and decides from its claims, so the two checks hold in every shape.
- **A tenant must be known to the scope before its first record.** A record for a tenant the scope does not know is held in the transport and raises an alarm, so a mistyped tenant id never creates a tenant. How a product pushes a tenant is Open ([0003](0003-the-neutral-model.md)). A push is idempotent by request id.
- **A product that later adopts an identity product for sign-in** keeps its project, buckets, logs and keys. Nothing is migrated.

### Built and planned

Built: a reader that asks for a grant on every call and applies it as a filter, the writer's and indexer's grants, in sluis with sluis's policy actor. Planned: the Cedar loaders of atom (plan step B5), audit's schema and the first bundle, the token-source declaration, the held records of an unknown tenant, row-level security.

## Consequences

- audit runs where sluis does not. Where both run, there are two policy setups, which is the price of independence.
- A decision costs a local evaluation and no network call. A read costs a decision and adds a record to the log.
- The Open item above is a real gap against sluis's design. Until it is decided, a deployment that needs the stronger property runs audit beside sluis.

## Roads not taken

- **Always call sluis's policy actor.** One policy point, but audit would depend on sluis.
- **A different engine (OPA, Casbin).** A separate design. It is not a second implementation of this one.
- **Authorisation in the reader without Cedar, by tokens alone.** A second authorisation path, and no common test set.
- **Audit alone read by operators only.** Tenants could not see their own records, which many products need.
- **Create a tenant's keys on its first record.** A mistyped tenant id would create a tenant, and there would be no clear moment to erase it.
