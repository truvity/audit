import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  FilterSchema,
  PathPredicateSchema,
  StringPredicateSchema,
  TargetPredicateSchema,
  TargetRefSchema,
  TimePredicateSchema,
  type Filter,
  type StringPredicate,
  type TargetRef,
} from "./gen/audit/v1/query_pb.js";

/**
 * The qualifier box: what a person types to narrow a search, compiled to the
 * typed filter the service takes. It never becomes free text on the server —
 * the contract has none — so every token either names a field or is an error
 * the box shows.
 *
 *   actor:alice              the actor, exactly
 *   action:wallet.issue*     a trailing * is a prefix
 *   outcome:failure,denied   a comma is any of
 *   -outcome:success         a leading - excludes
 *   target:credential:c-1    a target by type and id; target:credential any of that type
 *   data.channel:web         a filterable property of the data slot, by its path
 *   since:24h until:2026-09-18   the time the event occurred, relative or absolute
 *   "request:a b"            quotes keep a value with spaces together
 */

/** The fields a qualifier may name, and the filter field each fills. */
const stringFields = {
  id: "id",
  action: "action",
  source: "source",
  operation: "operation",
  outcome: "outcome",
  tenant: "tenantId",
  actor: "actorId",
  "actor.kind": "actorKind",
  subject: "subjectId",
  "subject.kind": "subjectKind",
  request: "requestId",
  trace: "traceId",
  client: "clientAddress",
  observer: "observerId",
  meter: "meterName",
} as const satisfies { [qualifier: string]: keyof Filter };

type StringQualifier = keyof typeof stringFields;

/** The qualifiers the box understands, for its help text. */
export const qualifierNames: readonly string[] = [
  ...Object.keys(stringFields),
  "target",
  "data.<path>",
  "since",
  "until",
];

export interface Compiled {
  filter: Filter;
  /** One message per token the box could not read; the filter omits them. */
  errors: string[];
}

interface Clause {
  include: string[];
  exclude: string[];
}

/** Compiles the box's text. `now` is for relative times, and for tests. */
export function compileQualifiers(text: string, now: Date = new Date()): Compiled {
  const errors: string[] = [];
  const strings = new Map<string, Clause>();
  const targets: { include: TargetRef[]; exclude: TargetRef[] } = { include: [], exclude: [] };
  const data = new Map<string, Clause>();
  let since: Date | undefined;
  let until: Date | undefined;

  for (const token of tokenize(text)) {
    let negate = false;
    let body = token;
    if (body.startsWith("-")) {
      negate = true;
      body = body.slice(1);
    }
    const colon = body.indexOf(":");
    if (colon <= 0 || colon === body.length - 1) {
      errors.push(`"${token}" names no field: write field:value, e.g. actor:${token}`);
      continue;
    }
    const field = body.slice(0, colon);
    const value = body.slice(colon + 1);
    const values = value.split(",").filter((v) => v !== "");

    if (field === "since" || field === "until") {
      if (negate) {
        errors.push(`"${token}": a time bound cannot be excluded`);
        continue;
      }
      const at = parseTime(value, now);
      if (!at) {
        errors.push(`"${token}": ${value} is neither a duration like 24h or 7d nor a date`);
        continue;
      }
      if (field === "since") since = at;
      else until = at;
    } else if (field === "target") {
      const refs = values.map(targetRef);
      (negate ? targets.exclude : targets.include).push(...refs);
    } else if (field.startsWith("data.") && field.length > 5) {
      add(data, field.slice(5), values, negate);
    } else if (field in stringFields) {
      add(strings, field, values, negate);
    } else {
      errors.push(`"${token}": ${field} is not a field; use one of ${qualifierNames.join(", ")}`);
    }
  }

  const filter = create(FilterSchema);
  for (const [field, clause] of strings) {
    const predicate = stringPredicate(clause, field, errors);
    if (predicate) filter[stringFields[field as StringQualifier]] = predicate as never;
  }
  for (const [path, clause] of data) {
    const predicate = stringPredicate(clause, `data.${path}`, errors);
    if (predicate) {
      filter.data.push(
        create(PathPredicateSchema, {
          path: "/" + path.split(".").join("/"),
          predicate: { case: "string", value: predicate },
        }),
      );
    }
  }
  if (targets.include.length > 0 && targets.exclude.length > 0) {
    errors.push("target is used both to include and to exclude; the service takes one or the other");
  } else if (targets.include.length > 0 || targets.exclude.length > 0) {
    filter.targets = create(TargetPredicateSchema, {
      operator: targets.include.length > 0
        ? { case: "in", value: { values: targets.include } }
        : { case: "notIn", value: { values: targets.exclude } },
    });
  }
  if (since || until) {
    filter.occurredAt = create(TimePredicateSchema, {
      operator: since && until
        ? { case: "between", value: { from: timestampFromDate(since), to: timestampFromDate(until) } }
        : since
          ? { case: "greaterThanOrEqual", value: timestampFromDate(since) }
          : { case: "lessThan", value: timestampFromDate(until as Date) },
    });
  }
  return { filter, errors };
}

