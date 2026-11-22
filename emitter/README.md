# emitter

The audit emitter: the library inside each emitting process ([0002](../docs/decisions/0002-pieces-and-holders.md)). From one call it builds one message, an OCSF security record as RFC 8785 canonical JSON ([0004](../docs/decisions/0004-the-record-format.md)), checks it against the product's action table, and hands it to atoom's [transport port](https://github.com/truvity/atoom/blob/master/transport/docs/transport-port.md). What one call does, the three calls and what the emitter refuses are on the [emitters page](../docs/reference/emitters.md).

| Language | Name | State |
|---|---|---|
| Rust | the crate `audit-emitter` (`audit_emitter`) in this directory | Built. Moved from sluis's `sluis-audit`, with the same bytes. |
| Go | a package in this directory | Planned: the next step of the move. |

The usage part ([0006](../docs/decisions/0006-the-usage-class.md), [0017](../docs/decisions/0017-the-organisation-of-a-usage-record-is-optional.md)) is planned in both.

## Use it from Rust

The crate is consumed from git, by tag, like atoom's crates. **Open:** the release flow of this repository ([0016](../docs/decisions/0016-distribution-and-the-lambda-shape.md)); until the first tag a consumer pins a commit.

```toml
[dependencies]
audit-emitter = { git = "https://github.com/truvity/audit", tag = "v0.17.0" }
```

```rust
use std::sync::Arc;
use audit_emitter::{Action, Config, Emitter, Event, Pseudonyms, Status, Table};

let emitter = Emitter::new(Config {
    product: "example".into(),      // metadata.product.name and unmapped.example
    release: "1.4.0".into(),        // metadata.product.version
    scope: "prod".into(),           // metadata.log_name
    component: "billing".into(),    // actor.user.uid is billing@prod
    table: Arc::new(Table::new(ROWS, SCHEMAS)?), // the product's generated action table
    transport_socket: "/run/example/audit/emit.sock".into(),
    pseudonyms: Some(Pseudonyms { mac: Arc::new(host_mac), label: "example-pseudonym-v1".into() }),
    correlation: None,              // atoom_socket::call::correlation
    now: None,                      // the system clock
})?;
emitter.emit(Event { action, status: Status::Success, ..Event::default() }).await?;
```

## What a product configures

The emitter speaks audit's neutral model ([0003](../docs/decisions/0003-the-neutral-model.md)). A product maps its own words to it, in its own repository.

| `Config` | What it is | In the record |
|---|---|---|
| `product` | The product's name | `metadata.product.name`; the product object `unmapped.<product>` |
| `release` | The product's release | `metadata.product.version` |
| `scope` | The scope of the log | `metadata.log_name`; the second half of `actor.user.uid` |
| `component` | The emitting component | `actor.user.uid` as `<component>@<scope>`; `application.name` and `service.name` where the class requires them |
| `table` | The product's action table and its generated schemas | Every record is held to the schema of its row |
| `transport_socket` | The emit socket of the transport port | `Emitter::with_transport` gives another client transport, such as a product's own client that carries its call context |
| `pseudonyms` | The `Mac` of the `pseudonym` purpose and the domain label | `user.uid` or `resources[].uid`, as `p1.<YYYYMM>.<value>` ([0011](../docs/decisions/0011-evidence-encryption-and-pseudonyms.md)) |
| `correlation` | Where the correlation id of the call being served is read | `metadata.correlation_uid` |

- **The `Mac`** is a trait: the emitter holds no key and no client of a key service. A product implements it over the host of atoom's `seal` block that serves it; until that block exists, over its own key service ([0011](../docs/decisions/0011-evidence-encryption-and-pseudonyms.md), "Planned work in atoom").
- **The label has no default.** Open in [0011](../docs/decisions/0011-evidence-encryption-and-pseudonyms.md): the neutral domain label. A label is part of every pseudonym, so the emitter leaves it to the product until audit fixes one.
- **The table has no default.** A product's table, its generator and the vectors of its own actions stay in the product's repository ([action table and catalogue](../docs/reference/action-table-and-catalogue.md)).

## The vectors

`vectors/` holds the shared vectors that the Rust crate and the Go package both pass, so that both write the same bytes ([0016](../docs/decisions/0016-distribution-and-the-lambda-shape.md)):

| File | What it fixes |
|---|---|
| `record-vectors.json` | Records built from resolved inputs, with their SHA-384; pseudonyms, ids, the inputs of the `Mac`, refusals and the e-mail shape. The product of every case is `example`. |
| `jcs-vectors.json`, `jcs-corpus/` | RFC 8785: the vectors of the record profile, and the corpus of the RFC's author with its licence |
| `jcs-differential.tsv` | The canonical text, or `!refused`, that both languages produce for the corpus and the records. A changed byte is a changed record hash and needs a decision, not a regeneration. |
| `schema-policy-vectors.json` | The policy of the schema documents of a catalogue ([0004](../docs/decisions/0004-the-record-format.md)) |
| `semver-vectors.json` | The releases a record may name |

## Run the tests

```sh
devbox run -- just rust-test     # build and test
devbox run -- just rust-lint     # rustfmt and clippy
```
