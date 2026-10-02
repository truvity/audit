# 0021. One configuration file, validated against a schema

- Status: accepted
- Date: 2026-10-02

## Context

The binaries are configured by flags, each with an environment variable as a
fallback, and the chart renders both. A deployment's configuration is
therefore spread across a command line, an environment and a chart's values,
none of which is checked as a whole: a flag misspelt in the chart is
discovered when the container starts, an environment variable that shadows a
flag is discovered when somebody asks why it does not behave, and nothing
says which variables are secrets.

Telemetry is configured the same way, by options of this component's own,
though a standard exists for it.

## Decision

This follows the shared rules of the component contract: configuration is a
file plus the environment for secrets
([policy 0002](https://github.com/truvity/policy/blob/master/docs/decisions/0002-config-file-plus-env.md)),
and telemetry is the SDK's environment
([policy 0006](https://github.com/truvity/policy/blob/master/docs/decisions/0006-telemetry-is-the-sdk-environment.md)).

**Each binary reads one configuration file** and nothing else configures it.
The file is YAML, its path is the one flag the binary has (`--config`), and
it is **validated against a JSON Schema** that ships with the release. An
unknown key, a missing required key or a value of the wrong type is a
start-up error that names the path to it. The same schema validates the file
in CI and in the chart, so a bad configuration fails before it is deployed.

**Secrets are the one thing that comes from the environment**, and only the
ones the file declares. A field the schema marks as secret holds a reference
to a variable by name (`{env: NAME}`), never a value; the process reads
exactly the variables the file names. A secret written into the file is
refused. There is no other path from the environment into the configuration.

**Telemetry uses the standard `OTEL_*` environment variables**
(`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, and the rest of the
SDK's). Nothing about telemetry is in the configuration file, so the same file
runs in every environment and the platform decides where signals go.

**Flags with an environment fallback are retired**, with no deprecation
period: the release that introduces the file removes them.

**The chart passes `config` through.** Its values hold a `config` tree that
is rendered into a ConfigMap as is and validated by the same schema, in place
of one chart value per option.

## Consequences

- A deployment is one reviewable document, and a change to it is one diff.
- A typo is a failed install, not a silently ignored option.
- The environment of a process is short, enumerable and in the file.
- The same file works on a function platform, where a file is in the image
  and the environment is the platform's.
- Breaking: every flag and variable a deployer uses today changes. The
  release notes carry a mapping.

## Alternatives considered

- **Keep flags and environment, add validation.** The sources still
  overlap, and the schema would have to describe precedence.
- **A configuration language that generates the file** (Pkl, CUE). A good
  producer of this file, and a later one: the schema is what such a tool
  would target, so nothing here forecloses it.
- **Telemetry in the file.** It ties the file to one environment and
  duplicates a standard.
