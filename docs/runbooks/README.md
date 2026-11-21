# Runbooks

What an operator does, step by step. Each runbook has a Purpose, Preconditions, Before you start, Steps and Afterwards, and each step says what to expect and how to verify it. They are harvested from the runbooks of audit-v1 and rewritten for the design here.

| Runbook | When |
|---|---|
| [Verify a log](verify-a-log.md) | An auditor or a scheduled job checks that a log is intact |
| [Investigate a failed verification](investigate-a-failed-verification.md) | A verification reports a finding |
| [Place and release a legal hold](legal-hold.md) | A matter needs objects kept beyond their retention |
| [Rebuild the index](rebuild-the-index.md) | Search is behind, wrong or gone |
| [Handle quarantine](handle-quarantine.md) | The writer refused messages and kept them whole |
| [Writer or witness outage](writer-or-witness-outage.md) | The write path or a witness is down |

**What is built.** `audit verify` and the writer's refusals exist today in sluis, as `sluis audit verify`, and `audit read`, `audit search` and `audit reindex` do not exist yet. A step that needs a tool that is not built says so and names what to do instead. The commands are written as they will be named here.

**Safe to do.** Every step that only reads says so. A step that changes something names how to undo it.
