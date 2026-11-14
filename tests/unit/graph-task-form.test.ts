import { describe, expect, it } from "vitest";
import { taskFieldChanged, taskFingerprint } from "@/components/graphs/TaskAccordion";
import type { FlowTask } from "@/components/graphs/types";

const task: FlowTask = { hero: "local/align", goal: "Align reads" };

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
