/**
 * The right panel's channel, pin and shell state, and the preferences that
 * survive a reload.
 *
 * Three facts are deliberately separate, because collapsing them is the
 * mistake this model exists to prevent:
 *
 * - `openChannels` is what the panel shows right now. It is transient: it is
 *   never written to storage, so a temporary look at Graphs cannot rewrite what
 *   the project restores.
 * - `pinnedChannels` is what the project restores on the next visit. Closing a
 *   channel does not unpin it, and unpinning does not close it.
 * - `open` is whether the shell is expanded. Pinning a channel never expands the
 *   shell: a user who collapsed the panel must not have it reopen on reload.
 *
 * Everything here is a pure function of its input. Storage and window are the
 * caller's business; that keeps the contract testable and the rules in one file.
 */
import { PRIMARY_DEFAULT_WIDTH, PRIMARY_MIN_WIDTH } from "./right-panel-width";
import {
  type InspectorPane,
  type InspectorTabRow,
  inspectorTabId,
  parseInspectorTabRows,
  rowMatchesId,
} from "./inspector-tabs";

// The row ids, the row cap and the pane model are owned by the row module: a
// row and the half it is drawn in are one thing, and every existing caller
// imports them from here. Re-exported rather than redefined, so there is
// exactly one definition and no type cycle to erase.
export { MAX_PERSISTED_INSPECTOR_TABS, fileTabId, pathOf } from "./inspector-tabs";
export { INSPECTOR_PANES } from "./inspector-tabs";
export type { InspectorPane } from "./inspector-tabs";

/** A channel the panel keeps open beside the inspector lane. */
export type PanelChannel = "files" | "graph" | "tools" | "canvas";

/** Every channel, in tab-strip order. */
export const PANEL_CHANNELS: readonly PanelChannel[] = ["files", "graph", "tools", "canvas"];

/** Whether a persisted value names a channel this build knows. */
export function isPanelChannel(value: unknown): value is PanelChannel {
  return typeof value === "string" && (PANEL_CHANNELS as readonly string[]).includes(value);
}

/** An inspector tab: a transient preview of one file. Owned by the caller. */
export interface InspectorTab {
  id: string;
  label: string;
  filePath?: string;
  projectId?: string;
}

/** The panel's whole state for one Project. */
export interface RightPanelState {
  /**
   * The Project this state belongs to. It travels with the state so a caller
   * can never write one Project's selection under another Project's key: a
   * project switch produces a new state, and until it lands the old state still
   * names the old Project.
   */
  projectId: string;
  /** Channels shown right now. Never persisted. */
  openChannels: PanelChannel[];
  /** Channels restored on the next visit to this Project. Persisted. */
  pinnedChannels: PanelChannel[];
  /** The selected channel; `null` means the launcher is showing. */
  activeChannelId: PanelChannel | null;
  /**
   * Which lane the user last activated. Both lanes stay mounted, so the panel
   * needs this to know which one the tab strip should highlight: selecting a
   * channel must not discard the inspector, and selecting a tab must not
   * discard the channel.
   */
  activeLane: "channel" | "inspector";
  /** Whether the shell is expanded. Persisted. */
  open: boolean;
  /**
   * The left lane's width per channel. Persisted.
   *
   * A tree wants room for deep paths; a run inspector wants room for a table.
   * One width for both means every channel switch re-drags it, so each channel
   * remembers its own.
   */
  primaryWidths: Record<PanelChannel, number>;
  /** The selected row in each pane, or `null` when that pane is empty. */
  activePanes: Record<InspectorPane, string | null>;
  /** The pane the reader last touched; `activeInspectorId` follows it. */
  focusedPane: InspectorPane;
  /**
   * The files this Project has open, oldest first. They belong to the Project,
   * not to the session, so they are persisted with the rest of its preferences —
   * that is what makes a reload give the reader their files back.
   */
  inspectorTabs: PersistedInspectorTab[];
  /**
   * The navigator lane is hidden. It means one thing — "I do not want the tree
   * right now" — and each layout presents that differently: side by side with
   * the preview, slid over it, or not at all.
   */
  navigatorHidden: boolean;
}

