# Schemas

JSON Schema (draft 2020-12) for everything that is loaded at runtime rather
than compiled:

| file | describes |
|---|---|
| `catalogue.schema.json` | an application's action catalogue |
| `preset.schema.json` | a framework preset under `presets/` |
| `extension.schema.json` | the constraints every extension-slot schema must satisfy, including the `x-audit-*` annotation vocabulary |

The core record's JSON Schema is generated from `proto/audit/v1/record.proto`
and published under `docs/reference/`; it is not hand-written here.
