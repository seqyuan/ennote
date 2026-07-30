"use client";

import { useCallback, useEffect, useRef, useState } from "react";

interface ResizeHandleProps {
  side: "left" | "right";
  onResize: (delta: number) => void;
  onResizeStart?: () => void;
  onResizeEnd?: () => void;
  ariaLabel?: string;
  value: number;
  min: number;
  max: number;
}

function resetBodyDragStyles() {
  document.body.style.cursor = "";
  document.body.style.userSelect = "";
}

export function ResizeHandle({ side, onResize, onResizeStart, onResizeEnd, ariaLabel, value, min, max }: ResizeHandleProps) {
  const handleRef = useRef<HTMLDivElement>(null);
  const isDragging = useRef(false);
  const startX = useRef(0);
  const [dragging, setDragging] = useState(false);

  const stopDragging = useCallback((pointerId?: number) => {
    if (!isDragging.current) return;
    isDragging.current = false;
    setDragging(false);

    const handle = handleRef.current;
    if (handle && pointerId !== undefined && handle.hasPointerCapture(pointerId)) {
      handle.releasePointerCapture(pointerId);
    }

    resetBodyDragStyles();
    onResizeEnd?.();
  }, [onResizeEnd]);

  const handlePointerDown = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0 || !e.isPrimary) return;
    e.preventDefault();
    isDragging.current = true;
    startX.current = e.clientX;
    setDragging(true);
    onResizeStart?.();
    e.currentTarget.setPointerCapture(e.pointerId);
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
  }, [onResizeStart]);

  const handlePointerMove = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    if (!isDragging.current) return;
    e.preventDefault();
    const delta = e.clientX - startX.current;
    startX.current = e.clientX;
    onResize(delta);
  }, [onResize]);

  const handlePointerUp = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    stopDragging(e.pointerId);
  }, [stopDragging]);

  const handlePointerCancel = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    stopDragging(e.pointerId);
  }, [stopDragging]);

  useEffect(() => {
    return () => {
      if (!isDragging.current) return;
      isDragging.current = false;
      resetBodyDragStyles();
      onResizeEnd?.();
    };
  }, [onResizeEnd]);

  const handleKeyDown = useCallback((event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    const visualDelta = event.key === "ArrowRight" ? 16 : -16;
    onResizeStart?.();
    onResize(visualDelta);
    onResizeEnd?.();
  }, [onResize, onResizeEnd, onResizeStart]);

  return (
    <div
      ref={handleRef}
      role="separator"
      aria-label={ariaLabel ?? "Resize panel"}
      aria-orientation="vertical"
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={value}
      tabIndex={0}
      className={`resize-handle${dragging ? " resize-handle-dragging" : ""}`}
      data-side={side}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={handlePointerUp}
      onPointerCancel={handlePointerCancel}
      onKeyDown={handleKeyDown}
      style={{
        alignSelf: "stretch",
        cursor: "col-resize",
        display: "flex",
        flexShrink: 0,
        justifyContent: "center",
        marginLeft: -5,
        marginRight: -5,
        position: "relative",
        touchAction: "none",
        width: 10,
        zIndex: 250,
      }}
    >
      <div className="resize-handle-line" />
    </div>
  );
}
