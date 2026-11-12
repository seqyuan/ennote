import { describe, expect, it } from "vitest";

import { MAX_PERSISTED_INSPECTOR_TABS } from "@/lib/inspector-tabs";
import {
  fileTabId,
  inspectorTabId,
  parseInspectorTabRows,
  pathOf,
  rowMatchesId,
  type ArtifactInspectorRow,
  type FileInspectorRow,
  type InspectorTabRow,
} from "@/lib/inspector-tabs";

/** A file row, as the row model builds one. */
const fileRow = (path: string, pane: "main" | "side" = "main"): FileInspectorRow =>
  ({ kind: "file", path, name: path.split("/").pop() ?? path, pane });

/** An artifact row: a widget published in a session. */
const artifactRow = (artifactId: string, sessionId = "s1", pane: "main" | "side" = "main"): ArtifactInspectorRow =>
  ({ kind: "artifact", artifactId, sessionId, name: `widget ${artifactId}`, pane });

const idsOf = (rows: readonly InspectorTabRow[]) => rows.map((row) => inspectorTabId("p1", row));

describe("inspector row ids", () => {
  it("keeps the file id byte-identical to what earlier releases stored", () => {
    // 0.10.0/0.11.0 wrote `file:<projectId>:<path>` into activeInspectorIds and
    // compare it after a reload. Changing a single byte here silently unselects
    // every restored tab, so the literal is pinned rather than derived.
    expect(inspectorTabId("p1", fileRow("/workspace/a.ts"))).toBe("file:p1:/workspace/a.ts");
    expect(inspectorTabId("p1", fileRow("/workspace/a.ts"))).toBe(fileTabId("p1", "/workspace/a.ts"));
  });

  it("names an artifact row after its project, session and artifact", () => {
    // Both ids are in the string because the row is the pair; and because an
    // artifact id being unique across the Worker is the Worker's property, not
    // something this model may assume.
    expect(inspectorTabId("p1", artifactRow("art_1", "s9"))).toBe("artifact:p1:s9:art_1");
  });

  it("matches an id only against the row it names", () => {
    const file = fileRow("/workspace/art_1");
    const artifact = artifactRow("art_1");

    expect(rowMatchesId("p1", file, "file:p1:/workspace/art_1")).toBe(true);
    expect(rowMatchesId("p1", artifact, "artifact:p1:s1:art_1")).toBe(true);
    // The peers share neither an id nor a kind prefix: a file whose path looks
    // like an artifact id must not be the row that selection points at.
    expect(rowMatchesId("p1", artifact, "file:p1:/workspace/art_1")).toBe(false);
    expect(rowMatchesId("p1", file, "artifact:p1:s1:art_1")).toBe(false);
    expect(rowMatchesId("p1", file, "file:p2:/workspace/art_1")).toBe(false);
    // The session is half of the identity, so the same artifact id published in
    // another session is another row.
    expect(rowMatchesId("p1", artifact, "artifact:p1:s2:art_1")).toBe(false);
  });

  it("reads a colon-bearing path back out of a file id", () => {
    const path = "/workspace/reports/run:42.csv";
    expect(pathOf(fileTabId("p1", path))).toBe(path);
  });
});

