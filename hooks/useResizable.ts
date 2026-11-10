"use client";

import { useRef, useCallback, useEffect, useState } from "react";

interface UseResizableOptions {
  initialWidth: number;
  minWidth: number;
  maxWidth: number;
  storageKey: string;
  /** "left" = right panel (drag left to expand), "right" = left sidebar (drag right to expand) */
  direction?: "left" | "right";
  /**
   * Maps a stored width through the caller's own rules before it is applied.
   * Without it a stored value is used only when it already sits inside
   * `minWidth`..`maxWidth`; with it the caller owns the bounds, so a value
   * saved on a wider viewport can still resolve to a narrower one.
   */
  resolveStored?: (stored: number) => number;
  /** CSS variable to publish the width to; omit to publish nothing. */
  cssVariable?: string;
}

/**
 * Hook for resizable panel width with localStorage persistence.
 */
export function useResizable({
  initialWidth, minWidth, maxWidth, storageKey, direction = "right", resolveStored, cssVariable,
}: UseResizableOptions) {
  const [width, setWidth] = useState(initialWidth);
  const [isResizing, setIsResizing] = useState(false);
  const widthRef = useRef(initialWidth);

  const applyWidth = useCallback((nextWidth: number) => {
    const newWidth = Math.max(minWidth, Math.min(maxWidth, nextWidth));
    setWidth(newWidth);
    widthRef.current = newWidth;
    if (cssVariable) document.documentElement.style.setProperty(cssVariable, `${newWidth}px`);
  }, [minWidth, maxWidth, cssVariable]);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      let nextWidth = widthRef.current;
      try {
        const stored = localStorage.getItem(storageKey);
        const parsed = stored ? Number.parseInt(stored, 10) : Number.NaN;
        if (Number.isFinite(parsed)) {
          if (resolveStored) nextWidth = resolveStored(parsed);
          else if (parsed >= minWidth && parsed <= maxWidth) nextWidth = parsed;
        }
      } catch { /* ignore unavailable storage */ }
      applyWidth(nextWidth);
    });
    return () => window.cancelAnimationFrame(frame);
  }, [storageKey, minWidth, maxWidth, resolveStored, applyWidth]);

  const beginResize = useCallback(() => {
    setIsResizing(true);
  }, []);

  const resizeBy = useCallback((delta: number) => {
    const sign = direction === "left" ? -1 : 1;
    applyWidth(widthRef.current + delta * sign);
  }, [applyWidth, direction]);

  const endResize = useCallback(() => {
    setIsResizing(false);
    try { localStorage.setItem(storageKey, String(widthRef.current)); } catch { /* ignore */ }
  }, [storageKey]);

  return { beginResize, endResize, isResizing, resizeBy, width };
}
