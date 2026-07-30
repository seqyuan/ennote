"use client";

import { useEffect, useState } from "react";

interface StreamingStatusBarProps {
  status: string;
  activeRun: boolean;
  waiting: boolean;
  reconnecting: boolean;
  compacting: boolean;
}

export function StreamingStatusBar({ status, activeRun, waiting, reconnecting, compacting }: StreamingStatusBarProps) {
  const [startedAt, setStartedAt] = useState<number | null>(null);
  const [clock, setClock] = useState(0);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      if (!activeRun) {
        setStartedAt(null);
        setClock(0);
        return;
      }
      const timestamp = Date.now();
      setStartedAt(timestamp);
      setClock(timestamp);
    });
    if (!activeRun) return () => window.cancelAnimationFrame(frame);
    const timer = window.setInterval(() => setClock(Date.now()), 1000);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearInterval(timer);
    };
  }, [activeRun]);

  if (!activeRun && !status) return null;
  const elapsed = startedAt ? Math.max(0, Math.floor((clock - startedAt) / 1000)) : 0;
  const label = compacting ? "Compacting context..." : reconnecting ? "Reconnecting..." : waiting ? "Waiting for approval..." : status || "Working...";

  return (
    <div className="streaming-status" role="status" aria-live="polite">
      <span>{label}{startedAt ? `  ${formatElapsed(elapsed)}` : ""}</span>
    </div>
  );
}

function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
}
