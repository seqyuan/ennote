import { describe, expect, it } from "vitest";
import { createSaveGate, mergeSavedDetail } from "@/components/settings/HeroesSettings";

// Regression: the editor returned early whenever a save was in flight, so an
// edit made during that round trip was thrown away while the header still said
// Saved. A single gate plus a "edited while saving" flag makes the save serial
// and makes the second round submit what the user sees.
describe("createSaveGate", () => {
  it("refuses a second concurrent save", () => {
    const gate = createSaveGate();

    expect(gate.enter()).toBe(true);
    expect(gate.enter()).toBe(false);
    expect(gate.running).toBe(true);
  });

  it("reports an edit made while saving exactly once", () => {
    const gate = createSaveGate();

    gate.enter();
    gate.markEdited();
    expect(gate.takeEdited()).toBe(true);
    expect(gate.takeEdited()).toBe(false);
  });

  it("ignores edits marked while no save is running", () => {
    const gate = createSaveGate();

    gate.markEdited();
    expect(gate.enter()).toBe(true);
    expect(gate.takeEdited()).toBe(false);
  });

  it("accepts a new save after the previous one left", () => {
    const gate = createSaveGate();

    gate.enter();
    gate.markEdited();
    gate.leave();

    expect(gate.takeEdited()).toBe(false);
    expect(gate.enter()).toBe(true);
  });
});

// The Worker normalises the document it stores. Adopting its document would
// overwrite text the user typed during the round trip, so only the digest is
// taken when an edit landed mid-save.
describe("mergeSavedDetail", () => {
  it("keeps the local document and adopts the returned digest while an edit landed", () => {
    const updated = { id: "reviewer", digest: "sha256:new", document: { prompt: "worker normalised" } };
    const local = { prompt: "user typed this" };

    expect(mergeSavedDetail(updated as never, local as never)).toMatchObject({
      digest: "sha256:new",
      document: local,
    });
  });

  it("adopts the returned document when no edit landed", () => {
    const updated = { id: "reviewer", digest: "sha256:new", document: { prompt: "worker normalised" } };

    expect(mergeSavedDetail(updated as never, null).document).toEqual({ prompt: "worker normalised" });
  });
});

