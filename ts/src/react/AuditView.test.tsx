import { Code } from "@connectrpc/connect";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { createQueryClient } from "../client.js";
import type { Sentences } from "../sentences.js";
import { fakeQueryService, type FakeOptions } from "../testing/fake.js";
import { AuditProvider, AuditView } from "./index.js";

const shop: Sentences = {
  source: "shop",
  version: "1.0.0",
  actions: {
    "shop.order.placed": {
      summary: "A customer placed an order.",
      message: { en: "{actor} placed order {targets.0.id}" },
    },
  },
};

function show(options: FakeOptions = {}, profiles = ["security"]) {
  const fake = fakeQueryService(options);
  render(
    <AuditProvider client={createQueryClient(fake.transport)} sentences={[shop]}>
      <AuditView profiles={profiles} permalink={(p, id) => `/audit/${p}/${id}`} />
    </AuditProvider>,
  );
  return fake;
}

afterEach(cleanup);

describe("the audit view", () => {
  it("shows records as their catalogues' sentences, the application's and the component's own", async () => {
    show();
    expect(await screen.findByText("ps_alice placed order o-3")).toBeTruthy();
    expect(screen.getByText("ps_bob placed order o-2")).toBeTruthy();
    expect(screen.getByText("olga read record r-3")).toBeTruthy();
  });

  it("narrows with the qualifier box, and says what it cannot read", async () => {
    const fake = show();
    await screen.findByText("ps_alice placed order o-3");
    const box = screen.getByRole("textbox", { name: "Narrow" });
    fireEvent.change(box, { target: { value: "actor:ps_bob" } });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(screen.queryByText("ps_alice placed order o-3")).toBeNull());
    expect(screen.getByText("ps_bob placed order o-2")).toBeTruthy();
    const last = fake.searches.at(-1);
    expect(last?.filter[0]?.actorId?.operator).toEqual({ case: "equal", value: "ps_bob" });

    fireEvent.change(box, { target: { value: "colour:red" } });
    fireEvent.keyDown(box, { key: "Enter" });
    expect(await screen.findByText(/colour is not a field/)).toBeTruthy();
  });

  it("opens a row to the record, its integrity and the values to narrow by", async () => {
    const fake = show({ verified: ["r-3"] });
    fireEvent.click(await screen.findByText("ps_alice placed order o-3"));
    expect(await screen.findByText(/^Verified /)).toBeTruthy();
    expect(screen.getByText(/"request_id": "req-3"/)).toBeTruthy();

    fireEvent.click(screen.getByText("request: req-3"));
    await waitFor(() => {
      const box = screen.getByRole("textbox", { name: "Narrow" }) as HTMLInputElement;
      expect(box.value).toBe("request:req-3");
    });
    expect(fake.searches.at(-1)?.filter[0]?.requestId?.operator).toEqual({ case: "equal", value: "req-3" });
  });

  it("says a record is not yet covered when no verified digest is", async () => {
    show();
    fireEvent.click(await screen.findByText("ps_bob placed order o-2"));
    expect(await screen.findByText("Not yet covered by a verified digest")).toBeTruthy();
  });

  it("counts values to narrow by", async () => {
    show();
    const counts = await screen.findByRole("navigation", { name: "Counts" });
    fireEvent.click(within(counts).getByText("denied"));
    await waitFor(() => expect(screen.queryByText("ps_alice placed order o-3")).toBeNull());
    expect(screen.getByText("ps_bob placed order o-2")).toBeTruthy();
  });

  it("says plainly when the sign-in may not read the profile", async () => {
    show({ deny: Code.PermissionDenied });
    expect(await screen.findByText(/This sign-in may not read that/)).toBeTruthy();
  });

  it("says live updates need the index when the searcher cannot follow recorded order", async () => {
    show();
    await screen.findByText("ps_alice placed order o-3");
    fireEvent.click(screen.getByRole("switch", { name: "Live" }));
    expect(await screen.findByText(/Live updates are not available here/)).toBeTruthy();
  });

  it("shows nothing to read when no profile is readable", () => {
    show({}, []);
    expect(screen.getByText(/No audit profile is readable/)).toBeTruthy();
  });

  it("switches profiles along the top", async () => {
    const fake = show({}, ["security", "history"]);
    await screen.findByText("ps_alice placed order o-3");
    fireEvent.click(screen.getByRole("tab", { name: "history" }));
    expect(await screen.findByText("Nothing recorded matches.")).toBeTruthy();
    expect(fake.searches.at(-1)?.profile).toBe("history");
  });
});
