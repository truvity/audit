# AGENTS.md

The working rules of this repository, for AI agents. People follow the same rules; [CONTRIBUTING.md](CONTRIBUTING.md) has the long form.

## Every task

- The default branch is `master`. Work on a branch, open a pull request against `master`, and do not push to `master`.
- One change per branch. Use absolute paths in commands: the working directory may reset between calls.
- This repository is public. Write no hostname, account id, email, card serial, person's name, ticket key or private repository name. Use example names.
- Never print a secret or a token, not in output, logs, commits or pull request text.
- The maintainer decides. Present options with pros, cons and a recommendation, and do not pick.
- If a safety check or a permission prompt blocks you, stop and report. Do not work around it.
- Do not merge your own pull request. Wait for the `check` job, fix what it reports, and answer review comments: fix what is right, and reply with the reason to what you decline.

## Records are append-only from the first set

- The records 0001 to 0016 are the first set, written as one clean rewrite. **From this set on, decision records are append-only.** Do not rewrite an accepted record. Write a new record that supersedes it, and move the old one to `docs/decisions/archive/` with a pointer to its successor. A typo or a broken link may be fixed in place.
- A new record has the next free number and the shape of the others: Status and date, Context, Decision, Consequences, Roads not taken.
- Requirement ids are stable and never reused. Withdraw a row; do not delete it.
- State each fact once and link to it. Mark what is built and what is planned. Write `Open:` where a question is open, and never invent an answer.

## Do not

- Carry installation details into this repository: board measurements, hardware flags, card slots, a database sized for one site. The design is neutral. An installation's choices belong to its own repository.
- Restate atoom's contracts. Link to the port or block page.
- Put prices, plans or balances here. audit records usage and nothing more.
