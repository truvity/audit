# Security Policy

## Reporting a vulnerability

Report a vulnerability privately through [GitHub Security Advisories](https://github.com/truvity/audit/security/advisories/new).

Do not open a public issue, a pull request or a discussion for a vulnerability.

Please include:

- the version or commit you tested;
- what you did, what you expected and what happened;
- a minimal way to reproduce it;
- what an attacker gains.

Do not include real secrets or personal data. Use test values.

## What to expect

The maintainer is one person. Replies are best effort, and we aim to answer within a week. We confirm the report, agree on severity, fix it, and then publish an advisory. We credit you if you wish. Please give us reasonable time to fix a problem before you tell anyone else.

## Supported versions

This repository holds design and documentation today. The code that the design describes is released from [sluis](https://github.com/truvity/sluis) and [atoom](https://github.com/truvity/atoom) until it moves here. Report a problem in that code against the repository that releases it. When code moves here, this section will name the supported versions.

## Scope

A flaw in the design that this repository describes is in scope: a way to forge, remove, reorder or read records that the design says it prevents. The [threat model](docs/reference/threat-model.md) lists what the design claims. Out of scope: your own deployment's configuration, third-party services, and findings that need an already-compromised administrator of the log store and of the key service together.
