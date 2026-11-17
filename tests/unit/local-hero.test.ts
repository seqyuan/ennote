import { describe, expect, it } from "vitest";

import { defaultLocalHero, localHeroHandleFor, localHeroId, parseToolList } from "@/lib/local-hero";

describe("localHeroHandleFor", () => {
  it("accepts a task id that is also a valid Hero handle", () => {
    expect(localHeroHandleFor("align")).toBe("align");
    expect(localHeroHandleFor(" qc-check_2 ")).toBe("qc-check_2");
  });

  it("refuses ids that could not be a directory name", () => {
    // The Worker's object-id rule is stricter than the Task id the form accepts.
    expect(localHeroHandleFor("2pass")).toBeNull();
    expect(localHeroHandleFor("Align")).toBeNull();
    expect(localHeroHandleFor("a")).toBeNull();
    expect(localHeroHandleFor("x".repeat(33))).toBeNull();
    expect(localHeroHandleFor("has space")).toBeNull();
  });
});

describe("defaultLocalHero", () => {
  const hero = defaultLocalHero("align", "Align", "provider/model");

  it("starts from a task_only context, because a Graph task child always is", () => {
    expect(hero.context.defaultMode).toBe("task_only");
    expect(hero.context.allowedModes).toEqual(["task_only"]);
  });

  it("carries the required PortableHeroDocument fields", () => {
    for (const key of ["schemaVersion", "handle", "name", "model", "skills", "authority",
      "permissionCeiling", "allowedTools", "context", "delegation", "outputContract",
      "maxLoopIterations", "prompt"] as const) {
      expect(hero).toHaveProperty(key);
    }
    expect(hero.handle).toBe("align");
    expect(hero.model.ref).toBe("provider/model");
  });

  it("is read-only by default: a Task can be turned write-capable deliberately", () => {
    expect(hero.authority).toBe("read_only");
    expect(hero.allowedTools).not.toContain("bash");
  });
});

describe("localHeroId", () => {
  it("reads the handle out of a local reference", () => {
    expect(localHeroId("local/align")).toBe("align");
    expect(localHeroId("  local/review  ")).toBe("review");
  });

  it("ignores every other reference", () => {
    expect(localHeroId("global/reviewer")).toBeNull();
    expect(localHeroId("local/")).toBeNull();
    expect(localHeroId(undefined)).toBeNull();
  });
});

describe("parseToolList", () => {
  it("trims and drops empty entries", () => {
    expect(parseToolList("read, write ,, bash ")).toEqual(["read", "write", "bash"]);
    expect(parseToolList("")).toEqual([]);
  });
});
