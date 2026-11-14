export const WORKER_GENERATION_CHANGED_EVENT = "ennote:worker-generation-changed";

interface APIEnvelope<T = unknown> {
  data?: T;
  error?: { code?: string; message?: string; retryable?: boolean };
}

export interface WorkerSSEEvent<T> {
  id?: string;
  event?: string;
  data: T;
}

let workerInstanceId: string | null = null;
let workerGeneration = 0;
const retiredWorkerInstances = new Set<string>();

export class WorkerGenerationChangedError extends Error {
  constructor() {
    super("Worker restarted; discarding data from the previous connection");
    this.name = "WorkerGenerationChangedError";
  }
}

export class WorkerUnreachableError extends Error {
  constructor() {
    super("The Worker is unreachable. Ennote is read-only until it returns.");
    this.name = "WorkerUnreachableError";
  }
}

/**
 * A Worker request that was answered with an error. `message` stays the whole
 * story for callers that only display it, but `status` and `code` let a caller
 * tell a digest conflict apart from a validation failure -- without them every
 * non-2xx looked the same and the editors reported a stale-file conflict for
 * ordinary 422 validation errors.
 */
export class WorkerRequestError extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = "WorkerRequestError";
    this.status = status;
    this.code = code;
  }
}

/** Whether a failed request was the "this file changed under you" case. */
export function isSourceDigestConflict(reason: unknown): boolean {
  return reason instanceof WorkerRequestError
    && (reason.status === 409 || reason.code === "source_digest_conflict");
}

/** Whether the last Worker exchange actually reached the process. */
export type WorkerReachability = "unknown" | "reachable" | "unreachable";

let workerReachability: WorkerReachability = "unknown";
const reachabilityListeners = new Set<() => void>();

export function getWorkerReachability(): WorkerReachability {
  return workerReachability;
}

export function subscribeWorkerReachability(listener: () => void): () => void {
  reachabilityListeners.add(listener);
  startReachabilityMonitor();
  return () => {
    reachabilityListeners.delete(listener);
    if (reachabilityListeners.size === 0) stopReachabilityMonitor();
  };
}

function setWorkerReachability(next: WorkerReachability): void {
  if (workerReachability === next) return;
  workerReachability = next;
  for (const listener of reachabilityListeners) listener();
}

// The Worker stamps this on every response it produces (internal/api/server.go),
// before authentication runs, so it is present on a rejection and on a degraded
// readiness answer alike. ennogate's own pages never carry it.
export const WORKER_INSTANCE_HEADER = "X-Ennote-Worker-Instance";

/**
 * Re-probe cadence. Fast while the Worker is unreachable, because that answer
 * decides when the App becomes writable again; slow while it answers, because
 * that probe only has to notice a death that fires no browser event. Twenty
 * seconds of one loopback GET is below noise for a local-first app.
 */
const UNREACHABLE_POLL_MS = 5_000;
const HEALTHY_POLL_MS = 20_000;

// The probe asks the cheapest question there is — is the process still there? —
// so it targets liveness, not readiness. Readiness pings the catalog and usage
// databases, which share one connection: a Worker rebuilding a projection can
// block that ping for seconds, and a busy Worker must not be reported as gone
// exactly while it is doing the work. A degraded projection is a mutation error,
// not an absent Worker.
const REACHABILITY_PROBE_PATH = "/v1/health/live";

// A loopback process answers in single-digit milliseconds. This bound exists so
// a wedged Worker cannot leave the probe pending forever and keep the read-only
// verdict from ever landing.
const REACHABILITY_PROBE_TIMEOUT_MS = 3000;

function probeTimeoutSignal(): AbortSignal | undefined {
  // Where the static is missing, degrade to an unbounded probe instead of
  // letting the missing method throw inside the try, which would report the
  // Worker as gone on every attempt and pin the App to read-only forever.
  if (typeof AbortSignal.timeout !== "function") return undefined;
  return AbortSignal.timeout(REACHABILITY_PROBE_TIMEOUT_MS);
}

/**
 * probeWorkerReachability asks the Worker, through the gate, whether it answers,
 * and resolves to the verdict it wrote. The browser's online flag is only a hint
 * to call this: Ennote's authority is a loopback process, and navigator.onLine
 * reports the state of the internet, which is a different question. A machine
 * with no network can still serve and mutate its own Worker.
 *
 * The verdict is deliberately asymmetric. "Unreachable" makes the App read-only,
 * so it is reserved for evidence of absence: a refused connection, or the gate
 * answering 502/504 because it could not reach the process. WORKER_INSTANCE_HEADER
 * confirms presence — the Worker stamps it on every response, before
 * authentication — but its absence proves nothing on its own, because a response
 * can also come from something standing in for the Worker. Everything else is
 * "unknown", which keeps the App writable and lets the user's next request
 * report what is actually wrong.
 */