/**
 * A row the panel had open, as it is remembered between visits.
 *
 * It is the same tagged union the reader parses, so a caller cannot write a row
 * this build would refuse to read back. The name is kept from the pre-artifact
 * model because every caller spells it that way.
 */
export type PersistedInspectorTab = InspectorTabRow;

/** What the browser keeps for one Project. */
export interface RightPanelPreferencesV1 {
  version: 1;
  projectId: string;
  pinnedChannels: PanelChannel[];
  /** The lane width per channel; `primaryWidth` is the pre-per-channel field. */
  primaryWidths?: Partial<Record<PanelChannel, number>>;
  /** The single lane width written before channels had their own. */
  primaryWidth?: number;
  open: boolean;
  /**
   * Files that were open, oldest first. Optional because rows written before
   * this field existed carry no such array; an absent one means "none".
   */
  inspectorTabs?: PersistedInspectorTab[];
  /** The row that was selected, when it is still among `inspectorTabs`. */
  activeInspectorId?: string | null;
  /** The selected row per pane; `activeInspectorId` is the pre-pane field. */
  activeInspectorIds?: Partial<Record<InspectorPane, string | null>>;
  /** The reader chose to hide the navigator; it stays hidden on the next visit. */
  navigatorHidden?: boolean;
}

/**
 * The storage key for one Project's preferences.
 * @param projectId - the Project the preferences belong to.
 */
export function rightPanelPreferencesKey(projectId: string): string {
  return `ennote:right-panel:v1:${projectId}`;
}

/** Preferences for a Project that has never stored any. */
export function emptyPreferences(projectId: string): RightPanelPreferencesV1 {
  return {
    version: 1,
    projectId,
    pinnedChannels: [],
    primaryWidths: { files: PRIMARY_DEFAULT_WIDTH, graph: PRIMARY_DEFAULT_WIDTH, tools: PRIMARY_DEFAULT_WIDTH, canvas: PRIMARY_DEFAULT_WIDTH },
    open: false,
    inspectorTabs: [],
    activeInspectorId: null,
    navigatorHidden: false,
  };
}

/** Dedupe and order the pins so equal states serialise to equal strings. */
function normalizeChannels(channels: readonly PanelChannel[]): PanelChannel[] {
  return PANEL_CHANNELS.filter((channel) => channels.includes(channel));
}

/**
 * Read one Project's preferences.
 *
 * A row must prove it belongs to the Project being opened: a leftover row from
 * another Project is not a preference, it is a stale cache entry, so it is
 * dropped rather than repaired. The same holds for a payload this build cannot
 * read — an unknown version is not migrated, it is ignored.
 * @param raw - the stored string, or `null` when nothing was stored.
 * @param projectId - the Project being opened.
 */