describe("reading persisted rows", () => {
  it("reads a row written without a kind as a file row", () => {
    // 0.11.0's shape, verbatim. "no kind means no row" would throw away the
    // files a reader had open on their last visit.
    const rows = parseInspectorTabRows([{ path: "/workspace/a.ts", name: "a.ts", pane: "side" }], "p1");

    expect(rows).toEqual([{ kind: "file", path: "/workspace/a.ts", name: "a.ts", pane: "side" }]);
    expect(idsOf(rows)).toEqual(["file:p1:/workspace/a.ts"]);
  });

  it("reads a row written before panes existed into the main pane", () => {
    expect(parseInspectorTabRows([{ path: "/workspace/a.ts", name: "a.ts" }], "p1"))
      .toEqual([{ kind: "file", path: "/workspace/a.ts", name: "a.ts", pane: "main" }]);
  });

  it("names a file row after its last path segment when the name is missing", () => {
    expect(parseInspectorTabRows([{ path: "/workspace/deep/a.ts" }], "p1"))
      .toEqual([{ kind: "file", path: "/workspace/deep/a.ts", name: "a.ts", pane: "main" }]);
  });

  it("reads an artifact row and keeps its session", () => {
    const rows = parseInspectorTabRows([
      { kind: "artifact", artifactId: "art_1", sessionId: "s1", name: "Sales", pane: "side" },
    ], "p1");

    expect(rows).toEqual([{ kind: "artifact", artifactId: "art_1", sessionId: "s1", name: "Sales", pane: "side" }]);
    expect(idsOf(rows)).toEqual(["artifact:p1:s1:art_1"]);
  });

  it("leaves an artifact row's label empty when the stored name is missing", () => {
    // The model does not invent a label: an English word stored here would be
    // read back as if it were the artifact's name, and the surface that draws
    // the row is what localises the fallback.
    const rows = parseInspectorTabRows([{ kind: "artifact", artifactId: "art_1", sessionId: "s1" }], "p1");
    expect(rows[0]).toMatchObject({ kind: "artifact", name: "", pane: "main" });
  });

  it("drops an artifact row that is missing one of its ids instead of downgrading it", () => {
    // An artifact row has no path, so "treat it as the old shape" could only
    // invent an empty file tab: a tab that draws and clicks into nothing.
    expect(parseInspectorTabRows([{ kind: "artifact", artifactId: "art_1" }], "p1")).toEqual([]);
    expect(parseInspectorTabRows([{ kind: "artifact", sessionId: "s1" }], "p1")).toEqual([]);
    expect(parseInspectorTabRows([{ kind: "artifact", artifactId: "", sessionId: "s1" }], "p1")).toEqual([]);
    // Even when a stray path is sitting next to the half-written ids.
    expect(parseInspectorTabRows([{ kind: "artifact", path: "/workspace/a.ts" }], "p1")).toEqual([]);
  });

  it("reads a kind it does not know as the old shape", () => {
    expect(parseInspectorTabRows([{ kind: "board", path: "/workspace/a.ts", name: "a.ts" }], "p1"))
      .toEqual([{ kind: "file", path: "/workspace/a.ts", name: "a.ts", pane: "main" }]);
    // …which still needs a path: an unknown kind alone is not a row.
    expect(parseInspectorTabRows([{ kind: "board", name: "board" }], "p1")).toEqual([]);
  });

  it("drops junk, an unknown pane and a missing value", () => {
    expect(parseInspectorTabRows(undefined, "p1")).toEqual([]);
    expect(parseInspectorTabRows(null, "p1")).toEqual([]);
    expect(parseInspectorTabRows({ kind: "file", path: "/a" }, "p1")).toEqual([]);
    expect(parseInspectorTabRows([], "p1")).toEqual([]);
    expect(parseInspectorTabRows([null, "row", 7, [], { name: "no path" }], "p1")).toEqual([]);
    expect(parseInspectorTabRows([{ path: "/workspace/a.ts", name: "a.ts", pane: "center" }], "p1")[0].pane)
      .toBe("main");
  });

  it("keeps the same-named file in each pane", () => {
    // Two panes can hold a file with the same base name; identity is the path
    // (and the id built from it), never the label the strip draws.
    const rows = parseInspectorTabRows([
      { path: "/workspace/left/index.ts", name: "index.ts", pane: "main" },
      { path: "/workspace/right/index.ts", name: "index.ts", pane: "side" },
    ], "p1");

    expect(rows.map((row) => [row.pane, row.kind === "file" ? row.path : null])).toEqual([
      ["main", "/workspace/left/index.ts"],
      ["side", "/workspace/right/index.ts"],
    ]);
  });

  it("de-duplicates by id, keeping the first write", () => {
    const rows = parseInspectorTabRows([
      { path: "/workspace/a.ts", name: "first" },
      { path: "/workspace/a.ts", name: "second" },
      { kind: "artifact", artifactId: "art_1", sessionId: "s1", name: "first" },
      { kind: "artifact", artifactId: "art_1", sessionId: "s1", name: "second" },
      // A file row and an artifact row never collide, even with the same tail.
      { path: "art_1" },
    ], "p1");

    expect(rows.map((row) => row.name)).toEqual(["first", "first", "art_1"]);
    expect(idsOf(rows)).toEqual(["file:p1:/workspace/a.ts", "artifact:p1:s1:art_1", "file:p1:art_1"]);
  });

  it("keeps the newest rows when a Project has opened many of them", () => {
    const stored = Array.from({ length: MAX_PERSISTED_INSPECTOR_TABS + 5 }, (_, index) => ({
      path: `/workspace/f${index}.ts`, name: `f${index}.ts`,
    }));
    const rows = parseInspectorTabRows(stored, "p1");

    expect(rows).toHaveLength(MAX_PERSISTED_INSPECTOR_TABS);
    expect(rows[0]).toMatchObject({ path: "/workspace/f5.ts" });
    expect(rows[rows.length - 1]).toMatchObject({ path: `/workspace/f${MAX_PERSISTED_INSPECTOR_TABS + 4}.ts` });
  });

  it("caps a list that mixes both kinds the same way", () => {
    const mixed = Array.from({ length: MAX_PERSISTED_INSPECTOR_TABS + 2 }, (_, index) => (index % 2 === 0
      ? { kind: "artifact", artifactId: `art_${index}`, sessionId: "s1" }
      : { path: `/workspace/f${index}.ts` }));
    const rows = parseInspectorTabRows(mixed, "p1");

    expect(rows).toHaveLength(MAX_PERSISTED_INSPECTOR_TABS);
    expect(idsOf(rows)[0]).toBe("artifact:p1:s1:art_2");
  });
});
