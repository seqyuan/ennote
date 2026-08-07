"use client";

import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

export interface ChildProgressEvent {
  delegationGroupId: string;
  taskName: string;
  childRunId: string;
  activity: string;
  tokens: number;
}

// ChildProgressState maps delegation group id -> task name -> latest activity.
type ChildProgressState = ReadonlyMap<string, ReadonlyMap<string, string>>;

// Module-level delivery: the single SSE consumer (useAgentSession) calls
// registerChildProgress for every live child_progress delta; the active
// ChildProgressProvider subscribes and keeps the rendered map fresh. This
// avoids threading a callback through AppShell's large prop surface.
let listener: ((event: ChildProgressEvent) => void) | null = null;

export function registerChildProgress(event: ChildProgressEvent): void {
  listener?.(event);
}

export function setChildProgressListener(next: ((event: ChildProgressEvent) => void) | null): void {
  listener = next;
}

interface ChildProgressContextValue {
  /** Latest live activity per group/task (non-durable rendering delta). */
  progress: ChildProgressState;
}

const ChildProgressContext = createContext<ChildProgressContextValue | null>(null);

export function ChildProgressProvider({ children }: { children: ReactNode }) {
  const [progress, setProgress] = useState<ChildProgressState>(new Map());

  useEffect(() => {
    setChildProgressListener((event) => {
      setProgress((current) => {
        const next = new Map(current);
        const group = new Map(next.get(event.delegationGroupId) ?? []);
        group.set(event.taskName, event.activity);
        next.set(event.delegationGroupId, group);
        return next;
      });
    });
    return () => setChildProgressListener(null);
  }, []);

  const value = useMemo(() => ({ progress }), [progress]);
  return <ChildProgressContext.Provider value={value}>{children}</ChildProgressContext.Provider>;
}

export function useChildProgress(): ChildProgressState {
  const value = useContext(ChildProgressContext);
  if (!value) throw new Error("useChildProgress must be used inside ChildProgressProvider");
  return value.progress;
}