export function parsePreferences(raw: string | null, projectId: string): RightPanelPreferencesV1 {
  if (raw === null) return emptyPreferences(projectId);
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return emptyPreferences(projectId);
  }
  if (typeof parsed !== "object" || parsed === null) return emptyPreferences(projectId);
  const row = parsed as Partial<RightPanelPreferencesV1>;
  if (row.version !== 1) return emptyPreferences(projectId);
  if (row.projectId !== projectId) return emptyPreferences(projectId);
  // A malformed field costs that field, not the row: discarding everything here
  // would let one bad write take the Project's pins, width and shell with it.
  const pinnedChannels = Array.isArray(row.pinnedChannels)
    ? normalizeChannels(row.pinnedChannels.filter(isPanelChannel))
    : [];
  // The single width this Project used to carry becomes every channel's starting
  // point; a channel absent from `primaryWidths` falls back to it.
  const legacyWidth = typeof row.primaryWidth === "number" && Number.isFinite(row.primaryWidth)
    ? row.primaryWidth
    : PRIMARY_DEFAULT_WIDTH;
  const storedWidths = (typeof row.primaryWidths === "object" && row.primaryWidths !== null)
    ? row.primaryWidths
    : {};
  const primaryWidths = {
    files: widthOf(storedWidths.files, legacyWidth),
    graph: widthOf(storedWidths.graph, legacyWidth),
    tools: widthOf(storedWidths.tools, legacyWidth),
    // A payload written before the canvas channel existed has no canvas width;
    // it falls back to the same legacy/default width as the other three.
    canvas: widthOf(storedWidths.canvas, legacyWidth),
  };
  const inspectorTabs = parseInspectorTabRows(row.inspectorTabs, projectId);
  const legacyActive = typeof row.activeInspectorId === "string" ? row.activeInspectorId : null;
  const storedActive = (typeof row.activeInspectorIds === "object" && row.activeInspectorIds !== null)
    ? row.activeInspectorIds
    : {};
  const activeIn = (pane: InspectorPane, value: unknown): string | null => {
    const candidate = typeof value === "string" ? value : pane === "main" ? legacyActive : null;
    if (candidate === null) return null;
    const owned = inspectorTabs.some((tab) => tab.pane === pane
      && rowMatchesId(projectId, tab, candidate));
    return owned ? candidate : null;
  };
  const activePanes = {
    main: activeIn("main", storedActive.main),
    side: activeIn("side", storedActive.side),
  };
  const activeInspectorId = activePanes.main ?? activePanes.side;
  return {
    version: 1,
    projectId,
    pinnedChannels,
    primaryWidths,
    open: row.open === true,
    inspectorTabs,
    activeInspectorId,
    activeInspectorIds: activePanes,
    navigatorHidden: row.navigatorHidden === true,
  };
}

/** Write one Project's preferences, normalised. */
export function serializePreferences(preferences: RightPanelPreferencesV1): string {
  return JSON.stringify({
    version: 1,
    projectId: preferences.projectId,
    pinnedChannels: normalizeChannels(preferences.pinnedChannels),
    primaryWidths: {
      files: widthOf(preferences.primaryWidths?.files, PRIMARY_DEFAULT_WIDTH),
      graph: widthOf(preferences.primaryWidths?.graph, PRIMARY_DEFAULT_WIDTH),
      tools: widthOf(preferences.primaryWidths?.tools, PRIMARY_DEFAULT_WIDTH),
      canvas: widthOf(preferences.primaryWidths?.canvas, PRIMARY_DEFAULT_WIDTH),
    },
    open: preferences.open,
    inspectorTabs: parseInspectorTabRows(preferences.inspectorTabs, preferences.projectId),
    navigatorHidden: preferences.navigatorHidden === true,
    // Explicit nulls stay nulls: `??` would substitute the focused row — which
    // may live in the side pane — into main's slot, and the next read would
    // believe main had a selection it never had.
    activeInspectorIds: {
      main: preferences.activeInspectorIds
        ? preferences.activeInspectorIds.main ?? null
        : preferences.activeInspectorId ?? null,
      side: preferences.activeInspectorIds ? preferences.activeInspectorIds.side ?? null : null,
    },
    activeInspectorId: preferences.activeInspectorIds
      ? preferences.activeInspectorIds.main ?? null
      : preferences.activeInspectorId ?? null,
  });
}

/**
 * The channels a visit starts with: the pins when there are any, otherwise all
 * of them.
 *
 * A fresh Project has no preference, so it shows the panel as the workspace
 * always has — every channel in the strip. A pin is a deliberate narrow
 * ("restore just this one"), so once the user pins, the pins are the whole
 * answer rather than an addition to a default.
 * @param pinned - the Project's pinned channels.
 */
export function seedOpenChannels(pinned: readonly PanelChannel[]): PanelChannel[] {
  return pinned.length > 0 ? normalizeChannels(pinned) : [...PANEL_CHANNELS];
}

/**
 * The state a visit to a Project starts from.
 *
 * The pins seed the open set, and the first channel is selected. The
 * persisted shell flag is applied here too — not as a last step in the caller —
 * because a caller that applied it after selecting a channel would expand the
 * shell the user had collapsed.
 * @param preferences - the Project's stored preferences.
 */