export function probeWorkerReachability(): Promise<WorkerReachability> {
  // The mount probe, the poll and an online/offline event can overlap. Sharing
  // one exchange keeps last-write-wins from letting a stale failure overwrite a
  // newer success.
  probeInFlight ??= runReachabilityProbe().finally(() => { probeInFlight = null; });
  return probeInFlight;
}

let probeInFlight: Promise<WorkerReachability> | null = null;

// A verdict of "unreachable" makes the App read-only, so it is reserved for
// positive evidence that the process is gone. "We could not tell" is not that
// evidence: an inconclusive answer must never override one either way, and in
// particular must never resurrect an absence that was already established.
function concludeInconclusive(): WorkerReachability {
  if (workerReachability !== "unreachable") setWorkerReachability("unknown");
  return "unknown";
}

function concludeUnreachable(): WorkerReachability {
  setWorkerReachability("unreachable");
  return "unreachable";
}

function isProbeTimeout(error: unknown): boolean {
  const name = (error as { name?: unknown } | null)?.name;
  return name === "TimeoutError" || name === "AbortError";
}

async function runReachabilityProbe(): Promise<WorkerReachability> {
  let response: Response;
  try {
    response = await fetch(workerAPIPath(REACHABILITY_PROBE_PATH), {
      method: "GET",
      cache: "no-store",
      signal: probeTimeoutSignal(),
    });
  } catch (error) {
    // A timeout says the measurement failed, not that the process is absent: the
    // browser queues a request while long-lived streams hold its per-origin
    // sockets, so a healthy Worker can miss a three second window under load.
    // Only a refused or broken connection proves the socket is gone.
    return isProbeTimeout(error) ? concludeInconclusive() : concludeUnreachable();
  }
  if (response.headers.has(WORKER_INSTANCE_HEADER)) {
    setWorkerReachability("reachable");
    return "reachable";
  }
  // No instance header. The gate could not reach the process and answered 502
  // (or 504) in its place — that is the Worker being absent, and it is the only
  // proxy-level status ennogate produces.
  if (response.status === 502 || response.status === 504) return concludeUnreachable();
  // The gate's auth boundary. ennogate keeps its sessions in memory, so
  // restarting it answers 401 to a page that was logged in a second ago; that
  // says nothing about the Worker. The proxy overwrites Authorization, so the
  // only 401/428 a browser can see is the gate's.
  if (response.status === 401 || response.status === 428) return concludeInconclusive();
  // Anything else came from the App's own origin and is not evidence about the
  // Worker at all — including a response from something standing in for it, such
  // as a test harness. Refusing to write on this is how a healthy App strands
  // itself; the request the user makes next reports what is actually wrong.
  return concludeInconclusive();
}

// The probe loop belongs to the state, not to a component. workerReachability is
// module-level, so a per-component loop would multiply the cadence the moment a
// second consumer subscribed. The first listener starts the loop and the last one
// stops it; a probe already in flight at that moment still finishes and writes
// the state it went looking for, which is the answer, not a leak.
let reachabilityMonitor: { timer: ReturnType<typeof setTimeout> | undefined } | null = null;

function startReachabilityMonitor(): void {
  if (reachabilityMonitor) return;
  const monitor: { timer: ReturnType<typeof setTimeout> | undefined } = { timer: undefined };
  reachabilityMonitor = monitor;
  const tick = async () => {
    // The reschedule has to survive the probe. A rejected probe would otherwise
    // throw out of this async function and end the loop for good — exactly the
    // permanent stall the probe exists to prevent.
    try {
      await probeWorkerReachability();
    } catch {
      // A subscriber threw while being notified. The probe already wrote its
      // verdict; the next tick asks again.
    } finally {
      if (reachabilityMonitor === monitor) {
        const delay = workerReachability === "unreachable" ? UNREACHABLE_POLL_MS : HEALTHY_POLL_MS;
        monitor.timer = setTimeout(() => { void tick(); }, delay);
      }
    }
  };
  void tick();
}

function stopReachabilityMonitor(): void {
  if (!reachabilityMonitor) return;
  clearTimeout(reachabilityMonitor.timer);
  reachabilityMonitor = null;
}

