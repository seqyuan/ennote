import { describe, expect, it } from "vitest";

import { resolveActiveTabId, scopeFileTabs } from "@/lib/file-tabs";

const tab = (projectId: string, path: string) => ({
  id: `file:${projectId}:${path}`,
  label: path,
  filePath: path,
  projectId,
});

describe("right-panel file tab scoping", () => {
  it("returns only the current project's rows", () => {
    const tabs = [tab("a", "one.ts"), tab("b", "two.ts")];

    expect(scopeFileTabs(tabs, "a").map((item) => item.id)).toEqual(["file:a:one.ts"]);
    expect(scopeFileTabs(tabs, "b").map((item) => item.id)).toEqual(["file:b:two.ts"]);
  });

  it("returns no rows while no project is selected", () => {
    expect(scopeFileTabs([tab("a", "one.ts")], null)).toEqual([]);
  });

  it("keeps a row selected inside its own project", () => {
    const scoped = ["file:a:one.ts", "file:a:two.ts"];
    expect(resolveActiveTabId("file:a:two.ts", scoped)).toBe("file:a:two.ts");
  });

  it("falls back to the Files panel when the selected row belongs to another project", () => {
    expect(resolveActiveTabId("file:a:one.ts", ["file:b:two.ts"])).toBe("files");
    expect(resolveActiveTabId("file:a:one.ts", [])).toBe("files");
  });

  it("keeps the workspace panels selected across a project switch", () => {
    for (const panel of ["files", "graph", "tools", "canvas"]) {
      expect(resolveActiveTabId(panel, [])).toBe(panel);
    }
  });
});
