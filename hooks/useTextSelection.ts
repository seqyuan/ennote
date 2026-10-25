"use client";

import { useEffect, useRef, useState, type RefObject } from "react";

export interface TextSelection {
  text: string;
  rect: { top: number; bottom: number; left: number; width: number };
}

interface Options {
  enabled?: boolean;
  /** Minimum viewport width; touch layouts use the native selection menu. */
  minWidth?: number;
}

/**
 * Track a non-collapsed text selection inside one scroll container.
 *
 * `selectionchange` alone is enough to show the toolbar (Playwright's
 * `locator.selectText()` fires it without pointer events); `pointerup` only
 * refreshes the rectangle after a drag. The selection is hidden on scroll and
 * resize because the fixed toolbar would otherwise point at stale text.
 */
export function useTextSelection(
  containerRef: RefObject<HTMLElement | null>,
  { enabled = true, minWidth = 640 }: Options = {},
): { selection: TextSelection | null; clear: () => void } {
  const [selection, setSelection] = useState<TextSelection | null>(null);
  const timer = useRef<number | null>(null);

  useEffect(() => {
    if (!enabled) return;
    const container = containerRef.current;
    if (!container) return;

    const clear = () => setSelection(null);

    const measure = () => {
      if (typeof window === "undefined" || window.innerWidth < minWidth) {
        setSelection(null);
        return;
      }
      const current = window.getSelection();
      if (!current || current.isCollapsed || current.rangeCount === 0) {
        setSelection(null);
        return;
      }
      const range = current.getRangeAt(0);
      const ancestor = range.commonAncestorContainer;
      const element = ancestor instanceof Element ? ancestor : ancestor.parentElement;
      if (!element || !container.contains(element)) {
        setSelection(null);
        return;
      }
      if (element.closest("input, textarea, [contenteditable='true']")) {
        setSelection(null);
        return;
      }
      const text = current.toString();
      if (text.trim().length === 0) {
        setSelection(null);
        return;
      }
      const rect = range.getBoundingClientRect();
      if (rect.width === 0 && rect.height === 0) {
        setSelection(null);
        return;
      }
      setSelection({
        text,
        rect: { top: rect.top, bottom: rect.bottom, left: rect.left, width: rect.width },
      });
    };

    const schedule = () => {
      if (timer.current !== null) window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => {
        timer.current = null;
        measure();
      }, 120);
    };

    document.addEventListener("selectionchange", schedule);
    document.addEventListener("pointerup", schedule);
    window.addEventListener("resize", clear);
    container.addEventListener("scroll", clear, { passive: true });

    return () => {
      document.removeEventListener("selectionchange", schedule);
      document.removeEventListener("pointerup", schedule);
      window.removeEventListener("resize", clear);
      container.removeEventListener("scroll", clear);
      if (timer.current !== null) window.clearTimeout(timer.current);
      timer.current = null;
    };
  }, [containerRef, enabled, minWidth]);

  return { selection: enabled ? selection : null, clear: () => setSelection(null) };
}
