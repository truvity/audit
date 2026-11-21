# 0007. A meter has a name, a unit and a kind, and only declared outcomes become usage evidence

Status: accepted, 2026-11-21

## Context

The [usage class](0006-the-usage-class.md) says that an action may carry meters, with units, and that an undeclared meter is refused. It does not say what a meter is, which outcomes of an action become usage, what happens to a meter over releases, or which time a usage fact counts at. The readers of the usage log need those answers to agree.

- **The product** builds its billing log from the usage facts. audit closes no period and writes no statement.
- **A limit check** reads counters made from usage facts.
- **Anyone granted the usage log** without the security log totals it by their own rules.

audit-v1 counted a metered action's success only, billed a gauge as the average of hourly samples, and treated a renamed meter as a new one. This design keeps those three rules.

## Decision

### The meter is declared

- **A meter is a document of the configuration kind `Meter`:** a name, a unit and a kind ([0015](0015-configuration-kinds.md)). It is a reviewed, signed document, so a new meter is a reviewed change. The name is the CloudEvents `type` of its events. The unit is a lower-case word such as `requests` or `bytes`. Amounts are signed 64-bit integers.
- **The action table names the meters an action may carry** ([0004](0004-the-record-format.md)). The emitter refuses a meter that no `Meter` document declares and an amount out of bounds.
- **The kind is `count` or `gauge`.** A count is an event: its amounts add up over any span. A gauge is an absolute sample, such as stored bytes, never a change. A periodic action that is audited as an action of its own emits it. The meter declares how the samples of a span combine: `avg`, `max` or `last`. audit picks none of them. The product chooses.

### Only declared outcomes become usage evidence

- **A meter declares the outcomes it records,** and the default is `success`. A refusal by policy or by a limit is never metered use. An action priced by attempt lists the failure it records.
- **The emitter adds the usage part only for a listed outcome.** The security part is always written, so a failed call is visible and carries no usage.

### A meter never changes

- **A meter's name, unit and kind are fixed from its first release.** Renaming it, or changing its unit or kind, is a new meter. The old meter stays in the table and in the `Meter` documents, marked retired. A retired meter can still be read from old events, and no release emits it.
- **A check compares the action table with the previous release's, and the `Meter` documents with the ones in force,** before anything is published. It fails when a meter was renamed, retyped or dropped without being retired. Within a major version the table only gains meters.
- **A reader never merges two meters.** A product's bill for a renamed meter shows both lines.
- **OpenTelemetry metrics are never a source of usage.** Telemetry is sampled and may be dropped. The usage stream is neither.

### The period of an event

- **audit closes no period.** A usage fact carries two times: the time of use, which is the CloudEvents `time` that the emitter sets, and the time the writer recorded it, which is the writer's stamp in the clear envelope of the entry and never the emitter's. A reader that assigns facts to periods uses its own calendar. This record fixes only which of the two times a reader trusts, so that every reader of the log agrees.
- **A fact counts at the time of use** when that time is not earlier than the recorded time by more than 72 hours, and not later by more than 5 minutes. A spool that was down delivers its events with their own times, up to that bound.
- **Otherwise the fact counts at its recorded time,** and the reader marks it `time_adjusted`. A wrong clock, or a replay of old events, cannot place usage in a span it was not used in.
- **The bounds are built in.** The 5-minute future bound assumes synchronised clocks. It is a ceiling chosen for that and not a measurement.

### Built and planned

Planned: the `Meter` kind, the table fields for meters, the comparison check and the period rule in readers. The documents of the usage class are in [reference/usage-event.md](../reference/usage-event.md).

## Consequences

- The readers of the usage log agree on which facts count and at what time, from one rule.
- A product cannot rename a meter to fix a name. The old one stays for as long as its events are kept. The table and the `Meter` documents grow with retired meters.
- A gauge's `avg` needs samples at a steady rate. The cadence of the periodic action is the product's, and a gap shows as fewer samples.
- A fact near a period border may count in the previous period when the writer was unreachable for longer than 72 hours. The product's billing log can show it as a late line.
- Billing a failure is possible, but it is a deliberate line in a product's table that a reviewer sees.
- audit carries no billing calendar, grace period or close job.

## Roads not taken

- **Decimal quantities, as audit-v1 had.** Exact for money in a string, but every sum needs a decimal library and a rounding rule. Integers in a unit fine enough for the meter are exact on every store.
- **Letting a meter be renamed through an alias.** The bill reads well, but "the sum equals the facts" then needs a map that every reader must hold.
- **Trusting the emitter's time alone.** An emitter with a wrong clock moves usage across periods, and a replayed old event is counted in the past.
- **Using the recorded time alone.** Immune to clocks, but a spool that was down for an hour at midnight moves a month's last minutes into the next month.
- **A period, a calendar or a close in audit.** One more calendar for every reader to share and a job to run. The product owns the contract with its customers and so owns the calendar.
- **Recording every outcome, or a meter per outcome.** The first meters refused calls. The second multiplies meters for what a flag in the declaration says.
- **A gauge as deltas.** A lost delta corrupts every later value. An absolute sample heals on the next one.
