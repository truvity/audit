# Contributing

Thank you for helping. People and AI agents follow the same rules. [AGENTS.md](AGENTS.md) is their short form.

## Before you start

- Look for an existing issue or pull request first.
- For a bug, open an issue with the steps to reproduce it.
- For a feature or a change of design, open an issue that describes the problem before the solution: who has it, and what they do today. Wait for the maintainer's answer before you build anything large.
- A security problem goes through [SECURITY.md](SECURITY.md), never through an issue or a pull request.

## How the repository is organised

| Where | What |
|---|---|
| `docs/decisions/` | Decision records. One subject per record. They are the source of truth for the design. |
| `docs/reference/` | What the records fix, in tables: formats, layouts, contracts. |
| `docs/runbooks/` | What an operator does, step by step. |
| `docs/requirements/` | The testable rules, as data. |
| `profiles/` | The compliance profiles: floors with their clauses. |

## Writing a decision

- A record stands alone: Status and date, Context, Decision, Consequences, Roads not taken. Fold what you learned into the text.
- **The first set, 0001 to 0016, was written as one clean rewrite. From it on, records are append-only.** To change a decision, write a new record that supersedes the old one, and move the old one to `docs/decisions/archive/` with a note that names its successor. Do not edit an accepted record, except to fix a typo or a broken link. When a later record supersedes only part of it, leave the record in place and change its Status line to `accepted, <date>. Partly superseded by [NNNN](NNNN-slug.md): <what changed>`; the new record says `Partly supersedes` in its own Status line and names the words it overtakes.
- State each fact once, where it belongs, and link to it from everywhere else.
- Say what is built and what is planned. Where something is open, write `Open:` and name the question. Do not invent a decision.
- Record the roads not taken, with the reason.
- Date a measurement and say how it was made. Label a claim you read but did not test.

## Writing documentation

- Plain English, short sentences, the same word for the same thing. The words of the model are in [the glossary](docs/reference/glossary.md).
- Links inside the repository are relative. Links to other repositories are absolute GitHub URLs on the default branch.
- This repository is public. Do not write hostnames, account ids, card serials, emails, people's names, private repository names or the deployment details of any estate. Use example names.

## Requirements

`docs/requirements/audit.yaml` holds the rules. A row has one keyword (MUST, SHOULD or MAY), one testable behaviour and a way to verify it. Ids are stable and never reused. A rule that no longer applies gets `status: withdrawn` and the id of its replacement. [The requirements README](docs/requirements/README.md) says more.

## The check

Every pull request runs the `check` job: it validates every YAML file and every relative markdown link. Run it locally with `python3 .github/scripts/check.py`.

## Licence

By sending a contribution you agree that it is licensed under the [LICENSE](LICENSE) (MIT).
