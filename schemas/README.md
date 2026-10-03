# Schemas

The schemas of each binary's configuration file. The three meta-schemas, which
govern formats the SDK also reads, are in [`sdk/schemas/`](../sdk/schemas/):
a Go module can embed only files inside its own directory.

JSON Schema (draft 2020-12) for everything that is loaded at runtime rather
than compiled; the first three are in `sdk/schemas/`:

| file | describes |
|---|---|
| `catalogue.schema.json` | an application's action catalogue |
| `preset.schema.json` | a framework preset under `presets/` |
| `extension.schema.json` | the constraints every extension-slot schema must satisfy, including the `x-audit-*` annotation vocabulary |

`config/` holds the schema of each binary's configuration file
(`audit-writer`, `audit-query`, and one for each scheduled job of `audit`).
They are generated from `internal/config/schema` by `just config-schemas` and
committed; `just drift` fails when they differ. The binaries embed them and the
chart's `values.schema.json` embeds them under each component's `config`. See
[the configuration reference](../docs/reference/configuration.md).

The core record's JSON Schema is generated from `proto/audit/v1/record.proto`
by a buf plugin during `just generate`, comments included, and published as
`gen/jsonschema/record.v1.schema.json`. It is never hand-written, and never
here: it belongs with the other generated code so there is one copy of it.
