# Reference

Reference pages say what the [decision records](../decisions/README.md) fix, in tables. A record decides, and a reference page lists. Where a page and a record disagree, the record is right and the page has a bug.

| Page | What it holds | Record |
|---|---|---|
| [Record format](record-format.md) | The fields of a record, the envelope, the canonical rules and the bounds | [0004](../decisions/0004-the-record-format.md), [0011](../decisions/0011-evidence-encryption-and-pseudonyms.md) |
| [Action table and catalogue](action-table-and-catalogue.md) | The table a product writes, the catalogue it publishes, the value types, the vectors | [0004](../decisions/0004-the-record-format.md) |
| [Log layout and checkpoints](log-layout.md) | The objects of a log, the checkpoint history, the limits and the tests | [0008](../decisions/0008-the-log.md), [0009](../decisions/0009-witnesses-and-verification.md) |
| [Reader API](reader-api.md) | The calls, the filter grammar, cursors, facets, proofs, export | [0012](../decisions/0012-search-and-the-reader-api.md) |
| [Emitters](emitters.md) | The Go and Rust emitters: calls, checks and bounds | [0005](../decisions/0005-the-security-class.md), [0006](../decisions/0006-the-usage-class.md) |
| [Writer state](writer-state.md) | The state object, the id segments, the clock rules, the arithmetic | [0008](../decisions/0008-the-log.md) |
| [Usage event](usage-event.md) | The attributes of a usage event and the rules of its time | [0006](../decisions/0006-the-usage-class.md), [0007](../decisions/0007-meters.md) |
| [`audit verify`](audit-verify.md) | What the command checks and the closed reasons | [0009](../decisions/0009-witnesses-and-verification.md), [0014](../decisions/0014-the-front-the-console-page-and-audit-verify.md) |
| [Configuration kinds](config-kinds.md) | The four kinds and their fields | [0015](../decisions/0015-configuration-kinds.md) |
| [Compliance profiles](compliance-profiles.md) | The five profiles, their floors and clauses | [0010](../decisions/0010-lock-modes-retention-and-compliance-floors.md) |
| [Threat model](threat-model.md) | The threats audit is designed against | all |
| [Telemetry and alerts](telemetry-and-alerts.md) | What the pieces publish and what to alert on | [0005](../decisions/0005-the-security-class.md), [0008](../decisions/0008-the-log.md) |
| [Glossary](glossary.md) | The terms of the model | [0003](../decisions/0003-the-neutral-model.md) |
| [Requirements](../requirements/README.md) | The testable rules | all |

Each page says what is built and what is planned. "Built" means built in sluis or atoom today. The code moves to this repository in steps.
