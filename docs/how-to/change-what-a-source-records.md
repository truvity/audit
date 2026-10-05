# Change what a source records

## Purpose

Add or change an action, a schema or a message of an application's catalogue and
roll it out, in the one order that does not dead-letter records: the emitter
tags, the writer gets the catalogue, the writer is deployed, then the emitter.

## Preconditions

- The emitting application (a *source*, such as sluis) owns its catalogue: a
  document (`<name>.yaml`, or `catalogue.yaml`) **and the JSON schemas it
  references**, together ([catalogue reference](../reference/catalogue.md)).
- You can deploy the installation's writer (the chart's values, or the Pulumi
  stack) and the emitter, in that order.

## Before you start

- **A catalogue is a document plus its JSON schemas, and the writer refuses to
  start without them.** It reads each document together with the `.json` files
  beside it and refuses to start when a referenced schema (an action's
  `data_schema`, a kind's `attributes_schema`, a meter's `dimensions_schema`) is
  missing, or a schema is there that nothing references. Ship the directory, not
  just the `.yaml`.
- **A changed document under an unchanged version is refused.** The archive keeps
  every version it was sent and refuses a different document under a version it
  holds, which stops the writer, and the application behind it, at start. Bump the
  catalogue's `version` for any change, even one new action, and keep the
  released document as a fixture.
- **A record naming a version the writer does not have is dead-lettered and
  acknowledged**, so neither queue's alarm sees it. The writer logs
  `event=unknown_catalogue` with the `source` and `catalogue_version`, counts
  `audit_writer_catalogue_unknown_total`, and on AWS the alarm
  `<name>-writer-unknown-catalogue` fires. That is what rolling the emitter before
  the writer looks like.
- **A stray `.yaml` is not taken for a catalogue.** A document taken as the
  catalogue only because it is the one `.yaml` of a `CatalogueDirs` directory must
  have a `source`, a `version` and `actions`, or the preview fails naming the file.
  A directory holds one catalogue.
- **Sluis ships the bundle as a release asset**,
  `sluis-audit-catalogue_<version>.tar.gz`. An estate fetches it and checks its
  checksum against the release's `checksums.txt`; it does not copy the files by
  hand, because a hand-vendored copy is the one that drifts from the release.

## Steps

1. **The emitter tags a release** with the new catalogue version. Verify: the
   release carries the catalogue bundle as an asset (for sluis,
   `sluis-audit-catalogue_<version>.tar.gz`), and its CI has checked that every
   emitted action is declared and the reverse. Roll back: none; an unreleased tag
   is simply not deployed.

2. **The writer gets the catalogue.**

   - **Kubernetes:** an application registers its catalogue at start-up over
     `RegisterCatalogue`, so the writer needs no change; the new version is stored
     under `catalogue/<source>/<version>` when the first record under it arrives.
     A catalogue given through the chart's `catalogues` value is a values change,
     applied in this step.
   - **AWS Lambda:** the writer has no registry; it has the common catalogue and
     the files in the layer. Fetch the release asset, check it, unpack it, and give
     the directory to the stack:

     ```sh
     curl -fsSLO "https://github.com/truvity/sluis/releases/download/v<version>/sluis-audit-catalogue_<version>.tar.gz"
     grep "sluis-audit-catalogue_<version>.tar.gz" checksums.txt | sha256sum -c -
     tar -xzf sluis-audit-catalogue_<version>.tar.gz -C catalogue/
     ```

     ```go
     Writer: auditpulumi.WriterArgs{
         CatalogueDirs: []string{"catalogue/roster"}, // the document and its *.json
     },
     ```

     (`roster` is the real directory and source name sluis still uses.) Verify:
     `pulumi preview` shows a new configuration layer version and the function
     pointed at it, and passes the catalogue guard, which compares each document
     with the archive's own copy ([ship a release](aws-ship-a-release.md)).

3. **Deploy the writer.** Verify: it starts (its `audit.writer.started` record carries the
   catalogues' digest) and `/readyz` answers on a Kubernetes writer. Roll back:
   the previous layer version is kept (`SkipDestroy`); redeploy the previous
   program. The old catalogue versions stay in the archive, so records written
   under them still read.

4. **Deploy the emitter.** Verify: the application logs its registration at
   start (a Kubernetes writer) and a record of the new action reaches the archive.
   Roll back: redeploy the previous emitter; the writer knows both versions.

## Afterwards

- Watch `event=unknown_catalogue` / `audit_writer_catalogue_unknown_total` for an
  hour after the emitter rolls. Any increase means a pod still runs a version the
  writer lacks; the records are in the archive under `dlq/` and are replayed with
  [replay dead-lettered records](replay-dead-lettered-records.md) once the writer
  has the version.
- Tell the people who read the trail if a message template changed: the viewer
  renders sentences from the catalogue.
