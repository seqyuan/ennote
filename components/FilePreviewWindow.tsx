"use client";

import { Maximize2, Minimize2, X } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { FileViewer } from "./FileViewer";

interface Props {
  projectId: string;
  filePath: string;
  fileName: string;
  onClose: () => void;
}

interface Bounds {
  x: number;
  y: number;
  width: number;
  height: number;
  maximized: boolean;
}

const MIN_WIDTH = 360;
const MIN_HEIGHT = 260;

export function FilePreviewWindow({ projectId, filePath, fileName, onClose }: Props) {
  const [bounds, setBounds] = useState<Bounds>(() => initialBounds());
  const restoredBounds = useRef<Bounds | null>(null);
  const interaction = useRef<{ kind: "move" | "resize"; x: number; y: number; bounds: Bounds } | null>(null);

  const beginInteraction = useCallback((kind: "move" | "resize", event: React.PointerEvent<HTMLElement>) => {
    if (event.button !== 0 || bounds.maximized) return;
    event.preventDefault();
    interaction.current = { kind, x: event.clientX, y: event.clientY, bounds };
    event.currentTarget.setPointerCapture(event.pointerId);
  }, [bounds]);

  const moveInteraction = useCallback((event: React.PointerEvent<HTMLElement>) => {
    const active = interaction.current;
    if (!active) return;
    event.preventDefault();
    const dx = event.clientX - active.x;
    const dy = event.clientY - active.y;
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;
    if (active.kind === "move") {
      setBounds({
        ...active.bounds,
        x: clamp(active.bounds.x + dx, 0, Math.max(0, viewportWidth - 160)),
        y: clamp(active.bounds.y + dy, 0, Math.max(0, viewportHeight - 44)),
      });
      return;
    }
    setBounds({
      ...active.bounds,
      width: clamp(active.bounds.width + dx, MIN_WIDTH, Math.max(MIN_WIDTH, viewportWidth - active.bounds.x)),
      height: clamp(active.bounds.height + dy, MIN_HEIGHT, Math.max(MIN_HEIGHT, viewportHeight - active.bounds.y)),
    });
  }, []);

  const endInteraction = useCallback((event: React.PointerEvent<HTMLElement>) => {
    interaction.current = null;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
  }, []);

  const toggleMaximized = useCallback(() => {
    setBounds((current) => {
      if (current.maximized) return restoredBounds.current ?? initialBounds();
      restoredBounds.current = current;
      return { x: 12, y: 12, width: window.innerWidth - 24, height: window.innerHeight - 24, maximized: true };
    });
  }, []);

  return (
    <section
      className={`file-preview-window${bounds.maximized ? " is-maximized" : ""}`}
      role="dialog"
      aria-label={`Preview ${fileName}`}
      style={{ left: bounds.x, top: bounds.y, width: bounds.width, height: bounds.height }}
    >
      <header
        className="file-preview-titlebar"
        onDoubleClick={toggleMaximized}
        onPointerDown={(event) => beginInteraction("move", event)}
        onPointerMove={moveInteraction}
        onPointerUp={endInteraction}
        onPointerCancel={endInteraction}
      >
        <span title={filePath}>{fileName}</span>
        <div className="file-preview-window-actions" onPointerDown={(event) => event.stopPropagation()}>
          <button type="button" onClick={toggleMaximized} aria-label={bounds.maximized ? "Restore preview" : "Maximize preview"} title={bounds.maximized ? "Restore preview" : "Maximize preview"}>
            {bounds.maximized ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
          </button>
          <button type="button" onClick={onClose} aria-label="Close preview" title="Close preview"><X size={15} /></button>
        </div>
      </header>
      <div className="file-preview-content"><FileViewer projectId={projectId} filePath={filePath} fileName={fileName} /></div>
      {!bounds.maximized && (
        <div
          className="file-preview-resize"
          role="separator"
          aria-label="Resize preview"
          onPointerDown={(event) => beginInteraction("resize", event)}
          onPointerMove={moveInteraction}
          onPointerUp={endInteraction}
          onPointerCancel={endInteraction}
        />
      )}
    </section>
  );
}

function initialBounds(): Bounds {
  const viewportWidth = typeof window === "undefined" ? 1280 : window.innerWidth;
  const viewportHeight = typeof window === "undefined" ? 800 : window.innerHeight;
  const width = Math.min(760, Math.max(MIN_WIDTH, viewportWidth - 48));
  const height = Math.min(600, Math.max(MIN_HEIGHT, viewportHeight - 72));
  return {
    x: Math.max(12, Math.round((viewportWidth - width) / 2)),
    y: Math.max(12, Math.round((viewportHeight - height) / 2)),
    width,
    height,
    maximized: false,
  };
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimum, value));
}