export function initialRightPanelState(preferences: RightPanelPreferencesV1): RightPanelState {
  const pinnedChannels = normalizeChannels(preferences.pinnedChannels);
  const openChannels = seedOpenChannels(pinnedChannels);
  return {
    projectId: preferences.projectId,
    openChannels,
    pinnedChannels,
    activeChannelId: openChannels[0] ?? null,
    open: preferences.open,
    primaryWidths: {
      // A Project that predates per-channel widths has one number for all three;
      // it is their starting point until each channel is dragged on its own.
      files: widthOf(preferences.primaryWidths?.files, widthOf(preferences.primaryWidth, PRIMARY_DEFAULT_WIDTH)),
      graph: widthOf(preferences.primaryWidths?.graph, widthOf(preferences.primaryWidth, PRIMARY_DEFAULT_WIDTH)),
      tools: widthOf(preferences.primaryWidths?.tools, widthOf(preferences.primaryWidth, PRIMARY_DEFAULT_WIDTH)),
      canvas: widthOf(preferences.primaryWidths?.canvas, widthOf(preferences.primaryWidth, PRIMARY_DEFAULT_WIDTH)),
    },
    navigatorHidden: preferences.navigatorHidden === true,
    ...seedPanes(preferences),
  };
}

/**
 * The selections a Project starts with, one per pane.
 *
 * An explicit null stays null: a Project whose files all live in the side pane
 * must not come back with one of them selected in main. When rows exist but no
 * selection survived (a row closed in another tab, a payload older than panes),
 * the first row takes the focus — a strip with rows and no selection is a state
 * nothing can draw.
 */
function seedPanes(preferences: RightPanelPreferencesV1): Pick<RightPanelState,
  "inspectorTabs" | "activePanes" | "focusedPane" | "activeLane"> {
  const inspectorTabs = parseInspectorTabRows(preferences.inspectorTabs, preferences.projectId);
  const stored = preferences.activeInspectorIds;
  const owned = (pane: InspectorPane, value: string | null): string | null =>
    value !== null && inspectorTabs.some((row) => row.pane === pane
      && rowMatchesId(preferences.projectId, row, value))
      ? value
      : null;

  const activePanes: Record<InspectorPane, string | null> = {
    main: owned("main", stored ? stored.main ?? null : preferences.activeInspectorId ?? null),
    side: owned("side", stored ? stored.side ?? null : null),
  };

  if (activePanes.main === null && activePanes.side === null && inspectorTabs.length > 0) {
    const first = inspectorTabs[0];
    activePanes[first.pane] = inspectorTabId(preferences.projectId, first);
  }

  const focusedPane: InspectorPane = activePanes.side !== null && activePanes.main === null ? "side" : "main";
  return {
    inspectorTabs,
    activePanes,
    focusedPane,
    activeLane: activePanes[focusedPane] !== null ? "inspector" : "channel",
  };
}

/**
 * The preferences a state writes back.
 *
 * The open *channel* set is not among them; the open *file* rows are, because
 * they are the Project's history of what the reader was looking at, and that is
 * exactly what a reload should give back.
 * @param state - the panel's state.
 */
export function preferencesOf(state: RightPanelState): RightPanelPreferencesV1 {
  return {
    version: 1,
    projectId: state.projectId,
    pinnedChannels: normalizeChannels(state.pinnedChannels),
    primaryWidths: {
      files: widthOf(state.primaryWidths.files, PRIMARY_DEFAULT_WIDTH),
      graph: widthOf(state.primaryWidths.graph, PRIMARY_DEFAULT_WIDTH),
      tools: widthOf(state.primaryWidths.tools, PRIMARY_DEFAULT_WIDTH),
      canvas: widthOf(state.primaryWidths.canvas, PRIMARY_DEFAULT_WIDTH),
    },
    open: state.open,
    inspectorTabs: parseInspectorTabRows(state.inspectorTabs, state.projectId),
    activeInspectorIds: state.activePanes,
    activeInspectorId: state.activePanes[state.focusedPane],
    navigatorHidden: state.navigatorHidden,
  };
}

