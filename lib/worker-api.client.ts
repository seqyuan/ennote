export function workerAPIPath(path: string): string {
  return path.startsWith("/api/worker/") ? path : `/api/worker${path}`;
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(workerAPIPath(path), init);
  const body = await response.json().catch(() => ({}));
  if (!response.ok || body.error) {
    throw new Error(body.error?.message ?? `HTTP ${response.status}`);
  }
  return body.data as T;
}
