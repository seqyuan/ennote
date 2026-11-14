import { describe, expect, it } from "vitest";
import { taskFieldChanged, taskFingerprint, thinkingForModel } from "@/components/graphs/TaskAccordion";
import type { GraphTask } from "@/components/graphs/types";

const task: GraphTask = { name: "Align", model: "openai-main/gpt-5", goal: "Align reads" };

// A blur is not a request. Before this, every tab across a field sent a PATCH even
// when the value had not changed.
describe("taskFieldChanged", () => {
  it("reports no change for an identical scalar", () => {
    expect(taskFieldChanged("Align", "Align")).toBe(false);
  });

  it("reports a change for a different scalar", () => {
    expect(taskFieldChanged("Align", "Align reads")).toBe(true);
  });

  it("treats an absent list and an empty list as the same", () => {
    expect(taskFieldChanged(undefined, [])).toBe(false);
  });

  it("compares list contents in order", () => {
    expect(taskFieldChanged(["local/a", "global/b"], ["local/a", "global/b"])).toBe(false);
    expect(taskFieldChanged(["local/a", "global/b"], ["global/b", "local/a"])).toBe(true);
    expect(taskFieldChanged(["local/a"], ["local/a", "global/b"])).toBe(true);
  });
});

// The form mounts on this fingerprint. Content that did not change must not
// remount (that would steal focus mid-edit); content that did change must, so a
// Builder apply is visible instead of a stale defaultValue.
describe("taskFingerprint", () => {
  it("is stable for an unchanged Task", () => {
    expect(taskFingerprint({ ...task })).toBe(taskFingerprint({ ...task }));
  });

  it("changes when the stored Task changes", () => {
    expect(taskFingerprint({ ...task, goal: "Different" })).not.toBe(taskFingerprint({ ...task }));
  });

  it("changes when the Worker normalises the Task into a different shape", () => {
    expect(taskFingerprint({ ...task, skills: [] })).not.toBe(taskFingerprint(task));
  });
});

// Switching to a model that cannot honour the current effort used to leave a
// selection the UI then hid, and the Run rejected it at freeze time.
describe("thinkingForModel", () => {
  it("keeps the effort the new model offers", () => {
    expect(thinkingForModel(true, ["default", "high"], "high")).toBe("high");
  });

  it("resets to default when the new model offers something else", () => {
    expect(thinkingForModel(true, ["default", "low"], "high")).toBe("default");
  });

  it("resets to default when the new model has no thinking support", () => {
    expect(thinkingForModel(false, ["default", "high"], "high")).toBe("default");
  });

  it("defaults an unset effort", () => {
    expect(thinkingForModel(true, ["default", "high"], undefined)).toBe("default");
  });
});
