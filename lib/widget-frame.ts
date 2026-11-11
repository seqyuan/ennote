/**
 * The client half of the widget frame protocol.
 *
 * The other half is authored by the Worker (`ennoworker/internal/artifacts/widgetdoc.go`),
 * which owns the CSP, the receiver script and the message names. This module only
 * *consumes* that protocol: it never builds a policy, and it never gains a message
 * type the Worker does not already send. Keeping the parsers here means the frame
 * component stays layout, and the interesting decisions are testable in a node test
 * environment with no DOM.
 */

/** The only sandbox a widget frame may ever have. */
export const WIDGET_FRAME_SANDBOX = "allow-scripts";

/** Posted by the receiver script after the widget root exists. */
export const WIDGET_MESSAGE_READY = "ennote:widget:ready";

/** Posted by the receiver script whenever the content height changes. */
export const WIDGET_MESSAGE_HEIGHT = "ennote:widget:height";

/** Posted by the receiver script when a script throws, fails to load, or is blocked. */
export const WIDGET_MESSAGE_ERROR = "ennote:widget:error";

/** Posted by us, and the only message the frame accepts: CSS custom properties. */
export const WIDGET_MESSAGE_THEME = "ennote:widget:theme";

/**
 * A widget is capped in the conversation: past this it scrolls inside its own
 * frame. The frame is one message wide, not a screen, and an unbounded report from
 * model-authored content would let one message swallow the timeline.
 */
export const WIDGET_MAX_HEIGHT = 900;

/**
 * How long to wait for the first `ready`. The receiver script boots on
 * DOMContentLoaded and reports immediately, so silence here means the document
 * never parsed — a truncation or a runaway script — which is a state the reader has
 * to see rather than a blank box.
 */
export const WIDGET_READY_TIMEOUT_MS = 5000;

export type WidgetFrameErrorKind = "script" | "csp";

export interface WidgetFrameError {
  kind: WidgetFrameErrorKind;
  detail: string;
}

/**
 * Height reports are rounded and clamped; anything else is dropped, never rendered.
 */
export function parseWidgetHeight(payload: unknown): number | null {
  if (!isRecord(payload) || payload.type !== WIDGET_MESSAGE_HEIGHT) return null;
  const raw = payload.height;
  if (typeof raw !== "number" || !Number.isFinite(raw) || raw <= 0) return null;
  return Math.max(1, Math.min(Math.round(raw), WIDGET_MAX_HEIGHT));
}

/**
 * Last known height per artifact.
 *
 * Module level, not component state: collapsing and re-expanding a message unmounts
 * and remounts the frame, and a remount that starts at 0 grows into its height on
 * screen — the same visible jump the first render was designed to avoid. Not cleared
 * on unmount; the map is bounded by the number of widgets a session has shown.
 */
const widgetHeights = new Map<string, number>();

export function readCachedWidgetHeight(artifactId: string): number {
  return widgetHeights.get(artifactId) ?? 0;
}

export function rememberWidgetHeight(artifactId: string, height: number): void {
  if (Number.isFinite(height) && height > 0) widgetHeights.set(artifactId, height);
}

/** Drop a cached height, so a retry starts at nothing rather than at the old size. */
export function forgetWidgetHeight(artifactId: string): void {
  widgetHeights.delete(artifactId);
}

/** Test hook: the cache outlives components, so a test has to reset it explicitly. */
export function resetWidgetHeightCache(): void {
  widgetHeights.clear();
}

/**
 * A failed subresource arrives through the same `error` event as a throw, so the
 * receiver classifies it and this function only validates the classification. An
 * unknown kind is reported as a script failure: it still means "this did not load".
 */
export function parseWidgetError(payload: unknown): WidgetFrameError | null {
  if (!isRecord(payload) || payload.type !== WIDGET_MESSAGE_ERROR) return null;
  const kind: WidgetFrameErrorKind = payload.kind === "csp" ? "csp" : "script";
  const detail = typeof payload.detail === "string" ? payload.detail.slice(0, 200) : "";
  return { kind, detail };
}

export function isWidgetReady(payload: unknown): boolean {
  return isRecord(payload) && payload.type === WIDGET_MESSAGE_READY;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
