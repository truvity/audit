# Contributing

## Ground rules for a public repository

This repository is public. Nothing in it may name a real organisation,
cluster, account, team, person, incident or internal ticket. Design
documents describe the component and the choices any deployer faces;
deployment-specific decisions live with the deployer.

## Decisions

Every decision that a stranger deploying this component would also face is
recorded under `docs/decisions/` in the MADR format (see the template).
A decision that only applies to one deployment is not recorded here.
Until the first release a decision may be revised in place, with a
"Revised" line under its status saying what changed and why. From the first
release on, decisions are never edited; they are superseded by a new one that
links back. Nothing anyone deploys can be surprised by a decision changing
before anything is deployed.

## Documentation

- `docs/why.md` and `docs/concepts.md` are the entry points and must stay
  readable by someone who has never seen the code.
- The CHANGELOG describes the state of the repository, not the journey.
- Presets cite the clause they implement and carry the disclaimer that they
  are an engineering reading, not legal advice.
- Mermaid diagrams: a `;` inside a sequence diagram message splits it.

## Tooling

Tools come from `devbox.json` through direnv. Never hand-roll a PATH; add a
missing tool with `devbox add <pkg>@<version>`.

## Commits and pull requests

Small, reviewable pull requests. A pull request that changes a contract
(`proto/`, `schemas/`, `presets/`) updates the matching reference page and,
if the change is not additive, a decision record.
