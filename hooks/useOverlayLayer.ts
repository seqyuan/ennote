"use client";

import { useEffect, useId, useRef } from "react";
import { getOverlayStack } from "@/lib/overlay-stack";

interface OverlayLayerOptions {
  id?: string;
  /** `false` keeps the layer open on Escape (blocking prompts). */
  dismissOnEscape?: boolean;
  /** Restore focus to the element that was active when the layer opened. */
  restoreFocus?: boolean;
}

/**
 * Register a dismissible surface in the shared Escape stack.
 *
 * `onDismiss` is stored in a ref and is always the latest callback, so
 * two-stage surfaces (a model menu returning to its root pane before closing)
 * do not read a stale closure. It must stay out of the effect deps: the layer
 * is registered once per open, not once per render.
 */
export function useOverlayLayer(
  open: boolean,
  onDismiss: () => void,
  { id, dismissOnEscape = true, restoreFocus = true }: OverlayLayerOptions = {},
): void {
  const autoId = useId();
  const layerId = id ?? autoId;
  const dismissRef = useRef(onDismiss);
  const restoreFocusRef = useRef(false);

  useEffect(() => {
    dismissRef.current = onDismiss;
  });

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    restoreFocusRef.current = restoreFocus;
    const release = getOverlayStack().push({
      id: layerId,
      dismissOnEscape,
      onDismiss: () => dismissRef.current(),
    });
    return () => {
      release();
      if (!restoreFocusRef.current) return;
      if (previous?.isConnected) previous.focus();
    };
  }, [open, layerId, dismissOnEscape, restoreFocus]);
}
