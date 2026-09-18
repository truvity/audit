import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import { common } from "./catalogue/common.js";
import { Operation, Outcome_Result, RecordSchema } from "./gen/audit/v1/record_pb.js";
import { messageArguments, recordArguments, renderMessage } from "./messages.js";

// The Go validator and this renderer each find a template's arguments with
// their own scanner. Both are held to the same fixture, so a template cannot
// be valid to one and mean something else to the other.
describe("the argument scanner", () => {
  const fixture: { template: string; arguments: string[] }[] = JSON.parse(
    // Tests run from ts/, beside the repository's testdata/.
    readFileSync(resolve(process.cwd(), "../testdata/messages.json"), "utf8"),
  );
  for (const c of fixture) {
    it(`reads ${JSON.stringify(c.template)}`, () => {
      expect(messageArguments(c.template)).toEqual(c.arguments);
    });
  }
});

const record = create(RecordSchema, {
  id: "0199b100-0000-7000-8000-00000000cafe",
  source: "shop",
  action: "shop.order.placed",
  operation: Operation.CREATE,
  tenantId: "acme",
  profile: "security",
  actor: { kind: "customer", id: "ps_alice" },
  subject: { kind: "holder", id: "ps_bob" },
  outcome: { result: Outcome_Result.FAILURE, reason: "card declined", code: "402" },
  observer: { id: "workload:shop", instance: "shop-7" },
  occurredAt: timestampFromDate(new Date("2026-09-18T10:00:00Z")),
  targets: [{ type: "order", id: "o-1", name: "Order 1" }],
  data: { items: 2, channel: "web", address: { city: "Delft" } },
});

describe("rendering", () => {
  it("fills dotted paths, which ICU alone refuses", () => {
    expect(renderMessage("{actor} placed order {targets.0.id}", recordArguments(record))).toBe(
      "ps_alice placed order o-1",
    );
  });

  it("counts with plural on a number from the data slot", () => {
    const t = "{data.items, plural, one {# item} other {# items}} to {data.address.city}";
    expect(renderMessage(t, recordArguments(record))).toBe("2 items to Delft");
  });

  it("chooses with select on the outcome", () => {
    const t = "{outcome, select, success {done} other {refused: {outcome.reason}}}";
    expect(renderMessage(t, recordArguments(record))).toBe("refused: card declined");
  });

  it("leaves a gap where the record carries nothing", () => {
    expect(renderMessage("by {subject.kind} on {targets.3.id}.", recordArguments(record))).toBe("by holder on .");
  });

  it("returns nothing for a template ICU cannot read, so the caller falls back", () => {
    expect(renderMessage("{broken, plural, one {x}", recordArguments(record))).toBeUndefined();
  });

  // Every template the component ships must render. A template the Go
  // validator accepts and FormatJS refuses would read as a bare summary.
  for (const [action, entry] of Object.entries(common.actions)) {
    for (const [locale, template] of Object.entries(entry.message ?? {})) {
      it(`renders ${action} (${locale})`, () => {
        const text = renderMessage(template, recordArguments(record), locale);
        expect(text, template).toBeTypeOf("string");
      });
    }
  }
});
