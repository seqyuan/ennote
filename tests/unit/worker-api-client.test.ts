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
});
