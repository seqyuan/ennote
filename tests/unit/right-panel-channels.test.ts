import { describe, expect, it } from "vitest";

import {
  MAX_PERSISTED_INSPECTOR_TABS,
  PANEL_CHANNELS,
  fileTabId,
  type PanelChannel,
  type RightPanelPreferencesV1,
  type RightPanelState,
  closeChannel,
  closeInspectorTab,
  collapseShell,
  emptyPreferences,
  initialRightPanelState,
  isPanelChannel,
  openChannel,
  openInspectorTab,
  parsePreferences,
  reorderInspectorTabs,
  pinChannel,
  preferencesOf,
  rightPanelPreferencesKey,
  serializePreferences,
  expandShell,
  setNavigatorHidden,
  setPrimaryWidth,
  unpinChannel,
} from "@/lib/right-panel-channels";
import {
  inspectorTabId,
  type ArtifactInspectorRow,
  type FileInspectorRow,
  type InspectorTabRow,
} from "@/lib/inspector-tabs";

const prefs = (over: Partial<RightPanelPreferencesV1> = {}): RightPanelPreferencesV1 => ({
  version: 1,
  projectId: "p1",
  pinnedChannels: [],
  primaryWidth: 280,
  open: false,
  ...over,
});

/** The row id the panel uses for a file in the fixture project. */
const id = (path: string) => fileTabId("p1", path);

/** A file row, as the panel stores it. Named apart from the row-model suite's
 * fixture because the two files are separate suites: a shared name for two
 * definitions is how the wrong one gets edited.
 */
const fileRowIn = (path: string, pane: "main" | "side" = "main"): FileInspectorRow =>
  ({ kind: "file", path, name: path.split("/").pop() ?? path, pane });

/** An artifact row, opened into the same strip as a file. The label is a
 * placeholder for the test only; the panel draws `row.name`.
 */
const artifactRowIn = (artifactId: string, sessionId = "s1", pane: "main" | "side" = "main"): ArtifactInspectorRow =>
  ({ kind: "artifact", artifactId, sessionId, name: `widget ${artifactId}`, pane });

/** The row id the panel selects for an artifact in the fixture project. */
const artifactId = (artifact: string, sessionId = "s1") =>
  inspectorTabId("p1", artifactRowIn(artifact, sessionId));

/** The file halves of a row list; artifact rows have their own suite. */
const fileRows = (rows: readonly InspectorTabRow[] | undefined): FileInspectorRow[] =>
  (rows ?? []).filter((row): row is FileInspectorRow => row.kind === "file");

/** The artifact halves of a row list. */
const artifactRows = (rows: readonly InspectorTabRow[] | undefined): ArtifactInspectorRow[] =>
  (rows ?? []).filter((row): row is ArtifactInspectorRow => row.kind === "artifact");

/** A state as a fresh visit to a project with these pins produces it. */
const state = (over: Partial<RightPanelState> = {}): RightPanelState => ({
  ...initialRightPanelState(prefs()),
  ...over,
});

