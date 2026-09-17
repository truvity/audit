# Schema standards

| standard | shape | verdict |
|---|---|---|
| OCSF 1.9.0 | class and category enums, `actor`, `metadata`, `status_id`, `unmapped`; profiles as mix-ins incl. `record_integrity` since 1.9; registered extensions | Too heavy to author in; the mandatory export target |
| ECS 9.x | flat dotted fields, `event.{category,type,outcome,action}`, `user.target`, `related.*` | Frozen as a design since the 2023 donation to OpenTelemetry; borrow vocabulary |
| OpenTelemetry Logs, semconv 1.44 | LogRecord with EventName and attributes; no audit convention (the only proposal, issue 2468, stalled) | Transport copy only |
| CloudEvents 1.0.2 | envelope: id, source, type, subject, time, data; extensions authcontext, sequence, dataschema, DSSE | Wire envelope, not a record |
| CADF 1.0 (2014) | initiator, action, target, outcome, observer, reason, reporterchain | Dormant; the cleanest concepts |
| Kubernetes audit v1 | request-centric, level and stage, objectRef, annotations | Best small-schema precedent |
| CloudTrail 1.11, Google AuditLog | RPC-centric, rich identity chain, request and response, authorizationInfo, addendum, major.minor versioning | Borrow identity chain, size caps, versioning, addendum |
| Retraced, WorkOS, Okta | action + actor + targets[] + outcome + context + metadata + version | The right shape for a small organisation |

Recommendation adopted: base the record on the vendor spine, which is CADF
modernised; keep both a free-form action and a coarse operation so every
record maps to an OCSF class and activity; keep an `unmapped` bag; ship
OCSF, ECS and OpenTelemetry mappings as exporters.

Sources: https://schema.ocsf.io/ , https://github.com/ocsf/ocsf-schema ,
https://www.elastic.co/docs/reference/ecs/ecs-opentelemetry ,
https://opentelemetry.io/docs/specs/otel/logs/data-model/ ,
https://github.com/open-telemetry/semantic-conventions/issues/2468 ,
https://github.com/cloudevents/spec/blob/main/cloudevents/spec.md ,
https://www.dmtf.org/standards/cadf ,
https://kubernetes.io/docs/reference/config-api/apiserver-audit.v1/ ,
https://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-event-reference-record-contents.html ,
https://docs.cloud.google.com/logging/docs/reference/audit/auditlog/rest/Shared.Types/AuditLog ,
https://workos.com/docs/reference/audit-logs/event ,
https://developer.okta.com/docs/reference/api/system-log/ ,
https://github.com/retracedhq/retraced ,
https://developer.hashicorp.com/vault/docs/audit ,
https://goteleport.com/docs/reference/deployment/monitoring/audit/