/** Show or hide the navigator lane. */
export function setNavigatorHidden(state: RightPanelState, hidden: boolean): RightPanelState {
  return state.navigatorHidden === hidden ? state : { ...state, navigatorHidden: hidden };
}

/**
 * Reorder one pane's rows, so the strip's order survives a reload too.
 *
 * Rows are named by id rather than by path: a row can also be an artifact now,
 * and "the row with this path" is not a question that answers for both kinds.
 */
export function reorderInspectorTabs(
  state: RightPanelState,
  fromId: string,
  toId: string,
): RightPanelState {
  const from = state.inspectorTabs.findIndex((row) => rowMatchesId(state.projectId, row, fromId));
  const to = state.inspectorTabs.findIndex((row) => rowMatchesId(state.projectId, row, toId));
  if (from < 0 || to < 0 || from === to) return state;
  // Rows only reorder within their own pane; a cross-pane drop is not a move, it
  // is a different action this build does not offer.
  if (state.inspectorTabs[from].pane !== state.inspectorTabs[to].pane) return state;
  const rows = [...state.inspectorTabs];
  const [moved] = rows.splice(from, 1);
  rows.splice(to, 0, moved);
  return { ...state, inspectorTabs: rows };
}

/** Show a channel: used by a channel tab, the add menu, and the launcher alike. */
export function openChannel(state: RightPanelState, channel: PanelChannel): RightPanelState {
  const openChannels = state.openChannels.includes(channel)
    ? state.openChannels
    : [...state.openChannels, channel];
  return { ...state, openChannels, activeChannelId: channel, activeLane: "channel", open: true };
}

/**
 * Hide a channel without unpinning it, falling through to the next open channel.
 * With nothing left open the launcher shows; the shell stays expanded.
 */
export function closeChannel(state: RightPanelState, channel: PanelChannel): RightPanelState {
  if (!state.openChannels.includes(channel)) return state;
  const openChannels = state.openChannels.filter((entry) => entry !== channel);
  if (state.activeChannelId !== channel) return { ...state, openChannels };
  const remaining = PANEL_CHANNELS.filter((entry) => openChannels.includes(entry));
  return {
    ...state,
    openChannels,
    activeChannelId: remaining[0] ?? null,
    // Hiding the channel is all the press asked for; the lane the reader was in
    // stays theirs when it still has something to show.
    activeLane: state.activeLane,
  };
}

/** Restore this channel on the next visit, and show it now. */
export function pinChannel(state: RightPanelState, channel: PanelChannel): RightPanelState {
  const pinnedChannels = state.pinnedChannels.includes(channel)
    ? state.pinnedChannels
    : normalizeChannels([...state.pinnedChannels, channel]);
  return openChannel({ ...state, pinnedChannels }, channel);
}

/** Stop restoring this channel. It stays open until it is closed. */
export function unpinChannel(state: RightPanelState, channel: PanelChannel): RightPanelState {
  if (!state.pinnedChannels.includes(channel)) return state;
  return { ...state, pinnedChannels: state.pinnedChannels.filter((entry) => entry !== channel) };
}

/** Expand the shell without touching which channels are open or pinned. */
export function expandShell(state: RightPanelState): RightPanelState {
  return state.open ? state : { ...state, open: true };
}

/** Collapse the shell without touching which channels are open or pinned. */
export function collapseShell(state: RightPanelState): RightPanelState {
  return state.open ? { ...state, open: false } : state;
}

/** Record one channel's lane width. */
export function setPrimaryWidth(
  state: RightPanelState,
  channel: PanelChannel,
  width: number,
): RightPanelState {
  if (state.primaryWidths[channel] === width) return state;
  return { ...state, primaryWidths: { ...state.primaryWidths, [channel]: width } };
}

/**
 * A stored width, when it is one.
 *
 * Clamped to a drawable range on the way in: a stored 0 or a negative number
 * would otherwise be written back forever, and the lane would only look right
 * because the resolver clamps it at render time.
 */
