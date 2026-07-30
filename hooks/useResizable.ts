"use client";

import { useRef, useCallback, useEffect, useState } from "react";

interface UseResizableOptions {
  initialWidth: number;
  minWidth: number;
  maxWidth: number;
  storageKey: string;
  /** "left" = right panel (drag left to expand), "right" = left sidebar (drag right to expand) */
  direction?: "left" | "right";
}

/**
 * Hook for resizable panel width with localStorage persistence.
 */
export function useResizable({ initialWidth, minWidth, maxWidth, storageKey, direction = "right" }: UseResizableOptions) {
  const [width, setWidth] = useState(initialWidth);
  const [isResizing, setIsResizing] = useState(false);
  const widthRef = useRef(initialWidth);

  const applyWidth = useCallback((nextWidth: number) => {
    const newWidth = Math.max(minWidth, Math.min(maxWidth, nextWidth));
    setWidth(newWidth);
    widthRef.current = newWidth;
    document.documentElement.style.setProperty(storageKey, `${newWidth}px`);
  }, [minWidth, maxWidth, storageKey]);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      let nextWidth = widthRef.current;
      try {
        const stored = localStorage.getItem(storageKey);
        const parsed = stored ? Number.parseInt(stored, 10) : Number.NaN;
        if (Number.isFinite(parsed) && parsed >= minWidth && parsed <= maxWidth) nextWidth = parsed;
      } catch { /* ignore unavailable storage */ }
      applyWidth(nextWidth);
    });
    return () => window.cancelAnimationFrame(frame);
  }, [storageKey, minWidth, maxWidth, applyWidth]);

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
