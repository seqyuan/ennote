import type { components } from "@/lib/worker-api.gen";
import {
  prependCanonicalMessages,
  reconcileLatestMessages,
  type CanonicalMessage,
  type CanonicalMessagePage,
  type ContextCheckpoint,
  type TurnMessage,
} from "@/lib/chat-messages";
import { apiEventStream, apiFetch, WorkerGenerationChangedError, type WorkerSSEEvent } from "@/lib/worker-api.client";

type ContextCompaction = components["schemas"]["ContextCompaction"];
type AgentRun = components["schemas"]["AgentRun"];
type ToolApprovalRequest = components["schemas"]["ToolApprovalRequest"];
type QueuedInput = components["schemas"]["QueuedInput"];
type SessionRunSnapshot = components["schemas"]["SessionRunSnapshot"];

// Frames of the session change feed (GET /v1/sessions/{id}/events).
type SessionStreamFrame =
  | { type: "subscribed"; instanceId?: string; lastSeq?: number; snapshot?: SessionRunSnapshot }
  | { type: "snapshot"; lastSeq?: number; snapshot?: SessionRunSnapshot }
  | { type: "message_committed"; runId?: string; payload?: unknown; firstSeq?: number; lastSeq?: number; createdAt?: string }
  | { type: string; eventId?: number; runId?: string; seq?: number; payload?: unknown; createdAt?: string };

const PAGE_SIZE = 50;

// Phase A residency cap (design §7): stores are kept in the module-level
// registry and evicted least-recently-used once this limit is crossed.
const STORE_LIMIT = 16;

export interface SessionStoreSnapshot {
  sessionId: string;
  dataBranchId: string | undefined;
  loaded: boolean;
  canonical: CanonicalMessage[];
  checkpoints: ContextCheckpoint[];
  transient: TurnMessage[];
  nextCursor: string | undefined;
  hasMore: boolean;
  loading: boolean;
  loadingOlder: boolean;
  error: string | null;
  // Transient projection fed by the session change feed (phase C).
  activeRun: AgentRun | null;
  pendingApproval: ToolApprovalRequest | null;
  queuedInputs: QueuedInput[];
  delegationActive: boolean;
  lastSeq: number;
  workerInstanceId: string | null;
  streamOpen: boolean;
}

type Listener = () => void;

/**
 * SessionStore is the pure (React-free) data layer for one session's history
 * window. It owns the canonical messages + checkpoints + transient deltas and
 * the fetch lifecycle, so the data survives React component unmount. The React
 * hook (useSessionStore) is only a subscription onto the notifier.
 *
 * Phase A feeds it through the existing worker APIs (/messages + /compactions);
 * later phases swap the fetch source for the session event stream.
 */
export class SessionStore {
  readonly sessionId: string;

  private dataBranchId: string | undefined;
  // The branch the hook most recently asked for. refreshLatest adopts it on
  // success so a run finishing right after a branch switch lands on the right
  // window (mirrors the pre-store hook setting dataBranchID explicitly).
  private requestedBranchId: string | undefined;
  private loaded = false;
  private canonical: CanonicalMessage[] = [];
  private checkpoints: ContextCheckpoint[] = [];
  private transient: TurnMessage[] = [];
  private nextCursor: string | undefined;
  private hasMore = false;
  private loading = false;
  private loadingOlder = false;
  private error: string | null = null;

  private requestVersion = 0;
  private requestController: AbortController | null = null;

  // Session change-feed projection (phase C): the authoritative transient state
  // pushed by the worker snapshot frames. Off-screen stores keep consuming this
  // through their own SSE connection, which is what enables multi-session
  // parallel push.
  private activeRun: AgentRun | null = null;
  private pendingApproval: ToolApprovalRequest | null = null;
  private queuedInputs: QueuedInput[] = [];
  private delegationActive = false;
  private lastSeq = 0;
  private workerInstanceId: string | null = null;

  private streamController: AbortController | null = null;
  private streamOpen = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempt = 0;
  private eventCursor: string | undefined;
  private disposed = false;

  private listeners = new Set<Listener>();
  private snapshot: SessionStoreSnapshot | null = null;

  constructor(sessionId: string) {
    this.sessionId = sessionId;
  }

