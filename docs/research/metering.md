# Metering from events

## Products

OpenMeter ingests CloudEvents, dedupes by source plus id, aggregates in
ClickHouse behind Kafka; no Postgres-only mode; a realistic self-hosted
setup is three stateful services. Lago dedupes by transaction id and
supports weighted sums for storage-like gauges; its high-volume mode is
ClickHouse. Stripe meters take an event name, customer id, value,
identifier and timestamp within 35 days, aggregate sum, count or last, and
recommend pre-aggregation with hourly or daily buckets. Orb has a
configurable grace period and never deletes ingested usage. Metronome
dedupes for 34 days. AWS Marketplace metering rejects records more than six
hours late and uses the hour as the idempotency key.
Sources: https://openmeter.io/docs/open-source/architecture ,
https://getlago.com/docs/guide/events/ingesting-usage ,
https://docs.stripe.com/api/v2/billing/meter-event/create ,
https://docs.stripe.com/billing/subscriptions/usage-based/meters/configure.md ,
https://docs.withorb.com/guides/events-and-metrics/reporting-errors ,
https://docs.metronome.com/guides/events/send-usage-events

## Design guidance

Exactly-once accounting is built on at-least-once delivery plus
idempotency. Every invoice must be reconstructible from raw events. Period
assignment lives on the occurrence time; lateness detection on the delta to
receipt time; an acceptance window of one to three days before close.
Storage is metered by periodic absolute samples averaged over the month,
not by deltas.
Sources: https://getlago.com/blog/event-ingestion-architecture ,
https://konghq.com/blog/enterprise/guide-to-metered-billing-for-apis ,
https://docs.aws.amazon.com/AmazonS3/latest/userguide/aws-usage-report-understand.html

## Sharing the stream with audit

Cloud providers do not bill from audit logs; the cloud provider's own
forum documents hours reconstructed from its trail disagreeing with its
bill, and an edge provider splits a sampled analytics store for billing
from log push for audit. Two emitters, however, produce divergent counts.
The resolution is one stream, two projections, two locked prefixes:
billing keeps tenant, meter, quantity and time for seven years and nothing
about people; security keeps actor, address and session for its own term.
Sources: https://repost.aws/questions/QUYQlSDf9GTFa0xd7p5hR_2Q/discrepancy-between-the-calculation-of-aws-ec2-on-demand-hours-using-cloudtrail-events-and-the-data-provided-by-aws-billing ,
https://developers.cloudflare.com/use-cases/saas/usage-analytics

## Dutch accounting retention

AWR art. 52: seven years. The tax authority's brochure on automated
administration: detail data behind condensed figures must be kept seven
years, demonstrably original and unchanged; conversion allowed only with
retained reconciliation. Supreme Court 2021: point-of-sale detail kept two
weeks failed the duty. Usage events that determine an invoice quantity are
the SaaS equivalent.
Sources: https://www.belastingdienst.nl/wps/wcm/connect/bldcontentnl/belastingdienst/zakelijk/btw/administratie_bijhouden/administratie_bewaren/ ,
https://download.belastingdienst.nl/belastingdienst/docs/geautomatiseerde_administratie_en_fiscale_bewaarplicht_al0401z6fd.pdf ,
https://uitspraken.rechtspraak.nl/details?id=ECLI:NL:HR:2021:987

## Volume

At one to ten million events a day of about a kilobyte, compressed growth
is a tenth to a bit over a gigabyte a day, a few terabytes after seven
years, tens of dollars a month on standard storage. A three-node stream
cluster and a partitioned Postgres are two orders of magnitude below their
limits. ClickHouse and Kafka are not justified below roughly a hundred
million events a day.
