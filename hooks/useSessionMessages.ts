"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { components } from "@/lib/worker-api.gen";
import {
  mergeTimeline,
  prependCanonicalMessages,
  reconcileLatestMessages,
  type CanonicalMessage,
  type CanonicalMessagePage,
  type ContextCheckpoint,
  type TurnMessage,
} from "@/lib/chat-messages";
import { apiFetch } from "@/lib/worker-api.client";

type ContextCompaction = components["schemas"]["ContextCompaction"];

const PAGE_SIZE = 50;

export function useSessionMessages(sessionId: string | null, activeBranchId?: string) {
  const [dataSessionID, setDataSessionID] = useState<string | null>(null);
  const [dataBranchID, setDataBranchID] = useState<string | undefined>();
  const [canonical, setCanonical] = useState<CanonicalMessage[]>([]);
  const [checkpoints, setCheckpoints] = useState<ContextCheckpoint[]>([]);
  const [transient, setTransient] = useState<TurnMessage[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const dataSessionIDRef = useRef<string | null>(null);
  const dataBranchIDRef = useRef<string | undefined>(undefined);
  const requestVersion = useRef(0);
  const requestController = useRef<AbortController | null>(null);
  const currentSession = useRef<string | null>(sessionId);

  useEffect(() => {
    currentSession.current = sessionId;
    requestVersion.current += 1;
    const version = requestVersion.current;
    requestController.current?.abort();
    const controller = new AbortController();
    requestController.current = controller;
    queueMicrotask(() => {
      if (controller.signal.aborted || requestVersion.current !== version) return;
      dataSessionIDRef.current = null;
      dataBranchIDRef.current = undefined;
      setDataSessionID(null);
      setDataBranchID(undefined);
      setCanonical([]);
      setCheckpoints([]);
      setTransient([]);
      setNextCursor(undefined);
      setHasMore(false);
      setHistoryError(null);
      setLoading(Boolean(sessionId));
      setLoadingOlder(false);
    });

    if (!sessionId) return () => controller.abort();

    Promise.all([
      apiFetch<CanonicalMessagePage>(`/v1/sessions/${encodeURIComponent(sessionId)}/messages?limit=${PAGE_SIZE}`, { signal: controller.signal }),
      apiFetch<ContextCompaction[]>(`/v1/sessions/${encodeURIComponent(sessionId)}/compactions`, { signal: controller.signal }),
    ]).then(([page, values]) => {
      if (controller.signal.aborted || requestVersion.current !== version || currentSession.current !== sessionId) return;
      dataSessionIDRef.current = sessionId;
      dataBranchIDRef.current = activeBranchId;
      setDataSessionID(sessionId);
      setDataBranchID(activeBranchId);
      setCanonical(page.messages ?? []);
      setCheckpoints(values ?? []);
      setNextCursor(page.nextCursor);
      setHasMore(page.hasMore);
    }).catch(error => {
      if (!controller.signal.aborted && requestVersion.current === version) {
        setHistoryError((error as Error).message);
      }
    }).finally(() => {
      if (!controller.signal.aborted && requestVersion.current === version) setLoading(false);
    });

    return () => controller.abort();
  }, [activeBranchId, sessionId]);

  const loadOlder = useCallback(async () => {
    const activeSession = currentSession.current;
    if (!activeSession || !hasMore || !nextCursor || loadingOlder) return false;
    requestController.current?.abort();
    const controller = new AbortController();
    requestController.current = controller;
    const version = ++requestVersion.current;
    setLoadingOlder(true);
    setHistoryError(null);
    try {
      const page = await apiFetch<CanonicalMessagePage>(
        `/v1/sessions/${encodeURIComponent(activeSession)}/messages?limit=${PAGE_SIZE}&before=${encodeURIComponent(nextCursor)}`,
        { signal: controller.signal },
      );
      if (controller.signal.aborted || requestVersion.current !== version || currentSession.current !== activeSession) return false;
      setCanonical(current => prependCanonicalMessages(current, page.messages ?? []));
      setNextCursor(page.nextCursor);
      setHasMore(page.hasMore);
      return true;
    } catch (error) {
      if (!controller.signal.aborted && requestVersion.current === version) setHistoryError((error as Error).message);
      return false;
    } finally {
      if (!controller.signal.aborted && requestVersion.current === version) setLoadingOlder(false);
    }
  }, [hasMore, loadingOlder, nextCursor]);

  const refreshLatest = useCallback(async () => {
    const activeSession = currentSession.current;
    if (!activeSession) return;
    requestController.current?.abort();
    const controller = new AbortController();
    requestController.current = controller;
    const version = ++requestVersion.current;
    setLoadingOlder(false);
    try {
      const [page, values] = await Promise.all([
        apiFetch<CanonicalMessagePage>(`/v1/sessions/${encodeURIComponent(activeSession)}/messages?limit=${PAGE_SIZE}`, { signal: controller.signal }),
        apiFetch<ContextCompaction[]>(`/v1/sessions/${encodeURIComponent(activeSession)}/compactions`, { signal: controller.signal }),
      ]);
      if (controller.signal.aborted || requestVersion.current !== version || currentSession.current !== activeSession) return;
      const replacingUnloadedSession = dataSessionIDRef.current !== activeSession || dataBranchIDRef.current !== activeBranchId;
      dataSessionIDRef.current = activeSession;
      dataBranchIDRef.current = activeBranchId;
      setDataSessionID(activeSession);
      setDataBranchID(activeBranchId);
      setCanonical(current => replacingUnloadedSession
        ? (page.messages ?? [])
        : reconcileLatestMessages(current, page.messages ?? []));
      setCheckpoints(values ?? []);
      if (replacingUnloadedSession) {
        setNextCursor(page.nextCursor);
        setHasMore(page.hasMore);
      }
      setTransient([]);
      setHistoryError(null);
    } catch (error) {
      if (!controller.signal.aborted && requestVersion.current === version) setHistoryError((error as Error).message);
    }
  }, [activeBranchId]);

  const appendTransient = useCallback((message: TurnMessage) => {
    setTransient(current => current.some(item => item.id === message.id) ? current : [...current, message]);
  }, []);

  const upsertTransient = useCallback((message: TurnMessage) => {
    setTransient(current => {
      const index = current.findIndex(item => item.id === message.id);
      if (index < 0) return [...current, message];
      const next = [...current];
      next[index] = { ...current[index], ...message };
      return next;
    });
  }, []);

  const timeline = useMemo(
    () => sessionId && dataSessionID === sessionId && dataBranchID === activeBranchId
      ? mergeTimeline(canonical, checkpoints, transient) : [],
    [activeBranchId, canonical, checkpoints, dataBranchID, dataSessionID, sessionId, transient],
  );

  return {
    messages: timeline,
    loading: Boolean(sessionId) && (dataSessionID !== sessionId || dataBranchID !== activeBranchId) && !historyError ? true : loading,
    loadingOlder,
    historyError,
    hasMore,
    loadOlder,
    refreshLatest,
    appendTransient,
    upsertTransient,
  };
}
