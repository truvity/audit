# Roadmap

Work that is wanted but not started. Each entry is a note, not a commitment:
nothing here is built, and none of it changes what the pages elsewhere
describe.

R2 is a supported archive store, with governance-grade retention.

## To do

Nothing is queued.

## Parked

A Cloudflare Cron Worker as the notary over R2, and Cloudflare Queues as an
ingest transport and the change feed for observe, are parked: R2's locks are
governance-grade only, and the target is S3 Object Lock in compliance mode
with Lambda and Kubernetes as the platforms
([0016](../decisions/0016-three-parts-installed-independently.md),
[0023](../decisions/0023-archive-retention-and-lifecycle.md)). Cloudflare stays a
proxy and DNS in front of an installation. Both would depend on the
[bucket contract](../reference/bucket-contract.md), which is a target a store
other than S3 would have to meet.
