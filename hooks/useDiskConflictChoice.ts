"use client";

import { useCallback, useState, type Dispatch, type MutableRefObject, type SetStateAction } from "react";
import { diffTaskIDs, applyOps, type FlushSession } from "@/lib/graph-ops";
import type { FlowDefinition, GraphDetail } from "@/components/graphs/types";
import { apiFetch } from "@/lib/worker-api.client";

/** The disk's version of the open Graph, as described to the reader before they choose. */
export type DiskPreview = { graphId: string; name: string; changed: string[] };

export type DiskConflictChoice = {
  diskPreview: DiskPreview | null;
  /** Drop the preview when the editor moves to another Graph. */
  clearDiskPreview: () => void;
  /** Re-read the Graph the conflict is about and describe how it differs. */
  viewDiskVersion: () => Promise<void>;
  /** Keep the queue, rebase it onto the disk definition, and drain on top of it. */
  keepMine: () => Promise<void>;
  /** Drop the queue and adopt the disk definition. */
  discardMine: () => Promise<void>;
};

/**
 * The disk side of a save conflict.
 *
 * Both choices are read-then-write: the definition is fetched again so the next
 * CAS carries the digest that is actually on disk. A read that returns after the
 * reader switched Graphs must not be adopted, so every write is guarded by the
 * session identity it started from - the same rule the drain follows.
 */
export function useDiskConflictChoice(input: {
  sessionRef: MutableRefObject<FlushSession | null>;
  serverDefRef: MutableRefObject<FlowDefinition | null>;
  latestDraftRef: MutableRefObject<FlowDefinition | null>;
  digestRef: MutableRefObject<string | null>;
  flush: (target: FlushSession) => Promise<boolean>;
  adoptServerDefinition: (updated: GraphDetail) => void;
  setDraft: Dispatch<SetStateAction<FlowDefinition | null>>;
  setPendingOps: Dispatch<SetStateAction<number>>;
  setConflict: Dispatch<SetStateAction<{ graphId: string; reason: "conflict" | "reload" } | null>>;
  setSaveError: Dispatch<SetStateAction<{ message: string; graphId: string } | null>>;
  setError: (value: string | null) => void;
}): DiskConflictChoice {
  const [diskPreview, setDiskPreview] = useState<DiskPreview | null>(null);
  const {
    sessionRef, serverDefRef, latestDraftRef, digestRef, flush, adoptServerDefinition,
    setDraft, setPendingOps, setConflict, setSaveError, setError,
  } = input;

  const viewDiskVersion = useCallback(async () => {
    const target = sessionRef.current;
    if (!target) return;
    try {
      const disk = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(target.graphId)}`);
      setDiskPreview({
        graphId: target.graphId,
        name: disk.definition?.name ?? disk.name,
        changed: diffTaskIDs(latestDraftRef.current, disk.definition ?? null),
      });
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [sessionRef, latestDraftRef, setError]);

  const keepMine = useCallback(async () => {
    const target = sessionRef.current;
    if (!target || target.running) return;
    try {
      const disk = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(target.graphId)}`);
      if (sessionRef.current !== target) return;
      // Rebase onto the disk definition so the next CAS carries its digest; my
      // queue is unchanged and goes out on top of it.
      const base = disk.definition ?? serverDefRef.current!;
      serverDefRef.current = base;
      digestRef.current = disk.digest;
      latestDraftRef.current = applyOps(base, target.ops.slice(target.sent));
      setDraft(latestDraftRef.current);
      setConflict(null);
      setDiskPreview(null);
      if (target.sent < target.ops.length) void flush(target);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [sessionRef, serverDefRef, latestDraftRef, digestRef, flush, setDraft, setConflict, setError]);

  const discardMine = useCallback(async () => {
    const target = sessionRef.current;
    if (!target || target.running) return;
    try {
      const disk = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(target.graphId)}`);
      if (sessionRef.current !== target) return;
      target.ops = [];
      target.sent = 0;
      adoptServerDefinition(disk);
      setPendingOps(0);
      setConflict(null);
      setSaveError(null);
      setDiskPreview(null);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [sessionRef, adoptServerDefinition, setPendingOps, setConflict, setSaveError, setError]);

  const clearDiskPreview = useCallback(() => setDiskPreview(null), []);

  return { diskPreview, clearDiskPreview, viewDiskVersion, keepMine, discardMine };
}
