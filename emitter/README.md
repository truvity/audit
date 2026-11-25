# emitter

The audit emitter: the library inside each emitting process ([0002](../docs/decisions/0002-pieces-and-holders.md)). From one call it builds one message, an OCSF security record as RFC 8785 canonical JSON ([0004](../docs/decisions/0004-the-record-format.md)), checks it against the product's action table, and hands it to atoom's [transport port](https://github.com/truvity/atoom/blob/master/transport/docs/transport-port.md). What one call does, the three calls and what the emitter refuses are on the [emitters page](../docs/reference/emitters.md).

| Language | Name | State |
|---|---|---|
| Rust | the crate `audit-emitter` (`audit_emitter`) in this directory | Built. Moved from sluis's `sluis-audit`, with the same bytes. |
| Go | the module `github.com/truvity/audit/emitter`, package `emitter`, in this directory | Built. Moved from sluis's `kits/go/audit`, with the same bytes. |

The usage part ([0006](../docs/decisions/0006-the-usage-class.md), [0017](../docs/decisions/0017-the-organisation-of-a-usage-record-is-optional.md)) is planned in both.

## Use it from Rust

The crate is consumed from git, by tag, like atoom's crates. Releases follow [releasing](../docs/release.md) ([0016](../docs/decisions/0016-distribution-and-the-lambda-shape.md)); pin the latest tag.

```toml
[dependencies]
audit-emitter = { git = "https://github.com/truvity/audit", tag = "v0.19.0" }
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

## Use it from Go

The module is tagged `emitter/vX.Y.Z`, with the one version of the repository; the first Go tag was v0.17.0 ([0016](../docs/decisions/0016-distribution-and-the-lambda-shape.md)), and releases follow [releasing](../docs/release.md), as for Rust.

```go
import (
	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
	"github.com/truvity/audit/emitter"
)

table, err := emitter.NewTable(rows, schemas) // the product's generated action table
e, err := emitter.New(emitter.Config{
	Product:    "example",
	Release:    "1.4.0",
	Scope:      "prod",
	Component:  "billing",
	Table:      table,
	Transport:  transportv1alpha1connect.NewTransportServiceClient(hc, url, call.ClientOptions()...),
	Pseudonyms: &emitter.Pseudonyms{Mac: hostMac, Label: "example-pseudonym-v1"},
})
err = e.Emit(ctx, emitter.Event{Action: action, Status: emitter.StatusSuccess})
```

`emitter.BuildRecord` is the pure serialiser behind the emitter, for a product's generator of example records: it takes a `RecordInput` and depends on no clock and no `Mac`.

## What a product configures

The emitter speaks audit's neutral model ([0003](../docs/decisions/0003-the-neutral-model.md)). A product maps its own words to it, in its own repository.

| `Config` | What it is | In the record |
|---|---|---|
| `product` | The product's name | `metadata.product.name`; the product object `unmapped.<product>` |
| `release` | The product's release | `metadata.product.version` |
| `scope` | The scope of the log | `metadata.log_name`; the second half of `actor.user.uid` |
| `component` | The emitting component | `actor.user.uid` as `<component>@<scope>`; `application.name` and `service.name` where the class requires them |
| `table` | The product's action table and its generated schemas | Every record is held to the schema of its row |
| `transport_socket` (Rust), `Transport` (Go) | The emit socket of the transport port (Rust), or the client of the port (Go) | In Rust, `Emitter::with_transport` gives another client transport, such as a product's own client that carries its call context |
| `pseudonyms` | The `Mac` of the `pseudonym` purpose and the domain label | `user.uid` or `resources[].uid`, as `p1.<YYYYMM>.<value>` ([0011](../docs/decisions/0011-evidence-encryption-and-pseudonyms.md)) |
| `correlation` | Where the correlation id of the call being served is read; by default atoom's call context | `metadata.correlation_uid` |

- **The `Mac`** is a trait: the emitter holds no key and no client of a key service. A product implements it over the host of atoom's `seal` block that serves it; until that block exists, over its own key service ([0011](../docs/decisions/0011-evidence-encryption-and-pseudonyms.md), "Planned work in atoom").
- **The label has no default.** It is the product's ([0018](../docs/decisions/0018-the-pseudonym-domain-label-is-the-products.md)): a label is part of every pseudonym, so each product names its own, and sluis keeps `sluis-pseudonym-v1`.
- **A summary that stands for several facts** says so in `Event.Facts` (Go): when it is lost, the total of lost facts counts them all ([0005](../docs/decisions/0005-the-security-class.md)). The Rust crate has no such field yet, as sluis's had none.
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
devbox run -- just build test    # both languages
devbox run -- just lint          # golangci-lint, rustfmt and clippy
```

The Go test `TestDifferentialWithRust` rewrites `vectors/jcs-differential.tsv` with `AUDIT_UPDATE_GOLDEN=1`; the Rust test reads the same file.
