import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";

import { QueryService, Sort_Field, type Filter, type SearchRequest, type StringPredicate } from "../gen/audit/v1/query_pb.js";
import { Operation, Outcome_Result, RecordSchema, type Record as AuditRecord } from "../gen/audit/v1/record_pb.js";

/** Records a fake service answers from, newest first. */
export function sampleRecords(): AuditRecord[] {
  const at = (minutes: number) => timestampFromDate(new Date(Date.now() - minutes * 60_000));
  return [
    create(RecordSchema, {
      id: "r-3", source: "shop", catalogueVersion: "1.0.0", action: "shop.order.placed",
      operation: Operation.CREATE, tenantId: "acme", profile: "security", occurredAt: at(1), recordedAt: at(1),
      actor: { kind: "customer", id: "ps_alice" }, targets: [{ type: "order", id: "o-3" }],
      outcome: { result: Outcome_Result.SUCCESS }, context: { requestId: "req-3" },
    }),
    create(RecordSchema, {
      id: "r-2", source: "shop", catalogueVersion: "1.0.0", action: "shop.order.placed",
      operation: Operation.CREATE, tenantId: "acme", profile: "security", occurredAt: at(5), recordedAt: at(5),
      actor: { kind: "customer", id: "ps_bob" }, targets: [{ type: "order", id: "o-2" }],
      outcome: { result: Outcome_Result.DENIED, reason: "card declined" },
    }),
    create(RecordSchema, {
      id: "r-1", source: "audit", catalogueVersion: "1.0.0", action: "audit.get",
      operation: Operation.ACCESS, tenantId: "acme", profile: "security", occurredAt: at(9), recordedAt: at(9),
      actor: { kind: "operator", id: "olga" }, targets: [{ type: "record", id: "r-3" }],
      outcome: { result: Outcome_Result.SUCCESS },
    }),
  ];
}

export interface Fake {
  transport: ReturnType<typeof createRouterTransport>;
  /** Every search the service was asked, in order. */
  searches: SearchRequest[];
}

export interface FakeOptions {
  records?: AuditRecord[];
  /** Refuse every search with this code, as a grant would. */
  deny?: Code;
  /** Records the fake says a verified digest covers. */
  verified?: string[];
  /** The profiles access() says the caller may search; default every profile a record is in. */
  readable?: string[];
}

/**
 * An in-memory query service with the archive scan's limits: it orders by
 * occurrence time only, so a tail — which asks for recorded order — is refused
 * the way the real scan refuses it.
 */
export function fakeQueryService(options: FakeOptions = {}): Fake {
  const records = options.records ?? sampleRecords();
  const searches: SearchRequest[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(QueryService, {
      access() {
        const names = options.readable ?? [...new Set(records.map((r) => r.profile))];
        return {
          profiles: names.map((profile) => ({ profile, operations: ["search", "facets", "get"], allTenants: true })),
        };
      },
      search(req) {
        searches.push(req);
        if (options.deny !== undefined) throw new ConnectError("the grant does not cover this profile", options.deny);
        if (req.sort.some((s) => s.field !== Sort_Field.OCCURRED_AT && s.field !== Sort_Field.ID)) {
          throw new ConnectError("this searcher orders by occurred_at only", Code.InvalidArgument);
        }
        const matching = records.filter((r) => r.profile === req.profile && req.filter.every((f) => matches(r, f)));
        const offset = Number(req.cursor || "0");
        const limit = req.limit || 100;
        const items = matching.slice(offset, offset + limit);
        return { items, cursors: { next: String(offset + items.length) } };
      },
      facets(req) {
        const matching = records.filter((r) => r.profile === req.profile && req.filter.every((f) => matches(r, f)));
        return {
          facets: req.fields.map((field) => {
            const counts = new Map<string, number>();
            for (const r of matching) {
              const v = field === "action" ? r.action : (Outcome_Result[r.outcome?.result ?? 0] ?? "").toLowerCase();
              counts.set(v, (counts.get(v) ?? 0) + 1);
            }
            return { field, values: [...counts].map(([value, count]) => ({ value, count: BigInt(count) })) };
          }),
        };
      },
      get(req) {
        const record = records.find((r) => r.id === req.id && r.profile === req.profile);
        if (!record) throw new ConnectError("no such record", Code.NotFound);
        const verified = options.verified?.includes(req.id);
        return {
          record,
          provenance: {
            objectKey: `profile=${req.profile}/x.ndjson.zst`,
            line: 1n,
            ...(verified ? { digestId: "digest/profile=security/hour=10.json", verifiedAt: timestampFromDate(new Date()) } : {}),
          },
        };
      },
    });
  });
  return { transport, searches };
}

function matches(r: AuditRecord, f: Filter): boolean {
  return (
    holds(f.actorId, r.actor?.id ?? "") &&
    holds(f.action, r.action) &&
    holds(f.outcome, (Outcome_Result[r.outcome?.result ?? 0] ?? "").toLowerCase()) &&
    holds(f.tenantId, r.tenantId)
  );
}

function holds(p: StringPredicate | undefined, value: string): boolean {
  if (!p) return true;
  switch (p.operator.case) {
    case "equal":
      return value === p.operator.value;
    case "notEqual":
      return value !== p.operator.value;
    case "in":
      return p.operator.value.values.includes(value);
    case "notIn":
      return !p.operator.value.values.includes(value);
    case "prefix":
      return value.startsWith(p.operator.value);
    default:
      return true;
  }
}
