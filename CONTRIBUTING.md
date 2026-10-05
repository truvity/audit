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
Decisions are never edited after acceptance; they are superseded by a new
one that links back.

## Documentation

Documentation follows the [documentation contract](https://github.com/truvity/policy/blob/master/docs/contracts/docs.md).
A page belongs to one directory of `docs/`, by what the reader is doing:

| directory | holds |
|---|---|
| `docs/getting-started/` | one tutorial per deployment shape, from nothing to working |
| `docs/how-to/` | one task per page; runbooks use one template (purpose, preconditions, before you start, steps with command, expected output, verify and rollback, afterwards); migration steps in `docs/how-to/upgrade/vX.Y.md`, linked from the CHANGELOG |
| `docs/reference/` | configuration keys, chart values, API, catalogue, bucket contract |
| `docs/explanation/` | design and the why |
| `docs/decisions/` | the ADRs, with a Status column in the index; a superseded decision has its own Status line changed |

- `docs/explanation/why.md` and `docs/explanation/concepts.md` are the entry points and must stay
  readable by someone who has never seen the code.
- Prefer a page under about 400 lines; split by audience, not by length.
- Where reference can be produced from code or a schema, mark it
  `<!-- generated: name -->` ... `<!-- /generated -->` so a drift check can regenerate it, and do
  not edit inside the markers by hand.
- The CHANGELOG describes the state of the repository, not the journey, and a **Breaking:** entry
  links its upgrade page.
- A compliance bundle is a **profile**; the files in `presets/` are *framework profiles*, and the
  directory keeps its name until a code change renames it.
- Framework profiles cite the clause they implement and carry the disclaimer that they
  are an engineering reading, not legal advice.
- Mermaid diagrams: a `;` inside a sequence diagram message splits it.
- `go test ./internal/docscheck` holds every relative link and anchor to what it points at.

## Tooling

Tools come from `devbox.json` through direnv. Never hand-roll a PATH; add a
missing tool with `devbox add <pkg>@<version>`.

`just check` is the gate. It needs nothing but this checkout: no C toolchain,
no network. The checks that need more are separate recipes, which CI runs as
their own jobs, and they matter as much.

- `just race` needs a C toolchain, which nothing else here does. Run it before
  changing anything that hands a record to a background goroutine, because a
  race there is a lost record rather than a crash.
- `just drift-ts` regenerates TypeScript, whose plugin comes from a remote
  schema registry that rate limits. `just drift`, in the gate, checks the same
  thing for Go and the JSON Schema, which local plugins produce. A gate that
  fails because somebody else was generating code is a gate people learn to
  ignore.
- `just conformance` starts Postgres, S3 with object locking (LocalStack) and
  an OpenBAO dev server, and runs the whole suite against them. Every test that
  skips without its service runs there: the searchers' conformance suite
  against all three searchers, the transports' corpus, the archive walks, the
  transit keys and signer. About twenty seconds; it needs Docker. The OpenBAO
  half covers a path a deployment opts into rather than the usual one:
  pseudonymisation keys are off by default
  ([0013](docs/decisions/0013-no-pseudonymisation-keys-by-default.md)), so the
  transit provider is tested because it is offered, not because it is the
  default. The transit signer, for the seals that will follow, is a separate
  choice, and is tested the same way.
- `just ts` installs the TypeScript package's dependencies, then typechecks,
  tests, builds, and checks what a publish would ship.
- Against real S3, on demand: `AUDIT_S3_REAL_BUCKET=<bucket> go test
  ./internal/s3test -run RealBucket` checks that a lock can be lengthened and
  that compliance mode refuses to shorten it, which no emulator implements.
  Point it at an Object-Locked sandbox bucket: each run leaves one small object
  locked for two days.

## Documentation held to the code

Documentation is held to the code it describes. Every command shown is one the
binary takes, every chart value named exists in `charts/audit/values.yaml`,
and every example worth compiling lives in `examples/` and is built by the
gate. `internal/docscheck` fails the gate on a relative link to a file that
does not exist, or to a heading a page does not have. When you rename a flag,
a value or a heading, search the docs for it in the same change.

Where the documentation runs ahead of the code — as it does while the
architecture of
[0011](docs/decisions/0011-one-installation-per-service-or-product.md),
[0012](docs/decisions/0012-two-deliveries-and-a-durable-ack.md) and
[0013](docs/decisions/0013-no-pseudonymisation-keys-by-default.md) is being
built — every name that does not exist yet says so where it is used: `# not
built yet`, or a sentence beside it. A reference
that cannot be told apart from the built thing is worse than a gap.

## Commits and pull requests

Small, reviewable pull requests. A pull request that changes a contract
(`proto/`, `schemas/`, `presets/`) updates the matching reference page and,
if the change is not additive, a decision record.
