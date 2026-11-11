import { describe, expect, it } from "vitest";

import {
  MAX_PERSISTED_INSPECTOR_TABS,
  fileTabId,
  type PanelChannel,
  type RightPanelPreferencesV1,
  type RightPanelState,
  closeChannel,
  closeInspectorTab,
  collapseShell,
  emptyPreferences,
  initialRightPanelState,
  openChannel,
  openInspectorTab,
  parsePreferences,
  pinChannel,
  preferencesOf,
  rightPanelPreferencesKey,
  serializePreferences,
  expandShell,
  setPrimaryWidth,
  unpinChannel,
} from "@/lib/right-panel-channels";

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

    expect(after.openChannels).toEqual(["files", "graph", "tools"]);
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
    expect(unpinned.openChannels).toEqual(["files", "graph", "tools"]);
    expect(unpinned.activeChannelId).toBe("graph");
  });

  it("keeps a channel pinned when the user closes its tab", () => {
    const pinned = pinChannel(state(), "graph");
    const closed = closeChannel(pinned, "graph");

    expect(closed.openChannels).toEqual(["files", "tools"]);
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
    const after = openInspectorTab(state(), "main.py", { path: "/workspace/main.py", name: "main.py" });

    expect(after.open).toBe(true);
    expect(after.activeInspectorId).toBe("main.py");
    expect(after.openChannels).toEqual(["files", "graph", "tools"]);
    expect(after.pinnedChannels).toEqual([]);
  });

  it("remembers which lane the user last activated", () => {
    const opened = openInspectorTab(state(), id("/workspace/main.py"), { path: "/workspace/main.py", name: "main.py" });
    expect(opened.activeLane).toBe("inspector");

    // Selecting a channel keeps the tab open, it only hands the strip back.
    const back = openChannel(opened, "graph");
    expect(back.activeLane).toBe("channel");
    expect(back.activeInspectorId).toBe(id("/workspace/main.py"));

    expect(closeInspectorTab(back, id("/workspace/main.py")).activeLane).toBe("channel");
  });

  it("closing the selected channel does not steal the lane from an open file", () => {
    const reading = openInspectorTab(state(), id("/workspace/main.py"), { path: "/workspace/main.py", name: "main.py" });
    const closed = closeChannel(reading, "files");

    expect(closed.openChannels).toEqual(["graph", "tools"]);
    expect(closed.activeLane).toBe("inspector");
    expect(closed.activeInspectorId).toBe(id("/workspace/main.py"));
  });

  it("closes the last inspector tab without closing the shell", () => {
    const opened = openInspectorTab(state(), id("/workspace/main.py"), { path: "/workspace/main.py", name: "main.py" });
    const closed = closeInspectorTab(opened, id("/workspace/main.py"));

    expect(closed.inspectorTabs).toEqual([]);
    expect(closed.activeInspectorId).toBeNull();
    expect(closed.open).toBe(true);
    expect(closed.activeChannelId).toBe("files");
  });

  it("selects the remaining tab when the active inspector tab is closed", () => {
    const first = openInspectorTab(state(), id("/workspace/a.py"), { path: "/workspace/a.py", name: "a.py" });
    const opened = openInspectorTab(first, id("/workspace/b.py"), { path: "/workspace/b.py", name: "b.py" });
    expect(opened.activeInspectorId).toBe(id("/workspace/b.py"));

    const withoutB = closeInspectorTab(opened, id("/workspace/b.py"));
    expect(withoutB.activeInspectorId).toBe(id("/workspace/a.py"));
    expect(withoutB.inspectorTabs.map((row) => row.path)).toEqual(["/workspace/a.py"]);

    expect(closeInspectorTab(opened, id("/workspace/a.py")).activeInspectorId).toBe(id("/workspace/b.py"));
  });

  it("moves a reopened row to the newest position", () => {
    let current = openInspectorTab(state(), id("/workspace/a.py"), { path: "/workspace/a.py", name: "a.py" });
    current = openInspectorTab(current, id("/workspace/b.py"), { path: "/workspace/b.py", name: "b.py" });
    current = openInspectorTab(current, id("/workspace/a.py"), { path: "/workspace/a.py", name: "a.py" });

    expect(current.inspectorTabs.map((row) => row.path)).toEqual(["/workspace/b.py", "/workspace/a.py"]);
  });

  it("falls back to the next open channel when the active one is closed", () => {
    const before = state({ openChannels: ["files", "graph", "tools"], activeChannelId: "files" });
    expect(closeChannel(before, "files").activeChannelId).toBe("graph");

    const last = state({ openChannels: ["files"], activeChannelId: "files" });
    expect(closeChannel(last, "files").activeChannelId).toBeNull();
  });
});

describe("remembered inspector rows", () => {
  const rows = (count: number) => Array.from({ length: count }, (_, index) => ({
    path: `/workspace/f${index}.ts`, name: `f${index}.ts`,
  }));

  const withRows = (all: ReturnType<typeof rows>) => all.reduce(
    (current, row) => openInspectorTab(current, `file:p1:${row.path}`, row),
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
    expect(back.primaryWidth).toBe(320);
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
      { path: "/workspace/a.ts", name: "a.ts" },
      { path: "/workspace/b.ts", name: "b.ts" }, // name filled from the path
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
    expect(initialRightPanelState(kept).activeInspectorId).toBe("file:p1:/workspace/a.ts");
    expect(initialRightPanelState(kept).activeLane).toBe("inspector");

    const stale = parsePreferences(JSON.stringify({
      version: 1, projectId: "p1", pinnedChannels: [], primaryWidth: 280, open: true,
      inspectorTabs: [{ path: "/workspace/a.ts", name: "a.ts" }],
      activeInspectorId: "file:p1:/workspace/gone.ts",
    }), "p1");
    expect(initialRightPanelState(stale).activeInspectorId).toBeNull();
    expect(initialRightPanelState(stale).activeLane).toBe("channel");
  });

  it("does not carry another Project's rows across a key", () => {
    const written = serializePreferences(preferencesOf(withRows(rows(2))));
    expect(parsePreferences(written, "p2")).toEqual(emptyPreferences("p2"));
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

  it("records the lane width and ignores a repeated value", () => {
    const widened = setPrimaryWidth(state(), 320);
    expect(widened.primaryWidth).toBe(320);
    expect(setPrimaryWidth(widened, 320)).toBe(widened);
  });
});

describe("hydrate", () => {
  it("applies the pins and the width before the shell flag", () => {
    const hydrated = initialRightPanelState(
      prefs({ pinnedChannels: ["tools", "files"], primaryWidth: 320, open: true }),
    );

    expect(hydrated.primaryWidth).toBe(320);
    expect(hydrated.openChannels).toEqual(["files", "tools"]);
    expect(hydrated.activeChannelId).toBe("files");
    expect(hydrated.open).toBe(true);
  });

  it("shows every channel when a project has no pins and no shell state", () => {
    const hydrated = initialRightPanelState(prefs());

    expect(hydrated.openChannels).toEqual(["files", "graph", "tools"]);
    expect(hydrated.activeChannelId).toBe("files");
    expect(hydrated.open).toBe(false);
    expect(hydrated.activeInspectorId).toBeNull();
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
      primaryWidth: 280,
      open: true,
      inspectorTabs: [],
      activeInspectorId: null,
    });
  });
});
