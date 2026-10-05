# Ship a release to the Lambda functions

## Purpose

Deploy the release's writer and notary zips through the Pulumi library, with the
guards that fail a `pulumi preview` instead of a running function.

## Preconditions

- The release's `audit-writer-lambda_<version>_linux_arm64.zip` and
  `audit-notary-lambda_<version>_linux_arm64.zip`, and the SHA-256 that the
  release's `checksums.txt` lists for each.
- The library at the same release as the zips (`github.com/truvity/audit/deploy/pulumi`).
- A deploying identity that can read `catalogue/*` and list the archive bucket
  (the catalogue guard, below) and publish and get layer versions
  (`lambda:PublishLayerVersion`, `lambda:GetLayerVersion`).

## Before you start

- **A function that fails its init is not a failed deploy.** The event source
  mapping keeps invoking it and the ingest queue drains into the dead-letter
  queue a message at a time. The guards below exist so that what can be known
  before the function is touched is a failed preview.
- **Library and zip must be the same release.** The library renders the
  configuration for its own release's schema, and a binary of another release
  may refuse it at start-up. The binary's release is read from the zip's file
  name, so do not rename the file. `Guards.AllowVersionSkew` accepts a
  difference that is meant (a build from a checkout); a library whose own
  release is not known (a `replace` directive) compares nothing.
- **Pin the digest in the stack's source**, where it is reviewed. Do not fetch
  `checksums.txt` at deploy time: that checks the file against itself.
- **The catalogue guard needs two permissions.** `s3:GetObject` on `catalogue/*`
  **and** `s3:ListBucket` on the bucket (condition `s3:prefix` = `catalogue/`);
  without the second, S3 answers a missing key with 403, which the guard
  refuses with a message naming it. `Guards.SkipCatalogueCheck` says the
  deploying identity cannot read the bucket.
- **A notary zip is not accepted as the writer's**, and the reverse.

## Steps

1. **Pin the package and its digest** in the stack.

   ```go
   Writer: auditpulumi.WriterArgs{
       Package:       "dist/audit-writer-lambda_<version>_linux_arm64.zip",
       PackageSHA256: "<the checksums.txt line for that file>",
   },
   Notary: auditpulumi.NotaryArgs{
       Package:       "dist/audit-notary-lambda_<version>_linux_arm64.zip",
       PackageSHA256: "<...>",
   },
   ```

   The library deploys the file byte for byte: it adds nothing and builds no
   package. Roll back: restore the previous values.

2. **Preview.** `pulumi preview`.

   Expected: the guards run ahead of every resource. The package must be the
   file the digest names, a zip with `bootstrap` at its root and nothing
   outside it, an arm64 Linux executable, built from the command the field
   wants. Each catalogue is compared with the archive's own copy at
   `catalogue/<source>/<version>`: a changed document under an unchanged
   version is what the writer refuses to start on, and is refused here instead.
   An object that is not there, or a bucket that is not there yet, is nothing
   to compare. Verify: the preview shows the function code and the layer
   version updated and no replacement.

3. **Apply.** `pulumi up`. Verify: the function's first invocation records
   `audit.writer.started` in the trail (its digests and the layer ARN); the queue's
   oldest-message-age alarm stays quiet. Roll back: `pulumi up` the previous
   program. Layer versions are never destroyed (`SkipDestroy`), so the previous
   configuration is still there.

## Afterwards

- If the library moved, read the release's [upgrade page](upgrade/v0.13.md) first.
- For a build from a checkout (no release zip), build as the release does and
  pass `Guards.AllowVersionSkew` with the digest of what you built:

  ```sh
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOEXPERIMENT=jsonv2 go build -trimpath -o bootstrap ./cmd/audit-writer-lambda
  zip audit-writer-lambda.zip bootstrap && sha256sum audit-writer-lambda.zip
  ```

- `just pulumi-test` runs the library's tests on Pulumi's mocks: no cloud, no
  credentials, no plugin. They hold the resources, the IAM, the alarm set, the
  guards and the configuration the library ships in each layer against the JSON
  Schema of the binary that reads it.
