import "server-only";

import type { components } from "@/lib/worker-api.gen";

const WORKER_URL = process.env.ENNOTE_WORKER_URL ?? "http://127.0.0.1:0";
const WORKER_TOKEN = process.env.ENNOTE_WORKER_TOKEN ?? "";

type Project = components["schemas"]["Project"];
type ProjectWorkspace = components["schemas"]["ProjectWorkspace"];
type Session = components["schemas"]["Session"];
type SessionMessagePage = components["schemas"]["SessionMessagePage"];
type AgentRun = components["schemas"]["AgentRun"];
type TurnSubmission = components["schemas"]["TurnSubmission"];
type QueuedInput = components["schemas"]["QueuedInput"];
type ContextCompaction = components["schemas"]["ContextCompaction"];
type CompactionSubmission = components["schemas"]["CompactionSubmission"];

export interface RunEvent {
  eventId: number;
  runId: string;
  seq: number;
  type: string;
  payload: unknown;
  createdAt: string;
  __eventId?: string;
}

function authHeaders(): Record<string, string> {
  return {
    Authorization: `Bearer ${WORKER_TOKEN}`,
    "Content-Type": "application/json",
  };
}

function baseURL(): string {
  return WORKER_URL.replace(/\/$/, "");
}

interface WorkerResponse<T = unknown> {
  data?: T;
  error?: { code: string; message: string; requestId: string; retryable: boolean };
}

export async function workerFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${baseURL()}${path}`, {
    ...init,
    headers: { ...authHeaders(), ...init?.headers },
  });
  const body = (await res.json().catch(() => ({}))) as WorkerResponse<T>;
  if (!res.ok || body.error) {
    throw new WorkerError(
      body.error?.code ?? "unknown",
      body.error?.message ?? `HTTP ${res.status}`,
      body.error?.requestId,
    );
  }
  return body.data as T;
}

export async function workerEventStream(
  path: string,
  onEvent: (event: RunEvent) => void,
  signal?: AbortSignal,
  lastEventId?: string,
): Promise<void> {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${WORKER_TOKEN}`,
    Accept: "text/event-stream",
  };
  if (lastEventId) headers["Last-Event-ID"] = lastEventId;
  const res = await fetch(`${baseURL()}${path}`, { headers, signal });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as WorkerResponse;
    throw new WorkerError(
      body.error?.code ?? "stream_error",
      body.error?.message ?? `HTTP ${res.status}`,
      body.error?.requestId,
    );
  }
  const reader = res.body?.getReader();
  if (!reader) throw new WorkerError("no_body", "No response body");
  const decoder = new TextDecoder();
  let buffer = "";
  let currentEventId: string | undefined;
  let dataLines: string[] = [];

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";
      for (const rawLine of lines) {
        const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
        if (line.startsWith("id:")) {
          currentEventId = line.slice(3).trim();
        } else if (line.startsWith("data:")) {
          dataLines.push(line.slice(5).trimStart());
        } else if (line === "" && dataLines.length > 0) {
          try {
            const event = JSON.parse(dataLines.join("\n")) as RunEvent;
            onEvent({ ...event, __eventId: currentEventId });
          } catch {
            // A malformed event is ignored; reconnect replay remains authoritative.
          }
          dataLines = [];
        }
      }
    }
  } finally {
    reader.releaseLock();
  }
}

export class WorkerError extends Error {
  constructor(
    public code: string,
    message: string,
    public requestId?: string,
  ) {
    super(message);
    this.name = "WorkerError";
  }
}

const id = encodeURIComponent;

export const worker = {
  projects: {
    list: () => workerFetch<Project[]>("/v1/projects"),
    create: (body: { name: string; description?: string; hostPath: string }) =>
      workerFetch<{ project: Project; workspace: ProjectWorkspace }>("/v1/projects", {
        method: "POST",
        body: JSON.stringify(body),
      }),
  },
  sessions: {
    list: (projectId: string) => workerFetch<Session[]>(`/v1/projects/${id(projectId)}/sessions`),
    create: (projectId: string, body: { title?: string; compactionPolicyProfileId?: string }) =>
      workerFetch<Session>(`/v1/projects/${id(projectId)}/sessions`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
    get: (sessionId: string) => workerFetch<Session>(`/v1/sessions/${id(sessionId)}`),
    messages: (sessionId: string, options?: { limit?: number; before?: string }) => {
      const query = new URLSearchParams();
      if (options?.limit) query.set("limit", String(options.limit));
      if (options?.before) query.set("before", options.before);
      const suffix = query.size > 0 ? `?${query.toString()}` : "";
      return workerFetch<SessionMessagePage>(`/v1/sessions/${id(sessionId)}/messages${suffix}`);
    },
    compact: (sessionId: string, body: { baseMessageId: string; instructions?: string; clientRequestId?: string }, idempotencyKey?: string) =>
      workerFetch<CompactionSubmission>(`/v1/sessions/${id(sessionId)}/compactions`, {
        method: "POST",
        headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
        body: JSON.stringify(body),
      }),
    compactions: (sessionId: string) =>
      workerFetch<ContextCompaction[]>(`/v1/sessions/${id(sessionId)}/compactions`),
    submitTurn: (
      sessionId: string,
      body: { text: string; baseMessageId?: string; config?: Record<string, unknown> },
      idempotencyKey: string,
    ) =>
      workerFetch<TurnSubmission>(`/v1/sessions/${id(sessionId)}/turns`, {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify(body),
      }),
  },
  runs: {
    get: (runId: string) => workerFetch<AgentRun>(`/v1/runs/${id(runId)}`),
    cancel: (runId: string) =>
      workerFetch<AgentRun>(`/v1/runs/${id(runId)}/cancel`, { method: "POST" }),
    queueInput: (
      runId: string,
      body: { kind: "steer" | "follow_up"; text: string; clientRequestId?: string },
      idempotencyKey?: string,
    ) =>
      workerFetch<QueuedInput>(`/v1/runs/${id(runId)}/inputs`, {
        method: "POST",
        headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : undefined,
        body: JSON.stringify(body),
      }),
    events: (runId: string, onEvent: (event: RunEvent) => void, signal?: AbortSignal, cursor?: string) =>
      workerEventStream(`/v1/runs/${id(runId)}/events`, onEvent, signal, cursor),
  },
  health: {
    live: () => workerFetch<{ status: "live" }>("/v1/health/live"),
    ready: () => workerFetch<{ status: "ready"; sandbox: string; degraded: boolean }>("/v1/health/ready"),
  },
};
