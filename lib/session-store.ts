import type { components } from "@/lib/worker-api.gen";
import {
  prependCanonicalMessages,
  reconcileLatestMessages,
  type CanonicalMessage,
  type CanonicalMessagePage,
  type ContextCheckpoint,
  type TurnMessage,
} from "@/lib/chat-messages";
import { WindowRefresh } from "@/lib/session-window-refresh";
import { apiEventStream, apiFetch, type WorkerSSEEvent } from "@/lib/worker-api.client";

type ContextCompaction = components["schemas"]["ContextCompaction"];
type AgentRun = components["schemas"]["AgentRun"];
type ToolApprovalRequest = components["schemas"]["ToolApprovalRequest"];
type QueuedInput = components["schemas"]["QueuedInput"];
type SessionRunSnapshot = components["schemas"]["SessionRunSnapshot"];

// Frames of the session change feed (GET /v1/sessions/{id}/events). The shapes
// come from contracts/openapi.yaml rather than being restated here, so a frame
// field that changes on the Worker side fails typecheck in this consumer
// instead of silently arriving as undefined. The Go half of the same guard is
// internal/api/stream_contract_test.go.
type SessionStreamFrame = components["schemas"]["SessionEventStreamFrame"];

const PAGE_SIZE = 50;

// Phase A residency cap (design §7): stores are kept in the module-level
// registry and evicted least-recently-used once this limit is crossed. Only the
// selected session keeps its change feed open (see pauseStream), so this is a
// memory cap, not a connection cap.
const STORE_LIMIT = 8;

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
  // pushed by the worker snapshot frames. Only the visible session holds a
  // socket (pauseStream drops it to stay inside the browser's per-origin
  // connection budget), so this projection is worth exactly as much as the
  // stream that feeds it: it is cleared on pause and rebuilt from the
  // `subscribed` frame the next stream sends.
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
  private streamPaused = false;
  private disposed = false;
  private readonly windowRefresh = new WindowRefresh(() => this.refreshLatest());

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
    if (this.loaded && this.dataBranchId === branchId) {
      // The window is resident, but a pause may have interrupted a read that the
      // change feed already told us about. Those events are behind eventCursor
      // and never replay, so this is the only place that can still honour them.
      if (this.windowRefresh.takeInterruptedRead()) return this.refreshLatest();
      return Promise.resolve();
    }
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
    if (this.disposed) return;
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
      // refreshLatest aborts the controller loadInitial was using, so its
      // finally skips clearing this. Without it a refresh that lands during a
      // branch switch leaves the store loading forever.
      this.loading = false;
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

  /**
   * Drops one optimistic transient. A submit the Worker rejected must not keep
   * its bubble: the draft returns to the composer, so a leftover ghost turn
   * would show the same text twice once the user retries.
   */
  removeTransient = (id: string): void => {
    const next = this.transient.filter((item) => item.id !== id);
    if (next.length === this.transient.length) return;
    this.transient = next;
    this.notify();
  };

  // ——— session change feed (phase C) ———

  openStream = (): void => {
    this.streamPaused = false;
    if (this.disposed || this.streamOpen || this.streamController) return;
    void this.openStreamInternal();
  };

  /**
   * Stops consuming the session change feed without discarding cached history.
   * The visible session reopens it on its next ensureLoaded; background sessions
   * stay resident but hold no socket, keeping the browser under the HTTP/1.1
   * per-origin connection budget.
   */
  pauseStream = (): void => {
    this.streamPaused = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    this.reconnectAttempt = 0;
    // Cancels the timer, not the intent: the reopened session still reads the
    // rows it was told about but never fetched (see ensureLoaded).
    this.windowRefresh.cancelPending();
    this.streamController?.abort();
    this.streamController = null;
    // A store with no socket cannot observe a run starting, finishing or an
    // approval resolving. Keeping the last snapshot would let a session that is
    // switched back paint a finished run as running, and let the run stream
    // reopen for a run that is already over. Drop it here; the `subscribed` frame
    // of the reopened stream restores it before any other frame.
    //
    // The cost is the mirror image of the bug: between reopening and that frame
    // the App reads the projection as "idle", so a run that is still going looks
    // finished for a moment. The streamed tail (transient) is kept deliberately —
    // it is what the user was reading — and a submit during that window is
    // refused by the Worker and restored to the composer.
    this.clearProjection();
    if (this.streamOpen) {
      this.streamOpen = false;
      this.notify();
    }
  };

  dispose = (): void => {
    this.disposed = true;
    this.requestController?.abort();
    this.streamController?.abort();
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    this.windowRefresh.replaceWindow();
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
    } catch {
      // A Worker generation change is not handled inside the store, deliberately.
      // WorkerGenerationGuard listens for WORKER_GENERATION_CHANGED_EVENT and
      // reloads the whole page, which rebuilds every cache row (canonical window,
      // checkpoints, transient projection) from the new Worker. A partial reset
      // here would be exactly the state that reload throws away, so generation
      // recovery lives in one place: the reload. Do not add an in-store reset.
    } finally {
      // A pause (or a newer stream) may already own the slot; the stale stream
      // must not clobber the current one's state or schedule a reconnect.
      if (this.streamController === controller) {
        this.streamController = null;
        this.streamOpen = false;
        this.notify();
        if (!this.disposed && !this.streamPaused) this.scheduleReconnect();
      }
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
        if (!this.disposed) this.windowRefresh.markDirty();
        break;
      }
      case "run_succeeded":
      case "run_failed":
      case "run_cancelled":
      case "run_interrupted":
      case "context_compaction_completed":
      case "run_context_compaction_completed":
        // A terminal frame is the boundary the window must be exact at, so it
        // takes the read the commit frames deferred.
        if (!this.disposed) this.windowRefresh.flush();
        break;
      default:
        // approval_requested/resolved, input_queued/injected: the snapshot
        // frame carries the authoritative projection.
        break;
    }
  }

  /**
   * Drops the pushed projection, not the window and not the streamed tail: the
   * history stays (it is not what went stale) and transient stays (it is what the
   * user was reading). Only the state the closed socket can no longer keep true
   * goes away.
   */
  private clearProjection(): void {
    if (this.activeRun === null && this.pendingApproval === null
      && this.queuedInputs.length === 0 && !this.delegationActive) return;
    this.activeRun = null;
    this.pendingApproval = null;
    this.queuedInputs = [];
    this.delegationActive = false;
    this.notify();
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
        // A compaction Run's instruction identity travels on the checkpoint: its
        // prompt is not a concatenation, so there is no prompt text to show.
        promptVersion: item.promptVersion,
        summaryContractDigest: item.summaryContractDigest,
      }));
    }
    this.notify();
  }

  private scheduleReconnect(): void {
    if (this.disposed || this.streamPaused || this.reconnectTimer) return;
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
    // The pending read belongs to the window that is being replaced, and
    // loadInitial reads the replacement in full.
    this.windowRefresh.replaceWindow();
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
