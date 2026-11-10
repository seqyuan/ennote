/**
 * Right-panel geometry rules.
 *
 * The panel shares one row with the conversation, so its width is not a free
 * preference: every pixel it takes is a pixel the conversation loses. These
 * rules keep the conversation readable and decide which lane layout fits the
 * panel at its current width.
 *
 * A stored width is never trusted on its own. It was written on some earlier
 * viewport with some earlier sidebar, and the window may have shrunk since, so
 * every value read from storage goes through `resolveRightPanelWidth`.
 */

/** The narrowest conversation column we accept while the panel is open. */
export const CHAT_MIN_WIDTH = 440;

/** The panel at its narrowest: one lane, no split. */
export const RIGHT_MIN_WIDTH = 280;

/** The panel at its widest; above this the conversation loses more than it gains. */
export const RIGHT_MAX_WIDTH = 1000;

/** The drag handle between the conversation and the panel. */
export const RESIZE_HANDLE_WIDTH = 5;

/** A fresh profile opens at this width, which holds two lanes on a 1440 viewport. */
export const RIGHT_DEFAULT_WIDTH = 680;

/** The left lane never goes below this, so the tree keeps its indent levels legible. */
export const PRIMARY_MIN_WIDTH = 220;

/** The right lane never goes below this, so a file preview keeps a usable measure. */
export const INSPECTOR_MIN_WIDTH = 264;

/** The draggable divider between the two lanes. */
export const LANE_DIVIDER_WIDTH = 8;

/** Below this the two lanes cannot both be useful, so the panel shows one. */
export const DUAL_LANE_MIN_WIDTH = 520;

/** Whether the panel's two lanes can both be drawn at this width. */
export type LaneMode = "wide" | "medium" | "narrow";

/** The widest the panel may be without starving the conversation. */
export function rightPanelMaxWidth(viewport: number, sidebarWidth: number): number {
  const room = viewport - sidebarWidth - CHAT_MIN_WIDTH - RESIZE_HANDLE_WIDTH;
  return Math.max(RIGHT_MIN_WIDTH, Math.min(RIGHT_MAX_WIDTH, room));
}

/**
 * The width to apply for a stored value on the current viewport.
 * @param stored - the persisted width, or `null` when nothing was stored.
 * @param viewport - the window's inner width.
 * @param sidebarWidth - the sidebar's applied width, `0` while it is collapsed.
 */
export function resolveRightPanelWidth(
  stored: number | null,
  viewport: number,
  sidebarWidth: number,
): number {
  const max = rightPanelMaxWidth(viewport, sidebarWidth);
  if (stored === null || !Number.isFinite(stored)) return Math.min(RIGHT_DEFAULT_WIDTH, max);
  return Math.max(RIGHT_MIN_WIDTH, Math.min(max, stored));
}

/**
 * The left lane's width, kept from starving the right lane.
 * @param stored - the persisted lane width, or `null` for the default.
 * @param shellWidth - the panel's full width, both lanes and the divider.
 */
export function resolvePrimaryLaneWidth(stored: number | null, shellWidth: number): number {
  const max = Math.max(PRIMARY_MIN_WIDTH, shellWidth - LANE_DIVIDER_WIDTH - INSPECTOR_MIN_WIDTH);
  const wanted = stored !== null && Number.isFinite(stored) ? stored : 280;
  return Math.max(PRIMARY_MIN_WIDTH, Math.min(max, wanted));
}

/**
 * Which layout the panel shows at a width.
 *
 * `narrow` is the pre-lanes single tab strip: it is also what the drawer gets
 * below 960px, where the panel fills the viewport and has no conversation
 * beside it to protect.
 * @param shellWidth - the panel's full width.
 * @param drawer - whether the panel is currently drawn as a full-screen drawer.
 */
export function laneMode(shellWidth: number, drawer: boolean): LaneMode {
  if (drawer) return "narrow";
  if (shellWidth >= DUAL_LANE_MIN_WIDTH) return "wide";
  if (shellWidth >= RIGHT_MIN_WIDTH) return "medium";
  return "narrow";
}