describe("right panel channel contract", () => {
  it("keeps only the pins once the user has pinned", () => {
    const hydrated = initialRightPanelState(prefs({ pinnedChannels: ["tools"] }));

    expect(hydrated.openChannels).toEqual(["tools"]);
    expect(hydrated.activeChannelId).toBe("tools");
  });

  it("selects an already-open channel without touching the open set or the pins", () => {
    const before = state({ openChannels: ["files", "graph"], activeChannelId: "files" });
    const after = openChannel(before, "graph");

    expect(after.openChannels).toEqual(["files", "graph"]);
    expect(after.pinnedChannels).toEqual([]);
    expect(after.activeChannelId).toBe("graph");
    expect(after.open).toBe(true);
  });

  it("reopens a channel the user had closed, without pinning it", () => {
    const after = openChannel(closeChannel(state(), "tools"), "tools");

    expect(after.openChannels).toEqual(["files", "graph", "canvas", "tools"]);
    expect(after.pinnedChannels).toEqual([]);
    expect(after.activeChannelId).toBe("tools");
  });

  it("pins a channel and seeds it on the next visit", () => {
    const after = pinChannel(state(), "graph");
    expect(after.pinnedChannels).toEqual(["graph"]);

    const next = initialRightPanelState(preferencesOf(after));
    expect(next.openChannels).toEqual(["graph"]);
    expect(next.activeChannelId).toBe("graph");
  });

  it("keeps an unpinned channel open until it is closed", () => {
    const pinned = pinChannel(state(), "graph");
    const unpinned = unpinChannel(pinned, "graph");

    expect(unpinned.pinnedChannels).toEqual([]);
    expect(unpinned.openChannels).toEqual(["files", "graph", "tools", "canvas"]);
    expect(unpinned.activeChannelId).toBe("graph");
  });

  it("keeps a channel pinned when the user closes its tab", () => {
    const pinned = pinChannel(state(), "graph");
    const closed = closeChannel(pinned, "graph");

    expect(closed.openChannels).toEqual(["files", "tools", "canvas"]);
    expect(closed.pinnedChannels).toEqual(["graph"]);
    expect(closed.open).toBe(true);
  });

  it("collapses the shell without touching the open set or the pins", () => {
    const pinned = pinChannel(state({ activeChannelId: "graph" }), "graph");
    const collapsed = collapseShell(pinned);

    expect(collapsed.open).toBe(false);
    expect(collapsed.openChannels).toEqual(pinned.openChannels);
    expect(collapsed.pinnedChannels).toEqual(["graph"]);
    expect(collapsed.activeChannelId).toBe("graph");
  });

  it("does not reopen the shell on hydrate when the user had collapsed it", () => {
    const hydrated = initialRightPanelState(prefs({ pinnedChannels: ["graph"], open: false }));

    expect(hydrated.open).toBe(false);
    expect(hydrated.openChannels).toEqual(["graph"]);
    expect(hydrated.activeChannelId).toBe("graph");
  });

  it("opens the shell when a file is opened from the tree or a message", () => {
    const after = openInspectorTab(state(), fileRowIn("/workspace/main.py"));

    expect(after.open).toBe(true);
    expect(after.activePanes.main).toBe(id("/workspace/main.py"));
    expect(after.openChannels).toEqual(["files", "graph", "tools", "canvas"]);
    expect(after.pinnedChannels).toEqual([]);
  });

  it("puts a row that arrived without a pane in the main pane", () => {
    // `row` is an argument, so its pane is a runtime value: the type promises
    // one, the caller is what may not deliver it. Resolving it here is what
    // keeps `{ undefined: id }` out of `activePanes` — a key no pane reads, so
    // the row would be stored and the selection silently lost.
    const paneLess = { kind: "file", path: "/workspace/a.ts", name: "a.ts" } as unknown as FileInspectorRow;
    const after = openInspectorTab(state(), paneLess);

    expect(after.activePanes).toEqual({ main: id("/workspace/a.ts"), side: null });
    expect(Object.keys(after.activePanes)).toEqual(["main", "side"]);
    expect(after.inspectorTabs).toEqual([fileRowIn("/workspace/a.ts")]);
    expect(after.focusedPane).toBe("main");
  });

  it("remembers which lane the user last activated", () => {
    const opened = openInspectorTab(state(), fileRowIn("/workspace/main.py"));
    expect(opened.activeLane).toBe("inspector");

    // Selecting a channel keeps the tab open, it only hands the strip back.
    const back = openChannel(opened, "graph");
    expect(back.activeLane).toBe("channel");
    expect(back.activePanes.main).toBe(id("/workspace/main.py"));

    expect(closeInspectorTab(back, id("/workspace/main.py")).activeLane).toBe("channel");
  });

  it("closing the selected channel does not steal the lane from an open file", () => {
    const reading = openInspectorTab(state(), fileRowIn("/workspace/main.py"));
    const closed = closeChannel(reading, "files");

    expect(closed.openChannels).toEqual(["graph", "tools", "canvas"]);
    expect(closed.activeLane).toBe("inspector");
    expect(closed.activePanes.main).toBe(id("/workspace/main.py"));
  });

  it("closes the last inspector tab without closing the shell", () => {
    const opened = openInspectorTab(state(), fileRowIn("/workspace/main.py"));
    const closed = closeInspectorTab(opened, id("/workspace/main.py"));

    expect(closed.inspectorTabs).toEqual([]);
    expect(closed.activePanes.main).toBeNull();
    expect(closed.activePanes.side).toBeNull();
    expect(closed.open).toBe(true);
    expect(closed.activeChannelId).toBe("files");
  });

  it("selects the remaining tab when the active inspector tab is closed", () => {
    const first = openInspectorTab(state(), fileRowIn("/workspace/a.py"));
    const opened = openInspectorTab(first, fileRowIn("/workspace/b.py"));
    expect(opened.activePanes.main).toBe(id("/workspace/b.py"));

    const withoutB = closeInspectorTab(opened, id("/workspace/b.py"));
    expect(withoutB.activePanes.main).toBe(id("/workspace/a.py"));
    expect(fileRows(withoutB.inspectorTabs).map((row) => row.path)).toEqual(["/workspace/a.py"]);

    expect(closeInspectorTab(opened, id("/workspace/a.py")).activePanes.main).toBe(id("/workspace/b.py"));
  });

  it("moves a reopened row to the newest position", () => {
    let current = openInspectorTab(state(), fileRowIn("/workspace/a.py"));
    current = openInspectorTab(current, fileRowIn("/workspace/b.py"));
    current = openInspectorTab(current, fileRowIn("/workspace/a.py"));

    expect(fileRows(current.inspectorTabs).map((row) => row.path)).toEqual(["/workspace/b.py", "/workspace/a.py"]);
  });

  it("falls back to the next open channel when the active one is closed", () => {
    const before = state({ openChannels: ["files", "graph", "tools"], activeChannelId: "files" });
    expect(closeChannel(before, "files").activeChannelId).toBe("graph");

    const last = state({ openChannels: ["files"], activeChannelId: "files" });
    expect(closeChannel(last, "files").activeChannelId).toBeNull();
  });
});

