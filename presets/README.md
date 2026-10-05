# Framework profiles

This directory holds the framework profiles: one YAML file per standard, each saying what that
framework requires of a profile (which record fields must be present or are forbidden, how
identities are treated, how long copies are kept, what integrity applies and how often the trail is
reviewed). Each cites the clauses it reads and carries a disclaimer. A deployment composes them
into its own **profiles**.

The directory is called `presets/` for now. A compliance bundle is a **profile**, not a preset; the
name stays here, in the `presets:` key of the deployment document and in the `preset` package until a
code change renames them. Everything about them, including the table of the seven and the composition
rules, is in [the profiles reference](../docs/reference/profiles.md); which to compose is
[which profiles to compose](../docs/explanation/which-profiles-to-compose.md). The format is
[`sdk/schemas/preset.schema.json`](../sdk/schemas/preset.schema.json), and `audit profile explain <name>`
prints what a profile keeps.