function widthOf(value: unknown, fallback: number): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return fallback;
  return Math.max(PRIMARY_MIN_WIDTH, Math.min(2000, Math.round(value)));
}

/**
 * Open a row in one pane of the inspector lane and show the shell.
 *
 * A row is open at most once: asking for it again — in either pane — moves the
 * focus to the pane that already holds it rather than drawing it twice. Two
 * copies of the same row with independent scroll positions would be a bug
 * wearing a feature's clothes.
 * @param state - the current state.
 * @param row - the row to remember; `row.pane` says which pane it belongs to.
 */
export function openInspectorTab(
  state: RightPanelState,
  row: PersistedInspectorTab,
): RightPanelState {
  // Identity is the row's id, not its path: an artifact row has no path, and the
  // id is what the selection in `activePanes` will refer to anyway. There is no
  // second id parameter for the same reason: two sources for one fact only
  // disagree, and the disagreement is a selection pointing at nothing.
  const rowId = inspectorTabId(state.projectId, row);
  const same = (tab: PersistedInspectorTab) => inspectorTabId(state.projectId, tab) === rowId;
  const existing = state.inspectorTabs.find(same);
  // A row is an argument, and an argument is a runtime value: the type says
  // every row has a pane, the caller is what does not. Resolving it here — the
  // way the reader resolves a stored row — keeps `activePanes` from gaining an
  // `undefined` key, which would silently lose the selection.
  const requested: InspectorPane = row.pane === "side" ? "side" : "main";
  const pane: InspectorPane = existing?.pane ?? requested;
  const inspectorTabs = parseInspectorTabRows([
    ...state.inspectorTabs.filter((tab) => !same(tab)),
    { ...row, pane },
  ], state.projectId);
  // The cap drops the oldest row, which may be the one a pane has selected. A
  // selection that points at a row nobody draws is worse than no selection.
  const alive = new Set(inspectorTabs.map((tab) => inspectorTabId(state.projectId, tab)));
  const activePanes = {
    main: state.activePanes.main !== null && alive.has(state.activePanes.main) ? state.activePanes.main : null,
    side: state.activePanes.side !== null && alive.has(state.activePanes.side) ? state.activePanes.side : null,
  };
  return {
    ...state,
    inspectorTabs,
    activePanes: { ...activePanes, [pane]: rowId },
    focusedPane: pane,
    activeLane: "inspector",
    open: true,
  };
}

/**
 * Close an inspector tab. Closing the last one only empties the lane.
 *
 * The rows that remain are read from the state, not from the caller: a caller
 * that passed a list one render stale could otherwise leave the selection
 * pointing at a file the Project no longer has open.
 * @param state - the current state.
 * @param tabId - the tab being closed.
 */
export function closeInspectorTab(state: RightPanelState, tabId: string): RightPanelState {
  const closing = state.inspectorTabs.find((row) => rowMatchesId(state.projectId, row, tabId));
  const inspectorTabs = state.inspectorTabs.filter((row) => !rowMatchesId(state.projectId, row, tabId));
  const pane: InspectorPane = closing?.pane ?? "main";

  // What is left in the pane that lost the row, and in the other one.
  const remainingIn = (which: InspectorPane) => inspectorTabs
    .filter((row) => row.pane === which)
    .map((row) => inspectorTabId(state.projectId, row));
  const remaining = remainingIn(pane);

  const activePanes = { ...state.activePanes };
  if (activePanes[pane] === tabId) {
    activePanes[pane] = remaining[remaining.length - 1] ?? null;
  }
  const elsewhere: InspectorPane = pane === "main" ? "side" : "main";
  const focusedPane = activePanes[pane] === null && activePanes[elsewhere] !== null ? elsewhere : state.focusedPane;
  const anyLeft = inspectorTabs.length > 0;

  return {
    ...state,
    inspectorTabs,
    activePanes,
    focusedPane,
    // An emptied lane hands the strip back to the channel; the tabs stay closed.
    activeLane: anyLeft ? state.activeLane : "channel",
  };
}
