"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { Session } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

export type SessionLifecycleView = "active" | "archived";

export function useProjectSessions(projectId: string | null) {
  const [activeSessions, setActiveSessions] = useState<Session[]>([]);
  const [visibleSessions, setVisibleSessions] = useState<Session[]>([]);
  const [view, setViewState] = useState<SessionLifecycleView>("active");
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(false);
  const [resolvedKey, setResolvedKey] = useState<string | null>(null);
  const [mutatingId, setMutatingId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const viewRef = useRef(view);
  const setView = useCallback((nextView: SessionLifecycleView) => {
    viewRef.current = nextView;
    setViewState(nextView);
  }, []);

  const load = useCallback(async (delay = 0) => {
    controller.current?.abort();
    if (!projectId) {
      setActiveSessions([]);
      setVisibleSessions([]);
      setResolvedKey(null);
      setLoading(false);
      return;
    }
    const activeController = new AbortController();
    controller.current = activeController;
    const version = ++generation.current;
    if (delay > 0) await new Promise(resolve => setTimeout(resolve, delay));
    if (activeController.signal.aborted || generation.current !== version) return;
    setLoading(true);
    const base = `/v1/projects/${encodeURIComponent(projectId)}/sessions`;
    const currentQuery = new URLSearchParams({ status: view });
    const normalized = query.trim();
    const requestKey = `${projectId}:${view}:${normalized}`;
    if (normalized) currentQuery.set("q", normalized);
    try {
      const activeRequest = apiFetch<Session[]>(`${base}?status=active`, { signal: activeController.signal });
      const visibleRequest = view === "active" && !normalized
        ? activeRequest
        : apiFetch<Session[]>(`${base}?${currentQuery.toString()}`, { signal: activeController.signal });
      const [active, visible] = await Promise.all([activeRequest, visibleRequest]);
      if (activeController.signal.aborted || generation.current !== version) return;
      setActiveSessions(active);
      setVisibleSessions(visible);
      setResolvedKey(requestKey);
      setError(null);
    } catch (reason) {
      if (!activeController.signal.aborted && generation.current === version) {
        setResolvedKey(requestKey);
        setError((reason as Error).message);
      }
    } finally {
      if (!activeController.signal.aborted && generation.current === version) setLoading(false);
    }
  }, [projectId, query, view]);

  useEffect(() => {
    const timer = window.setTimeout(() => void load(0), 200);
    return () => {
      window.clearTimeout(timer);
      controller.current?.abort();
    };
  }, [load]);

  const refresh = useCallback(() => load(0), [load]);

  const replaceSession = useCallback((session: Session) => {
    setActiveSessions(current => session.status === "active"
      ? current.some(item => item.id === session.id) ? current.map(item => item.id === session.id ? session : item) : [session, ...current]
      : current.filter(item => item.id !== session.id));
    setVisibleSessions(current => current.map(item => item.id === session.id ? session : item)
      .filter(item => item.status === viewRef.current));
  }, []);

  const transition = useCallback(async (session: Session, action: "archive" | "restore") => {
    setMutatingId(session.id);
    try {
      await apiFetch<Session>(`/v1/sessions/${encodeURIComponent(session.id)}/${action}`, { method: "POST", body: "{}" });
      setAnnouncement(`${action === "archive" ? "Archived" : "Restored"} ${session.title}`);
      setError(null);
      await refresh();
      return true;
    } catch (reason) {
      setError((reason as Error).message);
      return false;
    } finally {
      setMutatingId(null);
    }
  }, [refresh]);

  const requestKey = projectId ? `${projectId}:${view}:${query.trim()}` : null;

  return {
    activeSessions, visibleSessions, view, setView, query, setQuery,
    loading: loading || (requestKey !== null && resolvedKey !== requestKey), mutatingId,
    error, setError, announcement, refresh, replaceSession,
    archive: (session: Session) => transition(session, "archive"),
    restore: (session: Session) => transition(session, "restore"),
  };
}
