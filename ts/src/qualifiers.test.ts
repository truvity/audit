import { timestampDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import { compileQualifiers, qualifier } from "./qualifiers.js";

const now = new Date("2026-09-18T12:00:00Z");

describe("the qualifier box", () => {
  it("compiles each kind of token to its predicate", () => {
    const { filter, errors } = compileQualifiers(
      'actor:alice action:wallet.* outcome:failure,denied -operation:access target:credential:c-1 data.channel:web tenant:acme',
      now,
    );
    expect(errors).toEqual([]);
    expect(filter.actorId?.operator).toEqual({ case: "equal", value: "alice" });
    expect(filter.action?.operator).toEqual({ case: "prefix", value: "wallet." });
    expect(filter.outcome?.operator.case).toBe("in");
    expect(filter.operation?.operator).toEqual({ case: "notEqual", value: "access" });
    expect(filter.tenantId?.operator).toEqual({ case: "equal", value: "acme" });
    expect(filter.targets?.operator.case).toBe("in");
    const target = filter.targets?.operator.value?.values[0];
    expect([target?.type, target?.id]).toEqual(["credential", "c-1"]);
    expect(filter.data[0]?.path).toBe("/channel");
  });

  it("reads relative and absolute times as the occurred range", () => {
    const { filter } = compileQualifiers("since:24h until:2026-09-18", now);
    const range = filter.occurredAt?.operator;
    expect(range?.case).toBe("between");
    if (range?.case !== "between") return;
    expect(timestampDate(range.value.from!).toISOString()).toBe("2026-09-17T12:00:00.000Z");
    expect(timestampDate(range.value.to!).toISOString()).toBe("2026-09-18T00:00:00.000Z");
  });

  it("says what it could not read, and leaves it out", () => {
    const { filter, errors } = compileQualifiers("alice colour:red actor:a -actor:b action:x*,y since:soon", now);
    expect(errors).toHaveLength(5);
    expect(filter.actorId).toBeUndefined();
    expect(filter.action).toBeUndefined();
  });

  it("keeps a quoted value with spaces together", () => {
    const { filter, errors } = compileQualifiers('"request:a b"', now);
    expect(errors).toEqual([]);
    expect(filter.requestId?.operator).toEqual({ case: "equal", value: "a b" });
  });

  it("builds the tokens a row's filter buttons append", () => {
    expect(qualifier("actor", "alice")).toBe("actor:alice");
    expect(qualifier("outcome", "success", true)).toBe("-outcome:success");
    expect(qualifier("request", "a b")).toBe('"request:a b"');
    const round = compileQualifiers(qualifier("request", "a b"), now);
    expect(round.filter.requestId?.operator).toEqual({ case: "equal", value: "a b" });
  });
});
