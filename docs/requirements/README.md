# audit requirements

[`audit.yaml`](audit.yaml) is the source of truth for what audit must do. Each requirement is one clause with one [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) keyword and one testable behaviour. The records explain why. The rows say what to test.

The rows moved here from the area `AUDIT` of [sluis's requirements](https://github.com/truvity/sluis/blob/main/docs/requirements/audit.yaml). This page says what changed on the way.

## Fields of a row

| Field | Rule |
|---|---|
| `id` | `REQ-AUDIT-nnn`. Stable, and never reused. |
| `area` | `AUDIT` |
| `split_from` | The id of the longer row this clause was cut from. Clauses sit next to their parent, so ids are not always in order. |
| `statement` | One clause with one keyword: MUST, MUST NOT, SHOULD or MAY. The keyword equals `level`. |
| `rationale` | Why the rule exists |
| `level` | `MUST`, `SHOULD` or `MAY` |
| `status` | `done`, `partial` or `planned`, as the rule stood in sluis when it moved. The code is in sluis and atoom until it moves here. `withdrawn` is for a row that no longer applies. |
| `owner` | `audit`, or `sluis` for a row that stays in sluis |
| `runtimes` | Absent when the row applies in every shape of installation. Otherwise a list of `host`, `kubernetes` and `serverless`. |
| `verification` | How the rule is proved: `test-vectors`, `integration-test` or `manual`, with a short description. A MUST has at least one. |
| `stays_in_sluis`, `link` | For a row owned by sluis: why it stays, and the link to its row in sluis |

A row that no longer applies gets `status: withdrawn` and `superseded_by`. It is never deleted. Ids are not renumbered.

## What changed in the move

- **Ids are kept.** All 181 live rows keep their sluis id, so a link or a test name keeps working. This is the clearest re-keying: none.
- **The 11 withdrawn rows are dropped.** Their ids stay reserved and are never reused: REQ-AUDIT-006, 018, 065, 086, 097, 102, 104, 105, 133, 134 and 135. They were about a notary actor, a per-subject key, a board copy job, an OCSF extension and a separate usage key, and each was replaced by a live row.
- **`split_from` is kept** where the parent stayed, and every parent did.
- **The words follow the neutral model** ([0003](../decisions/0003-the-neutral-model.md)): scope for boundary, `unmapped.<product>` for `unmapped.sluis`, the `seal` block for the key service, the `state` block for the store actor, "the configuration check" for sluis's command, "the front" for its edge actor, and "component" for actor.
- **Rows that need an installation to prove them** keep their `verification` as a short, neutral line. The hardware checks of one installation are not carried here.
- **Dropped fields:** the sluis issue numbers, the milestones and the gap keys of sluis's roadmap, the standards register ids (which name sluis's own register), the feature tags, and the paths of sluis's tests and actors.

## Rows added in audit

REQ-AUDIT-193 to 195 are new in audit. They say which identifiers a usage event must carry and that the organisation is optional ([0017](../decisions/0017-the-organisation-of-a-usage-record-is-optional.md)).

## Rows that stay in sluis

14 rows are marked `owner: sluis`, with a link to sluis's copy. They are the rows audit does not own:

| Rows | Why they stay |
|---|---|
| REQ-AUDIT-078, 079, 081 | The embedding of the audit page in sluis's console, its browser session, its writes and the exchanged token that reaches the front. The page is audit's ([0014](../decisions/0014-the-front-the-console-page-and-audit-verify.md)). The embedding is sluis's. |
| REQ-AUDIT-122, 123, 165 | Actions of sluis: a step-up, an issued certificate, a change across tenants |
| REQ-AUDIT-159, 160, 181, 182, 184, 185 | The sealed grant of sluis's policy actor that its key service checks. audit's own second check is open ([0013](../decisions/0013-access.md)). |
| REQ-AUDIT-019, 180 | The roll-forward after a restore, and the idle write rate of an installation |

**The limit check also stays in sluis.** Its rows are in sluis's quota area, for example `REQ-QUOTA-097`. audit supplies the usage records that the counters are made from, and nothing else ([0006](../decisions/0006-the-usage-class.md)).

## Open

- **Open: the rows of the second check on Open.** When record [0013](../decisions/0013-access.md) decides how audit alone makes the second check, rows are added here, and the sluis-owned grant rows are mirrored or withdrawn as it says.
- **Open: a row for each decision's planned part.** The decisions name planned work for the Lambda shape ([0016](../decisions/0016-distribution-and-the-lambda-shape.md)), the neutral product object and the registry of tenants. Rows are written with the work.

## The check

CI parses the file. A fuller check of the schema, the one keyword in each statement, unique ids and the verification of each MUST moves with the check scripts from sluis. **Open:** until then, `.github/scripts/check.py` checks that the YAML parses, that ids are unique and that the keyword of each statement matches its level.
