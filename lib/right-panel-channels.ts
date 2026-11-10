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
import { PRIMARY_DEFAULT_WIDTH } from "./right-panel-width";

/** A channel the panel keeps open beside the inspector lane. */
export type PanelChannel = "files" | "graph" | "tools";

/** Every channel, in tab-strip order. */
export const PANEL_CHANNELS: readonly PanelChannel[] = ["files", "graph", "tools"];

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
  /** The left lane's width while both lanes are drawn. Persisted. */
  primaryWidth: number;
  /** The selected inspector tab, or `null` when the lane is empty. Never persisted. */
  activeInspectorId: string | null;
}

/** What the browser keeps for one Project. */
export interface RightPanelPreferencesV1 {
  version: 1;
  projectId: string;
  pinnedChannels: PanelChannel[];
  primaryWidth: number;
  open: boolean;
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
    primaryWidth: PRIMARY_DEFAULT_WIDTH,
    open: false,
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
  if (!Array.isArray(row.pinnedChannels)) return emptyPreferences(projectId);
  const primaryWidth = typeof row.primaryWidth === "number" && Number.isFinite(row.primaryWidth)
    ? row.primaryWidth
    : PRIMARY_DEFAULT_WIDTH;
  return {
    version: 1,
    projectId,
    pinnedChannels: normalizeChannels(row.pinnedChannels.filter(isPanelChannel)),
    primaryWidth,
    open: row.open === true,
  };
}

/** Write one Project's preferences, normalised. */
export function serializePreferences(preferences: RightPanelPreferencesV1): string {
  return JSON.stringify({
    version: 1,
    projectId: preferences.projectId,
    pinnedChannels: normalizeChannels(preferences.pinnedChannels),
    primaryWidth: preferences.primaryWidth,
    open: preferences.open,
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
    activeLane: "channel",
    open: preferences.open,
    primaryWidth: preferences.primaryWidth,
    activeInspectorId: null,
  };
}

/** The preferences a state writes back. The open set is not among them. */
export function preferencesOf(state: RightPanelState): RightPanelPreferencesV1 {
  return {
    version: 1,
    projectId: state.projectId,
    pinnedChannels: normalizeChannels(state.pinnedChannels),
    primaryWidth: state.primaryWidth,
    open: state.open,
  };
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

/** Record the left lane's width. */
export function setPrimaryWidth(state: RightPanelState, width: number): RightPanelState {
  return state.primaryWidth === width ? state : { ...state, primaryWidth: width };
}

/** Open a file in the inspector lane and show the shell. */
export function openInspectorTab(state: RightPanelState, tabId: string): RightPanelState {
  return { ...state, activeInspectorId: tabId, activeLane: "inspector", open: true };
}

/**
 * Close an inspector tab. Closing the last one only empties the lane.
 * @param state - the current state.
 * @param tabId - the tab being closed.
 * @param remainingIds - the ids still open after this one, in strip order.
 */
export function closeInspectorTab(
  state: RightPanelState,
  tabId: string,
  remainingIds: readonly string[],
): RightPanelState {
  if (state.activeInspectorId !== tabId) return state;
  const remaining = remainingIds.filter((id) => id !== tabId);
  const next = remaining[remaining.length - 1] ?? null;
  // An emptied lane hands the strip back to the channel; the tabs stay closed.
  return { ...state, activeInspectorId: next, activeLane: next === null ? "channel" : "inspector" };
}