  // ——— external store contract (useSyncExternalStore) ———

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getSnapshot = (): SessionStoreSnapshot => {
    if (!this.snapshot) this.snapshot = this.buildSnapshot();
    return this.snapshot;
  };

  // Server snapshot is always the empty store: fetch only happens client-side
  // (in useEffect), so SSR and hydration agree on an empty window.
  getServerSnapshot = (): SessionStoreSnapshot => {
    if (!this.snapshot) this.snapshot = this.buildSnapshot();
    return this.snapshot;
  };

  // ——— data lifecycle ———

  /**
   * Loads the initial window for a branch. Idempotent: a second call for the
   * same already-loaded branch is a cache hit (no re-fetch). This is what makes
   * switching away and back "free" — the window survives in the registry.
   */
  ensureLoaded = (branchId: string | undefined): Promise<void> => {
    this.requestedBranchId = branchId;
    this.openStream();
    if (this.loaded && this.dataBranchId === branchId) return Promise.resolve();
    this.resetForBranch();
    return this.loadInitial(branchId);
  };

  loadOlder = async (): Promise<boolean> => {
    if (!this.loaded || !this.hasMore || !this.nextCursor || this.loadingOlder) return false;
    this.requestVersion += 1;
    const version = this.requestVersion;
    this.requestController?.abort();
    const controller = new AbortController();
    this.requestController = controller;
    this.loadingOlder = true;
    this.error = null;
    this.notify();
    try {
      const page = await apiFetch<CanonicalMessagePage>(
        `/v1/sessions/${encodeURIComponent(this.sessionId)}/messages?limit=${PAGE_SIZE}&before=${encodeURIComponent(this.nextCursor)}`,
        { signal: controller.signal },
      );
      if (controller.signal.aborted || this.requestVersion !== version) return false;
      const older = page.messages ?? [];
      const current = this.canonical;
      const olderTail = older.length > 0 ? (older[older.length - 1].seq ?? 0) : 0;
      const currentHead = current.length > 0 ? (current[0].seq ?? 0) : 0;
      if (olderTail > 0 && currentHead > 0 && olderTail + 1 !== currentHead) {
        // Consecutive assertion (design §3.2.6): a gap means the window is out
        // of order; fail-soft rather than rendering a torn timeline.
        this.error = "History window is out of order; re-open the session to re-sync.";
        return false;
      }
      this.canonical = prependCanonicalMessages(current, older);
      this.nextCursor = page.nextCursor;
      this.hasMore = page.hasMore;
      return true;
    } catch (cause) {
      if (!controller.signal.aborted && this.requestVersion === version) {
        this.error = (cause as Error).message;
      }
      return false;
    } finally {
      if (!controller.signal.aborted && this.requestVersion === version) this.loadingOlder = false;
      this.notify();
    }
  };

  refreshLatest = async (): Promise<void> => {
    this.requestVersion += 1;
    const version = this.requestVersion;
    this.requestController?.abort();
    const controller = new AbortController();
    this.requestController = controller;
    this.loadingOlder = false;
    try {
      const [page, values] = await Promise.all([
        apiFetch<CanonicalMessagePage>(
          `/v1/sessions/${encodeURIComponent(this.sessionId)}/messages?limit=${PAGE_SIZE}`,
          { signal: controller.signal },
        ),
        apiFetch<ContextCompaction[]>(
          `/v1/sessions/${encodeURIComponent(this.sessionId)}/compactions`,
          { signal: controller.signal },
        ),
      ]);
      if (controller.signal.aborted || this.requestVersion !== version) return;
      const replacingUnloaded = !this.loaded;
      this.canonical = replacingUnloaded
        ? (page.messages ?? [])
        : reconcileLatestMessages(this.canonical, page.messages ?? []);
      this.checkpoints = values ?? [];
      if (replacingUnloaded) {
        this.nextCursor = page.nextCursor;
        this.hasMore = page.hasMore;
      }
      this.dataBranchId = this.requestedBranchId;
      this.loaded = true;
      this.transient = [];
      this.error = null;
    } catch (cause) {
      if (!controller.signal.aborted && this.requestVersion === version) {
        this.error = (cause as Error).message;
      }
    } finally {
      this.notify();
    }
  };

  appendTransient = (message: TurnMessage): void => {
    if (this.transient.some((item) => item.id === message.id)) return;
    this.transient = [...this.transient, message];
    this.notify();
  };