describe("remembered inspector rows", () => {
  const rows = (count: number) => Array.from({ length: count }, (_, index) => fileRowIn(`/workspace/f${index}.ts`));

  const withRows = (all: ReturnType<typeof rows>) => all.reduce(
    (current, row) => openInspectorTab(current, row),
    state(),
  );

  it("survives a write and a read", () => {
    const preferences = preferencesOf(withRows(rows(2)));
    const back = parsePreferences(serializePreferences(preferences), "p1");

    expect(back.inspectorTabs).toEqual(rows(2));
  });

  it("reads a payload written before the field existed", () => {
    const legacy = JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: ["files"], primaryWidth: 320, open: true,
    });
    const back = parsePreferences(legacy, "p1");

    expect(back.pinnedChannels).toEqual(["files"]);
    expect(back.primaryWidths).toEqual({ files: 320, graph: 320, tools: 320, canvas: 320 });
    expect(back.open).toBe(true);
    expect(back.inspectorTabs).toEqual([]);
    expect(back.activeInspectorId).toBeNull();
  });

  it("drops junk rows and de-duplicates by path", () => {
    const raw = JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: [], primaryWidth: 280, open: false,
      inspectorTabs: [
        { path: "/workspace/a.ts", name: "a.ts" },
        { path: "/workspace/a.ts", name: "duplicate" },
        { name: "no path" },
        null,
        { path: "/workspace/b.ts", name: "" },
      ],
    });
    const back = parsePreferences(raw, "p1");

    expect(back.inspectorTabs).toEqual([
      { kind: "file", path: "/workspace/a.ts", name: "a.ts", pane: "main" },
      { kind: "file", path: "/workspace/b.ts", name: "b.ts", pane: "main" }, // name filled from the path
    ]);
  });

  it("keeps the newest rows when a Project has opened many files", () => {
    const back = parsePreferences(
      serializePreferences(preferencesOf(withRows(rows(MAX_PERSISTED_INSPECTOR_TABS + 5)))),
      "p1",
    );

    expect(back.inspectorTabs).toHaveLength(MAX_PERSISTED_INSPECTOR_TABS);
    expect(back.inspectorTabs?.[0]).toEqual(rows(MAX_PERSISTED_INSPECTOR_TABS + 5)[5]);
    expect(back.inspectorTabs?.at(-1)).toEqual(rows(MAX_PERSISTED_INSPECTOR_TABS + 5).at(-1));
  });

  it("only restores a selection that is still one of the remembered rows", () => {
    const kept = parsePreferences(JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: [], primaryWidth: 280, open: true,
      inspectorTabs: [{ path: "/workspace/a.ts", name: "a.ts" }],
      activeInspectorId: "file:p1:/workspace/a.ts",
    }), "p1");
    expect(initialRightPanelState(kept).activePanes.main).toBe("file:p1:/workspace/a.ts");
    expect(initialRightPanelState(kept).activeLane).toBe("inspector");

    const stale = parsePreferences(JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: [], primaryWidth: 280, open: true,
      inspectorTabs: [{ path: "/workspace/a.ts", name: "a.ts" }],
      activeInspectorId: "file:p1:/workspace/gone.ts",
    }), "p1");
    // A stale selection is dropped; the row that is still there takes the focus,
    // because a strip with rows and nothing selected cannot be drawn.
    expect(initialRightPanelState(stale).activePanes.main).toBe("file:p1:/workspace/a.ts");
    expect(initialRightPanelState(stale).activeLane).toBe("inspector");
  });

  it("does not carry another Project's rows across a key", () => {
    const written = serializePreferences(preferencesOf(withRows(rows(2))));
    expect(parsePreferences(written, "p2")).toEqual(emptyPreferences("p2"));
  });
});