/**
 * The token that narrows to — or, negated, away from — one value of a field:
 * what a row's filter-for and filter-out buttons append to the box.
 */
export function qualifier(field: string, value: string, exclude = false): string {
  const needsQuotes = /[\s"]/.test(value);
  const token = `${exclude ? "-" : ""}${field}:${value}`;
  return needsQuotes ? `"${token.replace(/"/g, "")}"` : token;
}

function add(into: Map<string, Clause>, field: string, values: string[], negate: boolean): void {
  const clause = into.get(field) ?? { include: [], exclude: [] };
  (negate ? clause.exclude : clause.include).push(...values);
  into.set(field, clause);
}

// stringPredicate is the one predicate a field's clauses make. The contract
// has one operator per field, so a field both included and excluded, or a
// prefix among several values, is refused rather than guessed at.
function stringPredicate(clause: Clause, field: string, errors: string[]): StringPredicate | undefined {
  const { include, exclude } = clause;
  if (include.length > 0 && exclude.length > 0) {
    errors.push(`${field} is used both to include and to exclude; the service takes one or the other`);
    return undefined;
  }
  const values = include.length > 0 ? include : exclude;
  const negate = include.length === 0;
  const prefixes = values.filter((v) => v.endsWith("*"));
  if (prefixes.length > 0) {
    if (values.length > 1 || negate) {
      errors.push(`${field}: a prefix (${prefixes[0]}) stands alone and cannot be excluded`);
      return undefined;
    }
    return create(StringPredicateSchema, { operator: { case: "prefix", value: values[0]!.slice(0, -1) } });
  }
  if (values.length === 1) {
    return create(StringPredicateSchema, {
      operator: { case: negate ? "notEqual" : "equal", value: values[0]! },
    });
  }
  return create(StringPredicateSchema, {
    operator: { case: negate ? "notIn" : "in", value: { values } },
  });
}

function targetRef(value: string): TargetRef {
  const colon = value.indexOf(":");
  return create(
    TargetRefSchema,
    colon < 0 ? { type: value } : { type: value.slice(0, colon), id: value.slice(colon + 1) },
  );
}

// parseTime reads a duration back from now (30m, 24h, 7d) or a date.
function parseTime(value: string, now: Date): Date | undefined {
  const relative = /^(\d+)([mhd])$/.exec(value);
  if (relative) {
    const n = Number(relative[1]);
    const unit = { m: 60_000, h: 3_600_000, d: 86_400_000 }[relative[2] as "m" | "h" | "d"];
    return new Date(now.getTime() - n * unit);
  }
  if (!/^\d{4}-\d{2}-\d{2}/.test(value)) return undefined;
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? undefined : at;
}

// tokenize splits on whitespace, keeping a double-quoted run together.
function tokenize(text: string): string[] {
  const out: string[] = [];
  const re = /"([^"]*)"|(\S+)/g;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    out.push(m[1] ?? m[2] ?? "");
  }
  return out.filter((t) => t !== "");
}