  upsertTransient = (message: TurnMessage): void => {
    const index = this.transient.findIndex((item) => item.id === message.id);
    if (index < 0) {
      this.transient = [...this.transient, message];
    } else {
      const next = [...this.transient];
      next[index] = { ...next[index], ...message };
      this.transient = next;
    }
    this.notify();
  };

  // ——— session change feed (phase C) ———

  openStream = (): void => {
    if (this.disposed || this.streamOpen || this.streamController) return;
    void this.openStreamInternal();
  };

  dispose = (): void => {
    this.disposed = true;
    this.requestController?.abort();
    this.streamController?.abort();
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    this.listeners.clear();
  };

  // ——— internals ———

  private async openStreamInternal(): Promise<void> {
    if (this.disposed) return;
    const controller = new AbortController();
    this.streamController = controller;
    this.streamOpen = true;
    this.notify();
    try {
      const headers: Record<string, string> = {};
      if (this.eventCursor) headers["Last-Event-ID"] = this.eventCursor;
      await apiEventStream<SessionStreamFrame>(
        `/v1/sessions/${encodeURIComponent(this.sessionId)}/events`,
        (event) => this.handleStreamFrame(event),
        { signal: controller.signal, headers },
      );
    } catch (cause) {
      if (cause instanceof WorkerGenerationChangedError) this.handleGenerationChanged();
    } finally {
      this.streamController = null;
      this.streamOpen = false;
      this.notify();
      if (!this.disposed) this.scheduleReconnect();
    }
  }

  private handleStreamFrame(event: WorkerSSEEvent<SessionStreamFrame>): void {
    if (event.id) this.eventCursor = event.id;
    this.reconnectAttempt = 0;
    const data = event.data;
    if (!data || typeof data !== "object") return;
    switch (data.type) {
      case "subscribed": {
        const frame = data as Extract<SessionStreamFrame, { type: "subscribed" }>;
        this.workerInstanceId = frame.instanceId ?? null;
        this.lastSeq = frame.lastSeq ?? 0;
        this.applySnapshot(frame.snapshot);
        break;
      }
      case "snapshot": {
        const frame = data as Extract<SessionStreamFrame, { type: "snapshot" }>;
        this.lastSeq = frame.lastSeq ?? 0;
        this.applySnapshot(frame.snapshot);
        break;
      }
      case "message_committed": {
        const frame = data as Extract<SessionStreamFrame, { type: "message_committed" }>;
        this.lastSeq = Math.max(this.lastSeq, frame.lastSeq ?? 0);
        void this.refreshLatest();
        break;
      }
      case "run_succeeded":
      case "run_failed":
      case "run_cancelled":
      case "run_interrupted":
      case "context_compaction_completed":
      case "run_context_compaction_completed":
        void this.refreshLatest();
        break;
      default:
        // approval_requested/resolved, input_queued/injected: the snapshot
        // frame carries the authoritative projection.
        break;
    }
  }

  private applySnapshot(snapshot?: SessionRunSnapshot): void {
    if (!snapshot) return;
    this.activeRun = snapshot.activeRun ?? null;
    this.pendingApproval = snapshot.pendingApproval ?? null;
    this.queuedInputs = snapshot.queuedInputs ?? [];
    this.delegationActive = snapshot.delegationActive ?? false;
    if (snapshot.checkpoints) {
      this.checkpoints = snapshot.checkpoints.map((item) => ({
        id: item.id,
        status: item.status,
        reason: item.reason,
        summary: item.summary,
        reclaimedTokens: item.reclaimedTokens,
        firstKeptMessageId: item.firstKeptMessageId,
        sourceThroughMessageId: item.sourceThroughMessageId,
        baseLeafMessageId: item.baseLeafMessageId,
        createdAt: item.createdAt,
      }));
    }
    this.notify();
  }

  private handleGenerationChanged(): void {
    this.workerInstanceId = null;
    this.eventCursor = undefined;
    this.reconnectAttempt = 0;
    this.activeRun = null;
    this.pendingApproval = null;
    this.queuedInputs = [];
    this.delegationActive = false;
  }

