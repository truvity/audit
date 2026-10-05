# Keep the writer's secrets in SSM

## Purpose

Give the writer Lambda a secret (an OpenBAO token, say) without putting it in
the function's environment, by naming it in `Writer.Keys` and storing it as an
SSM SecureString.

## Preconditions

- A stack that deploys the writer with the [Pulumi library](../reference/aws-pulumi-library.md).
- The binaries of this release: version-2 configuration, which has the
  `secrets` block ([configuration](../reference/configuration.md#secrets)).
- Permission to create SSM parameters under the stack's secrets root, outside
  this program.

## Before you start

- **A `...Env` key in `Writer.Keys` is refused**, at preview. A version-1 `...Env`
  that the loader converts to `secrets: {source: env}` fails at run time on
  Lambda, because a function's environment is not a place for a secret. Name
  the secret with `...Secret` (`tokenSecret: openbao/token`).
- **The library never creates the parameters.** It is not given their values,
  and a value passed through Pulumi is kept in its state. A missing parameter
  fails the function's init, so the first invocation of a new version finds
  the fault, and messages do not drain into the dead-letter queue first.
- **`Writer.Secrets.Root` must be under `/audit/`** (at least two segments, the
  first `audit`), with no pattern, no trailing slash and no `.` or `..`
  segment. A name that climbs out of the root is refused at plan time and again
  by the binary at start.
- **A customer-managed key needs `Writer.Secrets.KeyArn`**, or the writer's
  `kms:Decrypt` is missing. Without a key the parameters are under the
  AWS-managed `alias/aws/ssm` and no grant is needed.

## Steps

1. **Name the secret.** In the stack, set it in `Writer.Keys` and, if you want a
   root other than the default, `Writer.Secrets`.

   ```go
   Writer: auditpulumi.WriterArgs{
       Keys: map[string]any{"provider": "transit", "transit": map[string]any{
           "openbao": map[string]any{"address": "https://openbao.example.com:8200",
               "tokenSecret": "openbao/token"}}},
       // Secrets: &auditpulumi.SecretsArgs{Root: "/audit/audit/private/config", KeyArn: kmsKey},
   },
   ```

   Expected: `pulumi preview` shows the layer's `audit.yaml` gaining
   `secrets: {source: ssm, root: ...}` and the writer's role gaining the SSM
   statement. Verify: the `SecretsRoot` output is the root you expect. Roll
   back: remove the key; the grant goes with it.

2. **Create the SecureString**, outside this program. The name is the root
   plus the secret's name.

   ```sh
   aws ssm put-parameter --type SecureString \
     --name "$(pulumi stack output SecretsRoot)/openbao/token" --value "$TOKEN"
   ```

   Verify: `aws ssm get-parameter --with-decryption --name <that name>` as the
   writer's role (or `aws iam simulate-principal-policy` for its grant). Roll
   back: `aws ssm delete-parameter` the name.

3. **Deploy** (`pulumi up`) and read the first invocation's log.

   Verify: the function starts and the trail gains an `audit.writer.started` record. A failure
   names the field, the source and the root, never the name or the value.
   Roll back: `pulumi up` the previous program; the previous layer version is
   kept.

## Afterwards

- Rotate the value by writing a new version of the parameter and deploying a
  new function version: the secret is read at cold start, not per invocation.
- What the library grants, exactly: `ssm:GetParameter` and `ssm:GetParameters`
  on `arn:aws:ssm:<region>:<account>:parameter<root>/*`, and with `KeyArn`,
  `kms:Decrypt` through SSM only and for parameters under the root only.

## Reference

The model is in [AWS Lambda](../explanation/aws-lambda.md#why-no-secret-is-in-the-environment).
