import { afterEach, describe, expect, it, vi } from "vitest";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
});

describe("worker API response formats", () => {
  it("reads raw text responses without treating them as JSON envelopes", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("schemaVersion: 1\nid: review\n", {
      status: 200,
      headers: { "Content-Type": "application/x-yaml" },
    })));
    const { apiText } = await import("@/lib/worker-api.client");

    await expect(apiText("/v1/agent-flows/graph/export?source=draft"))
      .resolves.toContain("id: review");
  });

  it("parses durable SSE frames and preserves live frame names", async () => {
    const encoder = new TextEncoder();
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('id: 1\ndata: {"type":"flow_started"}\n\nevent: live\nda'));
        controller.enqueue(encoder.encode('ta: {"type":"text_delta"}\n\nid: 2\ndata: {"type":"run_succeeded"}\n\n'));
        controller.close();
      },
    });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(stream, {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    })));
    const { apiEventStream } = await import("@/lib/worker-api.client");
    const events: Array<{ id?: string; event?: string; data: { type: string } }> = [];

    await apiEventStream<{ type: string }>("/v1/runs/run/events", event => events.push(event));

    expect(events).toEqual([
      { id: "1", event: undefined, data: { type: "flow_started" } },
      { id: undefined, event: "live", data: { type: "text_delta" } },
      { id: "2", event: undefined, data: { type: "run_succeeded" } },
    ]);
  });

  it("rejects responses from a replaced or retired Worker instance", async () => {
    const instances = ["worker-1", "worker-2", "worker-1", "worker-2"];
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: { ok: true } }), {
      status: 200,
      headers: { "Content-Type": "application/json", "X-Ennote-Worker-Instance": instances.shift()! },
    })));
    const { apiFetch, WorkerGenerationChangedError } = await import("@/lib/worker-api.client");

    await expect(apiFetch("/v1/projects")).resolves.toEqual({ ok: true });
    await expect(apiFetch("/v1/projects")).rejects.toBeInstanceOf(WorkerGenerationChangedError);
    await expect(apiFetch("/v1/projects")).rejects.toBeInstanceOf(WorkerGenerationChangedError);
    await expect(apiFetch("/v1/projects")).resolves.toEqual({ ok: true });
  });

  it("refuses a mutation once a probe finds the Worker unreachable", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    }));
    const { apiFetch, WorkerUnreachableError, probeWorkerReachability } = await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe(false);

    // Offline App state is read-only: the mutation never leaves the client and is
    // never queued for replay.
    const fetchSpy = vi.mocked(fetch);
    const callsBefore = fetchSpy.mock.calls.length;
    await expect(apiFetch("/v1/projects", { method: "POST", body: "{}" }))
      .rejects.toBeInstanceOf(WorkerUnreachableError);
    expect(fetchSpy).toHaveBeenCalledTimes(callsBefore);
  });

  it("keeps the write path open while the Worker answers, even with the browser offline", async () => {
    // A local-first machine with no network still owns a live loopback Worker.
    // navigator.onLine reports the internet, which is a different question, so
    // it must not be what refuses a mutation.
    vi.stubGlobal("navigator", { onLine: false });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: { ok: true } }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));
    const { apiFetch, probeWorkerReachability } = await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe(true);
    await expect(apiFetch("/v1/projects", { method: "POST", body: "{}" })).resolves.toEqual({ ok: true });
  });

  it("does not treat an HTTP error status as unreachable", async () => {
    // 500, 404 and 428 all mean something answered the socket.
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { message: "nope" } }), {
      status: 500,
      headers: { "Content-Type": "application/json" },
    })));
    const { apiFetch, probeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe(true);
    await expect(apiFetch("/v1/projects")).rejects.toThrow("nope");
    expect(getWorkerReachability()).toBe("reachable");
  });

  it("reports a recovered Worker after a failed probe", async () => {
    const { probeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Failed to fetch"); }));
    await expect(probeWorkerReachability()).resolves.toBe(false);
    expect(getWorkerReachability()).toBe("unreachable");

    // Any HTTP answer proves the process is up, including one that is still
    // waiting for first-run password setup.
    vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 428 })));
    await expect(probeWorkerReachability()).resolves.toBe(true);
    expect(getWorkerReachability()).toBe("reachable");
  });
});