  private scheduleReconnect(): void {
    if (this.disposed || this.reconnectTimer) return;
    const delay = Math.min(1000 * 2 ** this.reconnectAttempt, 15000);
    this.reconnectAttempt += 1;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.openStream();
    }, delay);
  }

  private resetForBranch(): void {
    this.requestVersion += 1;
    this.requestController?.abort();
    this.requestController = new AbortController();
    this.dataBranchId = undefined;
    this.loaded = false;
    this.canonical = [];
    this.checkpoints = [];
    this.transient = [];
    this.nextCursor = undefined;
    this.hasMore = false;
    this.error = null;
    this.loading = true;
    this.loadingOlder = false;
    this.notify();
  }

  private async loadInitial(branchId: string | undefined): Promise<void> {
    const version = this.requestVersion;
    const controller = this.requestController!;
    try {
      const [page, values] = await Promise.all([
        apiFetch<CanonicalMessagePage>(
          `/v1/sessions/${encodeURIComponent(this.sessionId)}/messages?limit=${PAGE_SIZE}`,
          { signal: controller.signal },
        ),
        apiFetch<ContextCompaction[]>(
          `/v1/sessions/${encodeURIComponent(this.sessionId)}/compactions`,
          { signal: controller.signal },
        ),
      ]);
      if (controller.signal.aborted || this.requestVersion !== version) return;
      this.dataBranchId = branchId;
      this.canonical = page.messages ?? [];
      this.checkpoints = values ?? [];
      this.nextCursor = page.nextCursor;
      this.hasMore = page.hasMore;
      this.loaded = true;
    } catch (cause) {
      if (!controller.signal.aborted && this.requestVersion === version) {
        this.error = (cause as Error).message;
      }
    } finally {
      if (!controller.signal.aborted && this.requestVersion === version) this.loading = false;
      this.notify();
    }
  }

  private buildSnapshot(): SessionStoreSnapshot {
    return {
      sessionId: this.sessionId,
      dataBranchId: this.dataBranchId,
      loaded: this.loaded,
      canonical: this.canonical,
      checkpoints: this.checkpoints,
      transient: this.transient,
      nextCursor: this.nextCursor,
      hasMore: this.hasMore,
      loading: this.loading,
      loadingOlder: this.loadingOlder,
      error: this.error,
      activeRun: this.activeRun,
      pendingApproval: this.pendingApproval,
      queuedInputs: this.queuedInputs,
      delegationActive: this.delegationActive,
      lastSeq: this.lastSeq,
      workerInstanceId: this.workerInstanceId,
      streamOpen: this.streamOpen,
    };
  }

  private notify(): void {
    this.snapshot = null;
    for (const listener of this.listeners) listener();
  }
}

/**
 * SessionRegistry is the module-level residency holder: a Map keyed by
 * sessionId plus LRU eviction. It is deliberately a small registry, not a
 * connection manager — each SessionStore owns its own fetch/connection
 * lifecycle (design §D3). Phase 2 (global mux) can swap the registry internals
 * without changing call sites.
 */
export class SessionRegistry {
  private stores = new Map<string, SessionStore>();
  private order: string[] = [];

  getStore = (sessionId: string): SessionStore => {
    let store = this.stores.get(sessionId);
    if (!store) {
      store = new SessionStore(sessionId);
      this.stores.set(sessionId, store);
      this.order.push(sessionId);
      this.prune(STORE_LIMIT);
    }
    this.touch(sessionId);
    return store;
  };

  has = (sessionId: string): boolean => this.stores.has(sessionId);

  disposeStore = (sessionId: string): void => {
    const store = this.stores.get(sessionId);
    if (store) {
      store.dispose();
      this.stores.delete(sessionId);
    }
    const index = this.order.indexOf(sessionId);
    if (index >= 0) this.order.splice(index, 1);
  };

  prune = (limit: number): void => {
    while (this.order.length > limit) {
      const id = this.order.shift();
      if (id !== undefined) this.disposeStore(id);
    }
  };

  private touch(sessionId: string): void {
    const index = this.order.indexOf(sessionId);
    if (index >= 0) this.order.splice(index, 1);
    this.order.push(sessionId);
  }
}

export function createSessionRegistry(): SessionRegistry {
  return new SessionRegistry();
}

// Default singleton: the app has one Worker origin, so one registry is enough.
// Tests construct fresh registries via createSessionRegistry().
export const sessionRegistry = createSessionRegistry();
