"use client";

import { useCallback, useEffect, useState } from "react";

import { RIGHT_DEFAULT_WIDTH, RIGHT_MIN_WIDTH, resolveRightPanelWidth, rightPanelMaxWidth } from "@/lib/right-panel-width";
import { useResizable } from "./useResizable";

/**
 * The right panel's width: the resizable state plus the live ceiling that keeps
 * the conversation readable.
 *
 * The ceiling is re-derived whenever the sidebar's width changes, including
 * while it is being dragged: the panel keeps its stored width but cannot keep a
 * width the sidebar just took away.
 *
 * The panel's stored width is not enough to size it. That number was written on
 * some earlier viewport with some earlier sidebar, and both can have changed
 * since, so every value read from storage is re-derived from the current room.
 * A window shrink therefore pulls the panel in instead of squeezing the
 * conversation.
 *
 * @param sidebarWidth - the sidebar's applied width; `0` while it is collapsed.
 */
export function useRightPanelWidth(width: number) {
  const [viewport, setViewport] = useState(() =>
    typeof window === "undefined" ? 1440 : window.innerWidth,
  );

  useEffect(() => {
    const onResize = () => setViewport(window.innerWidth);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const maxWidth = rightPanelMaxWidth(viewport, width);

  // Memoized because `useResizable` lists it in its hydrate effect: a fresh
  // closure on every render would re-run that effect on every render.
  const resolveStored = useCallback(
    (stored: number) => resolveRightPanelWidth(stored, viewport, width),
    [viewport, width],
  );

  const resize = useResizable({
    initialWidth: RIGHT_DEFAULT_WIDTH,
    minWidth: RIGHT_MIN_WIDTH,
    maxWidth,
    storageKey: "ennote-right-panel-width",
    direction: "left",
    resolveStored,
  });

  return { ...resize, maxWidth };
}
