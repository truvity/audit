# Getting started with a sluis-connected install

[sluis](https://github.com/truvity/sluis) is the access-management service that emits
audit records, and the first product that connects to an audit installation. (It was named
access-roster; the names `roster` in the catalogue's source and `ACCESS_ROSTER_*` in the
Lambda extension's old settings are kept identifiers, not the product's name.) This page is
audit's half of connecting it: what the installation must hold. How to configure sluis
itself, its settings and its runbook are in [sluis's documentation](https://github.com/truvity/sluis/tree/master/docs);
this page does not restate them.

You need an installation first: [Kubernetes](kubernetes.md) or [AWS Lambda](aws-lambda.md).

## What the audit installation holds for sluis

| what | where | how |
|---|---|---|
| sluis's catalogue (source `roster`) | the writer | Kubernetes: sluis registers it at start-up, nothing to install. Lambda: the writer has no registry; unpack sluis's release bundle `sluis-audit-catalogue_<version>.tar.gz` (check its checksum) and give the directory to `Writer.CatalogueDirs` ([change what a source records](../how-to/change-what-a-source-records.md)) |
| the right to write | the writer | Kubernetes: a `workloadIdentity.workloads` entry mapping sluis's ServiceAccount to the source (`roster`) and its issuer in `workloadIdentity.issuers`. Lambda: sluis's role in `Ingest.Senders` (a role ARN, not a session ARN) |
| the right to read | the query service | `query.grants` naming the issuer and audience sluis's console uses, and who may read what ([read the trail](../how-to/read-the-trail.md)) |
| the profiles sluis's actions fall under | the deployment document | at least `security`; `history` if sluis's console shows tenant-facing activity |

## Steps

1. **Install audit** with one of the two tutorials, with the entries in the table above.
2. **Connect sluis** by following sluis's own documentation, pointing its audit settings at the
   writer's address (Kubernetes) or the ingest queue URL (Lambda).
3. **Check from audit's side.** The writer logs the registration (Kubernetes), a record
   appears in the archive under `records/security/`, and `audit verify` passes
   ([verify the trail](../how-to/verify-the-trail.md)).

## When it goes wrong

- A record naming a catalogue version the writer lacks is dead-lettered and acknowledged;
  `event=unknown_catalogue` and the `<name>-writer-unknown-catalogue` alarm say so. The fix is
  the order in [change what a source records](../how-to/change-what-a-source-records.md): the writer
  gets the catalogue, then sluis rolls.
- Messages in the Lambda's DLQ: [redrive the ingest DLQ](../how-to/redrive-the-ingest-dlq.md).
- sluis refuses to start with an installation that refused its catalogue; the reasons are in the
  writer's log.
