# How products expose audit logs

Patterns worth copying, with the product that exemplifies each:

1. `resource.verb` action names plus a fixed coarse operation enum
   (create, access, modify, remove, authentication, transfer, restore) —
   GitHub.
2. Typed actor with a kind discriminator and token attribution by hash,
   never the token — OpenAI, GitHub.
3. Targets as an array of `{id, type, name}` — Okta, WorkOS.
4. Time-ordered ids that double as cursors — WorkOS, Stripe.
5. Previous-attributes diff only on update events, whole arrays when any
   element changed — Stripe.
6. Request id, idempotency key and trace id on every record, and the audit
   call id returned in the API response — Stripe, Cerbos.
7. Schema version on the record with an equal-on-major, greater-or-equal-
   on-minor contract — CloudTrail.
8. Register event schemas per action before emitting; bounded metadata —
   WorkOS.
9. Cursor pagination with opaque tokens and a resumable tail cursor that
   stays valid for days — GitHub, Stripe, Okta polling mode.
10. A small typed filter grammar with a keyword box, not free Lucene —
    GitHub qualifiers, Okta SCIM filter.
11. Separate immutable long-retention audit storage from short-retention
    activity logs, and make it lockable — Google Cloud's `_Required`
    bucket.
12. Hash per object plus a periodic signed digest chaining to the previous
    digest, and a validate command — CloudTrail.
13. Fail-closed writes with redundant sinks — Vault.
14. HMAC or redact sensitive values at write time with per-emitter allow
    lists; truncate oversized fields in a documented order rather than
    dropping the event — Vault, CloudTrail.
15. Asynchronous export job with a short-lived signed URL — WorkOS.
16. Streaming targets as compressed files partitioned by hour, at-least-
    once, with a replay buffer and a backfill cursor — GitHub, Auth0.
17. Tenant as a first-class column on every query; a shared event id when
    one action is visible to two tenants — CloudTrail, GitHub.
18. Late corrections appended as addendum records, never edits —
    CloudTrail.
19. A viewer with a reverse-chronological table, human sentences, facets,
    a JSON side panel with filter-for and filter-out, diffs, permalinks,
    export and an integrity badge — Grafana Explore, Pangea, Clerk.
20. Keep the index optional and rebuildable from object storage; do not
    rely on a ledger database — CloudTrail Lake, Teleport, the retirement
    of QLDB.

Corrections found while surveying: GitHub retains Git events seven days,
not ninety; Auth0 retention is one to thirty days by plan; CloudTrail Lake
closes to new customers in May 2026; supa_audit is archived and has no
hash chain; QLDB ended in July 2025.

Sources: https://docs.github.com/en/enterprise-cloud@latest/admin/monitoring-activity-in-your-enterprise/reviewing-audit-logs-for-your-enterprise/using-the-audit-log-api-for-your-enterprise ,
https://docs.stripe.com/api/events/object , https://docs.stripe.com/activity-logs ,
https://developer.okta.com/docs/reference/system-log-query/ ,
https://auth0.com/docs/deploy-monitor/logs/log-data-retention ,
https://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-log-file-validation-cli.html ,
https://docs.cloud.google.com/logging/docs/buckets ,
https://developer.hashicorp.com/vault/docs/audit ,
https://workos.com/docs/audit-logs , https://github.com/retracedhq/retraced ,
https://pangea.cloud/docs/audit/about-tamperproofing ,
https://platform.openai.com/docs/api-reference/audit-logs ,
https://docs.cerbos.dev/cerbos/latest/configuration/audit ,
https://goteleport.com/docs/reference/deployment/backends/ ,
https://github.com/pgaudit/pgaudit , https://github.com/supabase/supa_audit ,
https://www.infoq.com/news/2024/07/aws-kill-qldb
