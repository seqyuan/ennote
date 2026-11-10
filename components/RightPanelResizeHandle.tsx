"use client";

import { ResizeHandle } from "./ResizeHandle";
import { RIGHT_MIN_WIDTH } from "@/lib/right-panel-width";
import type { useRightPanelWidth } from "@/hooks/useRightPanelWidth";

/**
 * The drag handle between the conversation and the right panel.
 *
 * It lives beside the panel rather than inside it because the handle belongs to
 * the row, not to the panel: while the panel is closed the conversation uses the
 * whole width and there is nothing to drag. `max` is the live ceiling, so the
 * handle reports the same bound the width resolver enforces.
 */
export function RightPanelResizeHandle({ resize }: {
  resize: ReturnType<typeof useRightPanelWidth>;
}) {
  return (
    <ResizeHandle
      side="left"
      ariaLabel="Resize right panel"
      value={resize.width}
      min={RIGHT_MIN_WIDTH}
      max={resize.maxWidth}
      onResizeStart={resize.beginResize}
      onResize={resize.resizeBy}
      onResizeEnd={resize.endResize}
    />
  );
}
