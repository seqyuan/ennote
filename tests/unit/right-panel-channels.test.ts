import { describe, expect, it } from "vitest";

import {
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

/** A state as a fresh visit to a project with these pins produces it. */
const state = (over: Partial<RightPanelState> = {}): RightPanelState => ({
  ...initialRightPanelState(prefs()),
  ...over,
});

describe("right panel channel contract", () => {
  it("selects an already-open channel without touching the open set or the pins", () => {
    const before = state({ openChannels: ["files", "graph"], activeChannelId: "files" });
    const after = openChannel(before, "graph");

    expect(after.openChannels).toEqual(["files", "graph"]);
    expect(after.pinnedChannels).toEqual([]);
    expect(after.activeChannelId).toBe("graph");
    expect(after.open).toBe(true);
  });

  it("opens a channel from the add menu without pinning it", () => {
    const after = openChannel(state(), "tools");

    expect(after.openChannels).toEqual(["files", "tools"]);
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
    expect(unpinned.openChannels).toEqual(["files", "graph"]);
    expect(unpinned.activeChannelId).toBe("graph");
  });

  it("keeps a channel pinned when the user closes its tab", () => {
    const pinned = pinChannel(state(), "graph");
    const closed = closeChannel(pinned, "graph");

    expect(closed.openChannels).toEqual(["files"]);
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
    const after = openInspectorTab(state(), "main.py");

    expect(after.open).toBe(true);
    expect(after.activeInspectorId).toBe("main.py");
    expect(after.openChannels).toEqual(["files"]);
    expect(after.pinnedChannels).toEqual([]);
  });

  it("remembers which lane the user last activated", () => {
    const opened = openInspectorTab(state(), "main.py");
    expect(opened.activeLane).toBe("inspector");

    // Selecting a channel keeps the tab open, it only hands the strip back.
    const back = openChannel(opened, "graph");
    expect(back.activeLane).toBe("channel");
    expect(back.activeInspectorId).toBe("main.py");

    expect(closeInspectorTab(back, "main.py", []).activeLane).toBe("channel");
  });

  it("closes the last inspector tab without closing the shell", () => {
    const opened = openInspectorTab(state(), "main.py");
    const closed = closeInspectorTab(opened, "main.py", []);

    expect(closed.activeInspectorId).toBeNull();
    expect(closed.open).toBe(true);
    expect(closed.activeChannelId).toBe("files");
  });

  it("selects the remaining tab when the active inspector tab is closed", () => {
    const opened = openInspectorTab(openInspectorTab(state(), "a.py"), "b.py");
    expect(opened.activeInspectorId).toBe("b.py");

    expect(closeInspectorTab(opened, "b.py", ["a.py"]).activeInspectorId).toBe("a.py");
    expect(closeInspectorTab(opened, "a.py", ["b.py"]).activeInspectorId).toBe("b.py");
  });

  it("falls back to the next open channel when the active one is closed", () => {
    const before = state({ openChannels: ["files", "graph", "tools"], activeChannelId: "files" });
    expect(closeChannel(before, "files").activeChannelId).toBe("graph");

    const last = state({ openChannels: ["files"], activeChannelId: "files" });
    expect(closeChannel(last, "files").activeChannelId).toBeNull();
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

  it("opens nothing when a project has no pins and no shell state", () => {
    const hydrated = initialRightPanelState(prefs());

    expect(hydrated.openChannels).toEqual(["files"]);
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
    });
  });
});