describe("navigator visibility", () => {
  it("survives a write and a read", () => {
    const hidden = setNavigatorHidden(state(), true);
    const back = parsePreferences(serializePreferences(preferencesOf(hidden)), "p1");

    expect(back.navigatorHidden).toBe(true);
    expect(initialRightPanelState(back).navigatorHidden).toBe(true);
  });

  it("defaults to showing the navigator, including on a payload without the field", () => {
    expect(state().navigatorHidden).toBe(false);
    const legacy = parsePreferences(JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: [], primaryWidth: 280, open: false,
    }), "p1");
    expect(legacy.navigatorHidden).toBe(false);
  });

  it("returns the same state when nothing changes", () => {
    const shown = state();
    expect(setNavigatorHidden(shown, false)).toBe(shown);
    expect(setNavigatorHidden(setNavigatorHidden(shown, true), true)).not.toBe(shown);
  });
});

describe("two panes", () => {
  const openIn = (pane: "main" | "side", path: string, from = state()) =>
    openInspectorTab(from, fileRowIn(path, pane));

  it("draws a file in the pane that was asked for", () => {
    const both = openIn("side", "/workspace/b.ts", openIn("main", "/workspace/a.ts"));

    expect(fileRows(both.inspectorTabs).map((row) => [row.path, row.pane])).toEqual([
      ["/workspace/a.ts", "main"],
      ["/workspace/b.ts", "side"],
    ]);
    expect(both.activePanes).toEqual({ main: id("/workspace/a.ts"), side: id("/workspace/b.ts") });
    expect(both.focusedPane).toBe("side");
    expect(both.activeLane).toBe("inspector");
  });

  it("keeps one row per file, and focuses the pane that already has it", () => {
    const both = openIn("side", "/workspace/b.ts", openIn("main", "/workspace/a.ts"));
    const again = openIn("main", "/workspace/b.ts", both);

    const drawn = fileRows(again.inspectorTabs);
    expect(drawn.filter((row) => row.path === "/workspace/b.ts")).toHaveLength(1);
    expect(drawn.find((row) => row.path === "/workspace/b.ts")?.pane).toBe("side");
    expect(again.activePanes.side).toBe(id("/workspace/b.ts"));
    expect(again.focusedPane).toBe("side");
  });

  it("hands the focus back to the other pane when one is emptied", () => {
    const both = openIn("side", "/workspace/b.ts", openIn("main", "/workspace/a.ts"));
    const closed = closeInspectorTab(both, id("/workspace/b.ts"));

    expect(closed.activePanes).toEqual({ main: id("/workspace/a.ts"), side: null });
    expect(closed.focusedPane).toBe("main");
    expect(closed.activeLane).toBe("inspector");
  });

  it("leaves the channel lane only when both panes are empty", () => {
    const both = openIn("side", "/workspace/b.ts", openIn("main", "/workspace/a.ts"));
    const one = closeInspectorTab(both, id("/workspace/a.ts"));
    expect(one.activeLane).toBe("inspector");
    expect(closeInspectorTab(one, id("/workspace/b.ts")).activeLane).toBe("channel");
  });

  it("reorders inside a pane only", () => {
    const rows = openIn("side", "/workspace/b.ts", openIn("main", "/workspace/a.ts"));
    const moved = reorderInspectorTabs(rows, id("/workspace/b.ts"), id("/workspace/a.ts"));

    // A cross-pane drop is not a move; nothing changes.
    expect(moved).toBe(rows);
  });

  it("survives a write and a read, panes and all", () => {
    const both = openIn("side", "/workspace/b.ts", openIn("main", "/workspace/a.ts"));
    const back = parsePreferences(serializePreferences(preferencesOf(both)), "p1");

    expect(fileRows(back.inspectorTabs).map((row) => [row.path, row.pane])).toEqual([
      ["/workspace/a.ts", "main"],
      ["/workspace/b.ts", "side"],
    ]);
    expect(back.activeInspectorIds).toEqual({ main: id("/workspace/a.ts"), side: id("/workspace/b.ts") });
  });

  it("keeps an empty main pane empty through a round trip", () => {
    // The bug this pins: `??` treated an explicit null as "missing", so the
    // focused side row was written into main's slot and read back as main's.
    const sideOnly = openIn("side", "/workspace/b.ts");
    const stored = serializePreferences(preferencesOf(sideOnly));
    const back = parsePreferences(stored, "p1");
    const hydrated = initialRightPanelState(back);

    expect(hydrated.activePanes).toEqual({ main: null, side: id("/workspace/b.ts") });
    expect(hydrated.focusedPane).toBe("side");
    expect(hydrated.activeLane).toBe("inspector");

    // And it is a fixed point: writing the hydrated state again changes nothing.
    expect(serializePreferences(preferencesOf(hydrated))).toBe(stored);
  });

  it("selects the first row when a pane has rows but no selection", () => {
    const stored = parsePreferences(JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: [], primaryWidth: 280, open: true,
      inspectorTabs: [{ path: "/workspace/a.ts", name: "a.ts", pane: "side" }],
      activeInspectorIds: { main: null, side: null },
    }), "p1");

    expect(initialRightPanelState(stored).activeLane).toBe("inspector");
  });

  it("puts rows written before panes existed in the main pane", () => {
    const legacy = parsePreferences(JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: [], primaryWidth: 280, open: true,
      inspectorTabs: [{ path: "/workspace/a.ts", name: "a.ts" }],
      activeInspectorId: id("/workspace/a.ts"),
    }), "p1");

    expect(legacy.inspectorTabs).toEqual([{ kind: "file", path: "/workspace/a.ts", name: "a.ts", pane: "main" }]);
    expect(legacy.activeInspectorIds).toEqual({ main: id("/workspace/a.ts"), side: null });
    expect(initialRightPanelState(legacy).activePanes.main).toBe(id("/workspace/a.ts"));
  });
});

