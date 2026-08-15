export const WORKER_GENERATION_CHANGED_EVENT = "ennote:worker-generation-changed";

interface APIEnvelope<T = unknown> {
  data?: T;
  error?: { message?: string };
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

export function workerAPIPath(path: string): string {
  return path.startsWith("/api/worker/") ? path : `/api/worker${path}`;
}

export async function apiResponse(path: string, init?: RequestInit): Promise<Response> {
  const requestGeneration = workerGeneration;
  const response = await fetch(workerAPIPath(path), init);
  acceptWorkerGeneration(response, requestGeneration);
  return response;
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await apiResponse(path, init);
  const body = await response.json().catch(() => ({})) as APIEnvelope<T>;
  if (!response.ok || body.error) {
    throw new Error(body.error?.message ?? `HTTP ${response.status}`);
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
  const nextInstanceId = response.headers.get("X-Ennote-Worker-Instance");
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
