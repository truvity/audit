import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { RecordSchema } from "./gen/audit/v1/record_pb.js";
import { Sentencer, type Sentences } from "./sentences.js";

const shop = (version: string, en: string): Sentences => ({
  source: "shop",
  version,
  actions: { "shop.order.placed": { summary: "An order was placed.", message: { en } } },
});

describe("sentences", () => {
  it("uses the template of the version the record was written under", () => {
    const s = new Sentencer([shop("1.0.0", "old {actor}"), shop("2.0.0", "new {actor}")]);
    const r = create(RecordSchema, {
      source: "shop", catalogueVersion: "1.0.0", action: "shop.order.placed", actor: { id: "a" },
    });
    expect(s.sentence(r)).toBe("old a");
  });

  it("falls back to the newest version that has the action", () => {
    const s = new Sentencer([shop("1.9.0", "older {actor}"), shop("1.10.0", "newer {actor}")]);
    const r = create(RecordSchema, {
      source: "shop", catalogueVersion: "0.1.0", action: "shop.order.placed", actor: { id: "a" },
    });
    expect(s.sentence(r)).toBe("newer a");
  });

  it("knows the audit component's own actions without being told", () => {
    const r = create(RecordSchema, {
      source: "audit", catalogueVersion: "1.0.0", action: "audit.get",
      actor: { id: "olga" }, targets: [{ type: "record", id: "r-1" }],
    });
    expect(new Sentencer().sentence(r)).toBe("olga read record r-1");
  });

  it("never leaves a record without words", () => {
    const broken = new Sentencer([shop("1.0.0", "{actor, plural, one {x}")]);
    const r = create(RecordSchema, { source: "shop", catalogueVersion: "1.0.0", action: "shop.order.placed" });
    expect(broken.sentence(r)).toBe("An order was placed.");
    expect(new Sentencer().sentence(create(RecordSchema, { source: "x", action: "x.y" }))).toBe("x.y");
  });
});