describe("artifact rows in the inspector lane", () => {
  // Artifact rows travel through the same state functions as files. A second
  // path for them would be the duplication the tagged union exists to avoid,
  // and an untested path is where the next bug lives.
  it("selects an artifact row by an id that carries its session", () => {
    const opened = openInspectorTab(state(), artifactRowIn("art_1"));

    expect(opened.activePanes).toEqual({ main: "artifact:p1:s1:art_1", side: null });
    expect(opened.focusedPane).toBe("main");
    expect(opened.activeLane).toBe("inspector");
    expect(opened.open).toBe(true);
    expect(opened.inspectorTabs).toEqual([artifactRowIn("art_1")]);
  });

  it("keeps one row when the same artifact is opened again, and leaves it in its pane", () => {
    const opened = openInspectorTab(state(), artifactRowIn("art_1", "s1", "side"));
    const again = openInspectorTab(opened, artifactRowIn("art_1", "s1", "main"));

    expect(artifactRows(again.inspectorTabs)).toEqual([artifactRowIn("art_1", "s1", "side")]);
    // Asking for it in the other pane moves the focus, not the row: two copies
    // with independent state would be two surfaces, not one row.
    expect(again.activePanes).toEqual({ main: null, side: artifactId("art_1") });
    expect(again.focusedPane).toBe("side");
  });

  it("falls back to the row left in the pane when an artifact is closed", () => {
    const withFile = openInspectorTab(state(), fileRowIn("/workspace/a.ts"));
    const both = openInspectorTab(withFile, artifactRowIn("art_1"));

    const closed = closeInspectorTab(both, artifactId("art_1"));
    expect(closed.activePanes).toEqual({ main: id("/workspace/a.ts"), side: null });
    expect(closed.inspectorTabs).toEqual([fileRowIn("/workspace/a.ts")]);
    expect(closed.focusedPane).toBe("main");

    // The last row in the lane: nothing is left to select, and the strip goes
    // back to the channel lane.
    const alone = closeInspectorTab(
      openInspectorTab(state(), artifactRowIn("art_2", "s1", "side")),
      artifactId("art_2"),
    );
    expect(alone.activePanes).toEqual({ main: null, side: null });
    expect(alone.inspectorTabs).toEqual([]);
    expect(alone.activeLane).toBe("channel");
  });

  it("keeps the session through a write and a read, and caps the mixed list", () => {
    const mixed = Array.from({ length: MAX_PERSISTED_INSPECTOR_TABS + 2 }, (_, index) => (index % 2 === 0
      ? artifactRowIn(`art_${index}`, `s${index}`)
      : fileRowIn(`/workspace/f${index}.ts`)));
    const opened = mixed.reduce((current, row) => openInspectorTab(current, row), state());
    const back = parsePreferences(serializePreferences(preferencesOf(opened)), "p1");

    expect(back.inspectorTabs).toHaveLength(MAX_PERSISTED_INSPECTOR_TABS);
    const artifacts = artifactRows(back.inspectorTabs);
    // Both ids survive the trip, and the id a selection is compared against is
    // rebuilt from the pair rather than from the artifact id alone.
    expect(artifacts[0]).toEqual(artifactRowIn("art_2", "s2"));
    expect(artifacts.map((row) => row.sessionId))
      .toEqual(artifacts.map((row) => `s${row.artifactId.split("_")[1]}`));
    // The session is part of the id this state will compare against, not just a
    // field that happened to be carried along.
    expect(artifacts.map((row) => inspectorTabId("p1", row)))
      .toEqual(artifacts.map((row) => `artifact:p1:${row.sessionId}:${row.artifactId}`));
  });
});

