"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { RefObject } from "react";

/**
 * Project selector dropdown state with outside-click dismissal. The selector
 * menu is rendered by the caller; this hook owns the open/close lifecycle and
 * the container ref the outside-click handler watches.
 */
export function useProjectSelector(): {
  open: boolean;
  toggle: () => void;
  close: () => void;
  rootRef: RefObject<HTMLDivElement | null>;
} {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [open]);

  const toggle = useCallback(() => setOpen((o) => !o), []);
  const close = useCallback(() => setOpen(false), []);

  return { open, toggle, close, rootRef };
}