// Offline App state is read-only (AGENTS.md): once the Worker is known to be
// unreachable, a mutation is refused before it reaches the network and is never
// queued for replay, so a refactor cannot turn the missing queue into an
// implicit one. The signal is the Worker's reachability, not navigator.onLine:
// reads and writes both stay open while the local process answers.
//
// Reachability is written only by probeWorkerReachability, never inferred from an
// individual request. A single failed GET does not prove the process is gone —
// it is just as likely to be one unavailable resource — and treating it as proof
// would refuse unrelated writes. Asking the Worker is the only evidence that
// answers the question.
const READ_ONLY_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

function assertWorkerReachableMutation(init?: RequestInit): void {
  if (workerReachability !== "unreachable") return;
  if (READ_ONLY_METHODS.has((init?.method ?? "GET").toUpperCase())) return;
  throw new WorkerUnreachableError();
}

export function workerAPIPath(path: string): string {
  return path.startsWith("/api/worker/") ? path : `/api/worker${path}`;
}

export async function apiResponse(path: string, init?: RequestInit): Promise<Response> {
  assertWorkerReachableMutation(init);
  const requestGeneration = workerGeneration;
  const response = await fetch(workerAPIPath(path), init);
  acceptWorkerGeneration(response, requestGeneration);
  return response;
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await apiResponse(path, init);
  const body = await response.json().catch(() => ({})) as APIEnvelope<T>;
  if (!response.ok || body.error) {
    throw new WorkerRequestError(
      body.error?.message ?? `HTTP ${response.status}`,
      response.status,
      body.error?.code,
    );
  }
  return body.data as T;
}

export async function apiText(path: string, init?: RequestInit): Promise<string> {
  const response = await apiResponse(path, init);
  if (!response.ok) {
    throw new Error(await responseErrorMessage(response));
  }
  return response.text();
}

export async function apiEventStream<T>(
  path: string,
  onEvent: (event: WorkerSSEEvent<T>) => void,
  init?: RequestInit,
): Promise<void> {
  const headers = new Headers(init?.headers);
  if (!headers.has("Accept")) headers.set("Accept", "text/event-stream");
  const response = await apiResponse(path, { ...init, headers });
  if (!response.ok) {
    throw new Error(await responseErrorMessage(response));
  }
  const reader = response.body?.getReader();
  if (!reader) throw new Error("Worker event stream has no response body");

  const decoder = new TextDecoder();
  let buffer = "";
  let currentId: string | undefined;
  let currentEvent: string | undefined;
  let dataLines: string[] = [];

  const processLine = (rawLine: string) => {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (line.startsWith("id:")) {
      currentId = line.slice(3).trim();
    } else if (line.startsWith("event:")) {
      currentEvent = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).trimStart());
    } else if (line === "") {
      if (dataLines.length > 0) {
        try {
          onEvent({ id: currentId, event: currentEvent, data: JSON.parse(dataLines.join("\n")) as T });
        } catch {
          // Malformed frames do not advance application state; durable replay remains authoritative.
        }
      }
      currentId = undefined;
      currentEvent = undefined;
      dataLines = [];
    }
  };

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";
      for (const line of lines) processLine(line);
    }
    buffer += decoder.decode();
    if (buffer) processLine(buffer);
    if (dataLines.length > 0) processLine("");
  } finally {
    reader.releaseLock();
  }
}

function acceptWorkerGeneration(response: Response, requestGeneration: number): void {
  const nextInstanceId = response.headers.get(WORKER_INSTANCE_HEADER);
  if (nextInstanceId) {
    if (workerInstanceId === null) {
      workerInstanceId = nextInstanceId;
    } else if (workerInstanceId !== nextInstanceId) {
      if (!retiredWorkerInstances.has(nextInstanceId)) {
        retiredWorkerInstances.add(workerInstanceId);
        workerInstanceId = nextInstanceId;
        workerGeneration += 1;
        if (typeof window !== "undefined") {
          window.dispatchEvent(new CustomEvent(WORKER_GENERATION_CHANGED_EVENT, {
            detail: { instanceId: nextInstanceId, generation: workerGeneration },
          }));
        }
      }
      throw new WorkerGenerationChangedError();
    }
  }
  if (requestGeneration !== workerGeneration) throw new WorkerGenerationChangedError();
}

async function responseErrorMessage(response: Response): Promise<string> {
  const body = await response.json().catch(() => ({})) as APIEnvelope;
  return body.error?.message ?? `HTTP ${response.status}`;
}