describe("shell and lane width", () => {
  it("expands and collapses without changing anything else", () => {
    const base = state();
    const opened = expandShell(base);
    expect(opened.open).toBe(true);
    expect(collapseShell(opened).open).toBe(false);
    expect(expandShell(opened)).toBe(opened); // already open: same object, no render
    expect(collapseShell(base)).toBe(base);   // already closed
  });

  it("gives each channel its own lane width", () => {
    const withFiles = setPrimaryWidth(state(), "files", 320);
    expect(withFiles.primaryWidths.files).toBe(320);
    expect(withFiles.primaryWidths.graph).toBe(280);

    const withGraph = setPrimaryWidth(withFiles, "graph", 400);
    expect(withGraph.primaryWidths).toEqual({ files: 320, graph: 400, tools: 280, canvas: 280 });
    expect(setPrimaryWidth(withGraph, "graph", 400)).toBe(withGraph);
  });
});

describe("hydrate", () => {
  it("applies the pins and the width before the shell flag", () => {
    const hydrated = initialRightPanelState(
      prefs({ pinnedChannels: ["tools", "files"], primaryWidth: 320, open: true }),
    );

    // The width written before channels had their own becomes every channel's.
    expect(hydrated.primaryWidths).toEqual({ files: 320, graph: 320, tools: 320, canvas: 320 });
    expect(hydrated.openChannels).toEqual(["files", "tools"]);
    expect(hydrated.activeChannelId).toBe("files");
    expect(hydrated.open).toBe(true);
  });

  it("shows every channel when a project has no pins and no shell state", () => {
    const hydrated = initialRightPanelState(prefs());

    expect(hydrated.openChannels).toEqual(["files", "graph", "tools", "canvas"]);
    expect(hydrated.activeChannelId).toBe("files");
    expect(hydrated.open).toBe(false);
    expect(hydrated.activePanes).toEqual({ main: null, side: null });
  });
});

