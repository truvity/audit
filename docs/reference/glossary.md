# Glossary

The terms of the model ([0003](../decisions/0003-the-neutral-model.md)), and the other terms that the records use. The other pages use no others.

| Term | Meaning |
|---|---|
| **Action** | One thing a product's component does that is audited. It has a name, which is `metadata.event_code`, and a row in the product's action table. |
| **Action table** | A product's table of its actions, in code. It is the source of the schemas ([reference](action-table-and-catalogue.md)). |
| **Auditor** | A binding that reads a project's logs and changes nothing. The **scope auditor** reads every project of the scope. |
| **Best effort** | An emit that does not wait and may be lost, with the loss counted ([0005](../decisions/0005-the-security-class.md)). |
| **Blob store** | A store of objects under paths, reached through atoom's blob port. |
| **Catalogue** | The action table of a release with its generated schemas, published as configuration ([0004](../decisions/0004-the-record-format.md)). |
| **Checkpoint** | A signed note that names a log, its size and its root hash. |
| **Component** | A part of a product that emits: an actor, a service. |
| **Cosignature** | A witness's signature on a checkpoint. |
| **Emitter** | The library in an emitting process. It builds one message from one call. |
| **Envelope** | The clear part of an entry: the id, time, tenant, class, outcome and actor type, and the writer's stamps. |
| **Evidence** | The records of a log, sealed under the tenant's `evidence` key. They outlive the tenant's personal data. |
| **Fence** | The write-once rule of the blob store: an object is created only if absent. |
| **Front** | The audit's only edge listener. It holds no key, secret or state. |
| **Grant** | The result of a decision: the projects, log kinds and tenants a call may reach, its operations, its time window and its view. |
| **Index** | A search database derived from the log. Never evidence. |
| **Log** | One tiled log of one kind, written by one writer instance. |
| **Log kind** | A kind of log a project keeps: `audit` or `usage`. |
| **Lock mode** | The blob backend's lock: `none`, `removable` or `compliance`. |
| **Message** | What the emitter hands to the transport: an id, a record and its usage events. |
| **Meter** | A named, fixed measure of use, with a unit and a kind ([0007](../decisions/0007-meters.md)). |
| **Product** | The software that emits. Its name is `metadata.product.name`. |
| **Product object** | `unmapped.<product>`: a product's own facts that OCSF has no attribute for. |
| **Profile** (compliance) | A floor that a standard sets, as a data file ([0010](../decisions/0010-lock-modes-retention-and-compliance-floors.md)). |
| **Project** | One emitting unit of a scope, with its own bucket, writer, log keys and stream. |
| **Pseudonym** | A keyed stand-in for a person, `p1.<epoch>.<value>`, made under the tenant's `pseudonym` key. |
| **Quarantine** | The place in a project's bucket where a refused message is kept whole. |
| **Reader** | The piece that serves the audit API. |
| **Scope** | The unit of keys and policy. One audit serves one scope. |
| **Seal** (verb) | To encrypt a record under a tenant's key through atoom's `seal` block. Also the batch's seal: the signing of a checkpoint. |
| **`seal` block** | atoom's block for keys: purposes, data keys, wraps, `Seal`, `Open`, `Mac` and signing by purpose. It was called `keyring` in the first plan. |
| **Seal entry** | The entry that ends a batch, in clear, with its counts and the clock check. |
| **Security class** | The class of records for what happened. Loss is counted ([0005](../decisions/0005-the-security-class.md)). |
| **Split view** | Different readers shown different histories of one log. Only a witness exposes it. |
| **`state` block** | atoom's block for signed configuration: addresses, kinds, layered reads. audit declares its kinds to it. |
| **Tenant** | The party whose records and keys they are. |
| **Tile** | A static object of a `tlog-tiles` log: an entry bundle or a hash tile. |
| **Transport** | The queue that carries messages from emitters to the writer, through atoom's transport port. |
| **Usage class** | The class of records for what was used. Never dropped ([0006](../decisions/0006-the-usage-class.md)). |
| **Verifier** | Whoever checks a log from the store alone, with the keys they pinned. |
| **Witness** | A party that cosigns checkpoints. Optional. |
| **Writer** | The piece that validates, deduplicates and seals messages into the logs of one project. |
