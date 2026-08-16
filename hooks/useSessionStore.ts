"use client";

import { useEffect, useMemo, useSyncExternalStore } from "react";

import { mergeTimeline, type ConversationNode, type TurnMessage } from "@/lib/chat-messages";
import { sessionRegistry, type SessionStoreSnapshot } from "@/lib/session-store";
import type { components } from "@/lib/worker-api.gen";

type AgentRun = components["schemas"]["AgentRun"];
type ToolApprovalRequest = components["schemas"]["ToolApprovalRequest"];
type QueuedInput = components["schemas"]["QueuedInput"];

// Stable no-op values used when there is no selected session. They keep the
// returned API shape constant so callers don't need null branches.
const NO_SNAPSHOT: SessionStoreSnapshot = {
  sessionId: "",
  dataBranchId: undefined,
  loaded: false,
  canonical: [],
  checkpoints: [],
  transient: [],
  nextCursor: undefined,
  hasMore: false,
  loading: false,
  loadingOlder: false,
  error: null,
  activeRun: null,
  pendingApproval: null,
  queuedInputs: [],
  delegationActive: false,
  lastSeq: 0,
  workerInstanceId: null,
  streamOpen: false,
};

const noopSubscribe = (): (() => void) => () => {};
const noopLoadOlder = async (): Promise<boolean> => false;
const noopRefresh = async (): Promise<void> => {};
const noopAppend = (): void => {};
const noopUpsert = (): void => {};

export interface UseSessionStoreResult {
  messages: ConversationNode[];
  loading: boolean;
  loadingOlder: boolean;
  historyError: string | null;
  hasMore: boolean;
  loadOlder: () => Promise<boolean>;
  refreshLatest: () => Promise<void>;
  appendTransient: (message: TurnMessage) => void;
  upsertTransient: (message: TurnMessage) => void;
  // Transient projection from the session change feed (phase C).
  activeRun: AgentRun | null;
  pendingApproval: ToolApprovalRequest | null;
  queuedInputs: QueuedInput[];
  delegationActive: boolean;
  lastSeq: number;
  workerInstanceId: string | null;
  streamOpen: boolean;
}

/**
 * React subscription onto the module-level SessionStore for a session.
 *
 * - The store is lazily created (and thereafter resident) in the registry, so
 *   unmounting this hook only unsubscribes — it never destroys the store.
 * - Switching away and back is a cache hit when the same branch is already
 *   loaded; no re-fetch (phase A residency guarantee).
 */
export function useSessionStore(sessionId: string | null, branchId?: string): UseSessionStoreResult {
  const store = sessionId ? sessionRegistry.getStore(sessionId) : null;

  const snapshot = useSyncExternalStore(
    store ? store.subscribe : noopSubscribe,
    store ? store.getSnapshot : (): SessionStoreSnapshot => NO_SNAPSHOT,
    store ? store.getServerSnapshot : (): SessionStoreSnapshot => NO_SNAPSHOT,
  );

  // Trigger the (idempotent) load. Runs after paint, client-only; the store
  // keeps the fetch alive across unmount so in-flight loads complete.
  useEffect(() => {
    if (store) void store.ensureLoaded(branchId);
  }, [store, branchId]);

  const loaded = Boolean(sessionId) && snapshot.loaded && snapshot.dataBranchId === branchId;

  const messages = useMemo(
    () => (loaded ? mergeTimeline(snapshot.canonical, snapshot.checkpoints, snapshot.transient) : []),
    [loaded, snapshot.canonical, snapshot.checkpoints, snapshot.transient],
  );

  // Mirrors the pre-store loading semantics: report loading while the window
  // for this (session, branch) has not been loaded, unless an error surfaced.
  const loading = Boolean(sessionId) && !loaded && !snapshot.error ? true : snapshot.loading;

  return {
    messages,
    loading,
    loadingOlder: snapshot.loadingOlder,
    historyError: snapshot.error,
    hasMore: snapshot.hasMore,
    loadOlder: store ? store.loadOlder : noopLoadOlder,
    refreshLatest: store ? store.refreshLatest : noopRefresh,
    appendTransient: store ? store.appendTransient : noopAppend,
    upsertTransient: store ? store.upsertTransient : noopUpsert,
    activeRun: snapshot.activeRun,
    pendingApproval: snapshot.pendingApproval,
    queuedInputs: snapshot.queuedInputs,
    delegationActive: snapshot.delegationActive,
    lastSeq: snapshot.lastSeq,
    workerInstanceId: snapshot.workerInstanceId,
    streamOpen: snapshot.streamOpen,
  };
}