describe("preferences", () => {
  it("keys preferences by project", () => {
    expect(rightPanelPreferencesKey("p1")).toBe("ennote:right-panel:v1:p1");
    expect(rightPanelPreferencesKey("p2")).not.toBe(rightPanelPreferencesKey("p1"));
  });

  it("drops preferences that belong to another project", () => {
    const raw = serializePreferences(prefs({ projectId: "other", pinnedChannels: ["graph"] }));

    expect(parsePreferences(raw, "p1")).toEqual(emptyPreferences("p1"));
  });

  it("falls back to empty preferences on a malformed or wrong-version payload", () => {
    expect(parsePreferences(null, "p1")).toEqual(emptyPreferences("p1"));
    expect(parsePreferences("{", "p1")).toEqual(emptyPreferences("p1"));
    expect(parsePreferences(JSON.stringify({ version: 2, projectId: "p1" }), "p1"))
      .toEqual(emptyPreferences("p1"));
    expect(parsePreferences(JSON.stringify({ version: 1 }), "p1")).toEqual(emptyPreferences("p1"));
  });

  it("ignores channel names it does not know", () => {
    const raw = JSON.stringify(prefs({ pinnedChannels: ["graph", "board"] as PanelChannel[] }));

    expect(parsePreferences(raw, "p1").pinnedChannels).toEqual(["graph"]);
  });

  it("normalises the pinned order so equal states serialise equally", () => {
    const a = serializePreferences(prefs({ pinnedChannels: ["tools", "files", "tools"] }));
    const b = serializePreferences(prefs({ pinnedChannels: ["files", "tools"] }));

    expect(a).toBe(b);
    expect(JSON.parse(a).pinnedChannels).toEqual(["files", "tools"]);
  });

  it("writes back only the fields the panel restores", () => {
    const after = openChannel(pinChannel(state(), "graph"), "tools");

    expect(preferencesOf(after)).toEqual({
      version: 1,
      projectId: "p1",
      pinnedChannels: ["graph"],
      primaryWidths: { files: 280, graph: 280, tools: 280, canvas: 280 },
      open: true,
      inspectorTabs: [],
      activeInspectorIds: { main: null, side: null },
      activeInspectorId: null,
      navigatorHidden: false,
    });
  });
});

describe("canvas channel", () => {
  it("defaults the canvas width like the other channels", () => {
    expect(emptyPreferences("p1").primaryWidths).toEqual({
      files: 280, graph: 280, tools: 280, canvas: 280,
    });
    expect(initialRightPanelState(prefs()).primaryWidths).toEqual({
      files: 280, graph: 280, tools: 280, canvas: 280,
    });
  });

  it("reads a payload written before the canvas channel existed", () => {
    // A pre-canvas payload carries widths for the original three but none for
    // canvas; canvas falls back to the legacy/default width like the rest.
    const back = parsePreferences(JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: [], primaryWidth: 300, open: false,
      primaryWidths: { files: 320, graph: 340, tools: 360 },
    }), "p1");

    expect(back.primaryWidths).toEqual({ files: 320, graph: 340, tools: 360, canvas: 300 });
  });

  it("round-trips a canvas width through serialise", () => {
    const withCanvas = setPrimaryWidth(state(), "canvas", 400);
    const back = parsePreferences(serializePreferences(preferencesOf(withCanvas)), "p1");

    expect(back.primaryWidths?.canvas).toBe(400);
  });

  it("recognises canvas and lets it pass through open, close, pin and unpin", () => {
    expect(isPanelChannel("canvas")).toBe(true);
    expect(PANEL_CHANNELS).toContain("canvas");

    const opened = openChannel(state(), "canvas");
    expect(opened.openChannels).toContain("canvas");
    expect(opened.activeChannelId).toBe("canvas");

    const pinned = pinChannel(opened, "canvas");
    expect(pinned.pinnedChannels).toEqual(["canvas"]);
    const unpinned = unpinChannel(pinned, "canvas");
    expect(unpinned.pinnedChannels).toEqual([]);
    const closed = closeChannel(unpinned, "canvas");
    expect(closed.openChannels).not.toContain("canvas");
  });
});
