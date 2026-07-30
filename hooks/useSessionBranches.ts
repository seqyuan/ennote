"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { components } from "@/lib/worker-api.gen";
import { apiFetch } from "@/lib/worker-api.client";

type Session = components["schemas"]["Session"];
type SessionBranch = components["schemas"]["SessionBranch"];
type BranchNavigation = components["schemas"]["BranchNavigation"];

interface UseSessionBranchesInput {
  sessionId: string | null;
  activeBranchId?: string;
  onSessionUpdated: (session: Session) => void;
}

export function useSessionBranches({ sessionId, activeBranchId, onSessionUpdated }: UseSessionBranchesInput) {
  const [branches, setBranches] = useState<SessionBranch[]>([]);
  const [dataSessionId, setDataSessionId] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [changing, setChanging] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    if (!sessionId) return;
    const version = generation.current;
    setLoading(true);
    try {
      const values = await apiFetch<SessionBranch[]>(`/v1/sessions/${encodeURIComponent(sessionId)}/branches`, { signal });
      if (!signal?.aborted && generation.current === version) {
        setBranches(values ?? []);
        setDataSessionId(sessionId);
        setError(null);
      }
    } catch (cause) {
      if (!signal?.aborted && generation.current === version) setError((cause as Error).message);
    } finally {
      if (!signal?.aborted && generation.current === version) setLoading(false);
    }
  }, [sessionId]);

  useEffect(() => {
    const version = ++generation.current;
    const controller = new AbortController();
    queueMicrotask(() => {
      if (generation.current !== version) return;
      setError(null);
      if (sessionId) void refresh(controller.signal);
      else setLoading(false);
    });
    return () => controller.abort();
  }, [activeBranchId, refresh, sessionId]);

  const applyNavigation = useCallback((navigation: BranchNavigation) => {
    generation.current += 1;
    setBranches(navigation.branches);
    setDataSessionId(navigation.session.id);
    onSessionUpdated(navigation.session);
    setError(null);
  }, [onSessionUpdated]);

  const createBranch = useCallback(async (fromMessageId: string, label?: string) => {
    if (!sessionId || changing) return false;
    setChanging(true);
    try {
      const navigation = await apiFetch<BranchNavigation>(`/v1/sessions/${encodeURIComponent(sessionId)}/branches`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ fromMessageId, ...(label?.trim() ? { label: label.trim() } : {}) }),
      });
      applyNavigation(navigation);
      return true;
    } catch (cause) {
      setError((cause as Error).message);
      return false;
    } finally {
      setChanging(false);
    }
  }, [applyNavigation, changing, sessionId]);

  const activateBranch = useCallback(async (branchId: string) => {
    if (!sessionId || changing || branchId === activeBranchId) return false;
    setChanging(true);
    try {
      const navigation = await apiFetch<BranchNavigation>(
        `/v1/sessions/${encodeURIComponent(sessionId)}/branches/${encodeURIComponent(branchId)}/activate`,
        { method: "POST" },
      );
      applyNavigation(navigation);
      return true;
    } catch (cause) {
      setError((cause as Error).message);
      return false;
    } finally {
      setChanging(false);
    }
  }, [activeBranchId, applyNavigation, changing, sessionId]);

  const clearError = useCallback(() => setError(null), []);
  return { branches: dataSessionId === sessionId ? branches : [], loading, changing, error,
    clearError, refresh, createBranch, activateBranch };
}
