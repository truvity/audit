# Send the Lambda functions' telemetry

## Purpose

Have the writer and notary functions export OpenTelemetry with the **function
role's identity and no secret**.

## Preconditions

- A published layer version of the OTLP extension in the account and region
  (by convention named `audit-otlp`), from
  [sluis](https://github.com/truvity/sluis)'s release (the extension is
  sluis's: its page covers the extension itself).
- An issuer that trades the function role's STS identity token for a
  short-lived token audienced for the OTLP door, and an OTLP/HTTP endpoint.
- The account has outbound identity federation enabled.

## Before you start

- **The issuer must admit the two roles** (a group matcher on
  `arn:aws:iam::<account>:role/audit/audit-*`, or the exact role ARNs).
- **The extension is fail-open**: telemetry never blocks an invocation, so a
  broken setup is silent until you look for the signal.
- **The old `ACCESS_ROSTER_*` names are deprecated for one minor.** The library
  sets both spellings of the extension's four settings; `Telemetry.OmitLegacyEnv`
  drops the old ones once the extension reads the new names.
- **No secret goes in `Telemetry.ExtraEnv`**: it refuses `OTEL_*HEADERS*` and any
  key naming a token, secret, password or credential.

## Steps

1. Set `Telemetry` in the stack (`ExtensionLayerArn`, `IssuerURL`,
   `OTLPEndpoint`; `STSAudience` and `OTLPAudience` default to `otlp`).
   Expected: the functions gain the extension layer and the environment below,
   and the roles gain `sts:GetWebIdentityToken` limited to that audience, ES384
   and 300 seconds.

   ```
   OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
   OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
   OTEL_SERVICE_NAME=audit-writer            # audit-notary for the notary
   AUDIT_OTLP_ISSUER, AUDIT_OTLP_STS_AUDIENCE, AUDIT_OTLP_ENDPOINT, AUDIT_OTLP_AUDIENCE
   ```

   Roll back: remove `Telemetry`; the layer, the variables and the statement
   go together.

2. Apply, then invoke the writer once. Verify: the metric
   `audit_queue_message_age_seconds` (label `transport=sqs`) arrives at the
   collector. The function flushes at the end of every invocation and on
   SIGTERM, because the environment is frozen after it.

## Afterwards

The alarm set does not depend on this telemetry: it is CloudWatch's
([alarms](../reference/aws-pulumi-library.md#alarms)). The OTLP signals are in
[telemetry](../reference/telemetry.md).
