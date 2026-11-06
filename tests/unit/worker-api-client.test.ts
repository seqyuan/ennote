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

    await expect(probeWorkerReachability()).resolves.toBe("unreachable");

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
      headers: { "Content-Type": "application/json", "X-Ennote-Worker-Instance": "worker-1" },
    })));
    const { apiFetch, probeWorkerReachability } = await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe("reachable");
    await expect(apiFetch("/v1/projects", { method: "POST", body: "{}" })).resolves.toEqual({ ok: true });
  });

  it("counts a degraded Worker as reachable whatever status it answers with", async () => {
    // The Worker reports an unavailable projection with its own 503. It answered,
    // so the App keeps its write path: only a missing Worker makes it read-only.
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { message: "projection_unavailable" } }), {
      status: 503,
      headers: { "Content-Type": "application/json", "X-Ennote-Worker-Instance": "worker-1" },
    })));
    const { probeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe("reachable");
    expect(getWorkerReachability()).toBe("reachable");
  });

  it("treats a proxy error page as unreachable even though the socket answered", async () => {
    // ennogate's reverse proxy answers 502 when it cannot reach the Worker. The
    // fetch resolves, so nothing throws, and only the missing instance header
    // says the Worker itself never spoke. This is the case the offline contract
    // has to survive: gate alive, Worker gone.
    vi.stubGlobal("fetch", vi.fn(async () => new Response("bad gateway", { status: 502 })));
    const { apiFetch, WorkerUnreachableError, probeWorkerReachability, getWorkerReachability } =
      await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe("unreachable");
    expect(getWorkerReachability()).toBe("unreachable");

    const fetchSpy = vi.mocked(fetch);
    const callsBefore = fetchSpy.mock.calls.length;
    await expect(apiFetch("/v1/projects", { method: "POST", body: "{}" }))
      .rejects.toBeInstanceOf(WorkerUnreachableError);
    expect(fetchSpy).toHaveBeenCalledTimes(callsBefore);
  });

  it("bounds the probe so a hung request cannot keep the verdict from landing", async () => {
    // Only the wiring is pinned here: the 3s bound itself comes from
    // AbortSignal.timeout, whose native timer fake timers cannot advance, so a
    // test that waits it out would cost three real seconds.
    const fetchSpy = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(null, {
      status: 200,
      headers: { "X-Ennote-Worker-Instance": "worker-1" },
    }));
    vi.stubGlobal("fetch", fetchSpy);
    const { probeWorkerReachability } = await import("@/lib/worker-api.client");

    await probeWorkerReachability();

    expect(fetchSpy.mock.calls[0]?.[1]?.signal).toBeInstanceOf(AbortSignal);
  });

  it("shares one exchange between overlapping probes", async () => {
    // The mount probe, the poll and an online event can overlap. Sharing the
    // exchange keeps a stale failure from overwriting a newer success.
    const fetchSpy = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    vi.stubGlobal("fetch", fetchSpy);
    const { probeWorkerReachability } = await import("@/lib/worker-api.client");

    const settled = await Promise.all([
      probeWorkerReachability(),
      probeWorkerReachability(),
      probeWorkerReachability(),
    ]);

    expect(settled).toEqual(["unreachable", "unreachable", "unreachable"]);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
  });

  it("does not claim the Worker is gone when the gate rejects the session", async () => {
    // ennogate keeps its sessions in memory, so restarting the gate answers 401
    // to a page that was logged in a second ago. That response carries no
    // Worker instance header, but it is not evidence about the Worker: the App
    // must not show a banner that lies nor unplug the write path.
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { code: "unauthorized" } }), {
      status: 401,
      headers: { "Content-Type": "application/json" },
    })));
    const { apiFetch, probeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe("unknown");
    expect(getWorkerReachability()).toBe("unknown");

    // The write path stays open; the request itself reports the real problem.
    await expect(apiFetch("/v1/projects", { method: "POST", body: "{}" })).rejects.toThrow("HTTP 401");
  });

  it("does not declare the Worker gone on the strength of a missing header", async () => {
    // A response from the App's own origin is not evidence either way: a test
    // harness, a future proxy, or anything else can answer for the Worker. Only
    // an explicit "I could not reach it" counts.
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: { status: "live" } }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));
    const { probeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe("unknown");
    expect(getWorkerReachability()).toBe("unknown");
  });

  it("treats a probe timeout as inconclusive rather than as an absent Worker", async () => {
    // The browser queues a request while long-lived streams hold its per-origin
    // sockets, so a healthy Worker can miss the timeout window under load. A
    // read-only App would then block writes against a process that is working.
    const timeout = Object.assign(new Error("signal timed out"), { name: "TimeoutError" });
    vi.stubGlobal("fetch", vi.fn(async () => { throw timeout; }));
    const { apiFetch, probeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    await expect(probeWorkerReachability()).resolves.toBe("unknown");
    expect(getWorkerReachability()).toBe("unknown");

    // The write path stays open.
    await expect(apiFetch("/v1/projects", { method: "POST", body: "{}" })).rejects.toThrow("timed out");
  });

  it("but a timeout does not clear an established unreachable verdict", async () => {
    const { probeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    vi.stubGlobal("fetch", vi.fn(async () => new Response("bad gateway", { status: 502 })));
    await expect(probeWorkerReachability()).resolves.toBe("unreachable");

    const timeout = Object.assign(new Error("signal timed out"), { name: "TimeoutError" });
    vi.stubGlobal("fetch", vi.fn(async () => { throw timeout; }));
    await expect(probeWorkerReachability()).resolves.toBe("unknown");
    expect(getWorkerReachability()).toBe("unreachable");
  });

  it("does not let a gate rejection resurrect an established unreachable verdict", async () => {
    // The Worker is gone, so the App is read-only. Then the gate restarts: its
    // in-memory session store is empty, so the probe now gets a headerless 401.
    // That answer carries no information about the Worker and must not put the
    // App back into a writable state.
    const { apiFetch, WorkerUnreachableError, probeWorkerReachability, getWorkerReachability } =
      await import("@/lib/worker-api.client");

    vi.stubGlobal("fetch", vi.fn(async () => new Response("bad gateway", { status: 502 })));
    await expect(probeWorkerReachability()).resolves.toBe("unreachable");

    vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 401 })));
    await expect(probeWorkerReachability()).resolves.toBe("unknown");
    expect(getWorkerReachability()).toBe("unreachable");
    await expect(apiFetch("/v1/projects", { method: "POST", body: "{}" }))
      .rejects.toBeInstanceOf(WorkerUnreachableError);
  });

  it("lands a verdict even where AbortSignal.timeout is missing", async () => {
    // A browser without the static must not turn into a probe that throws on
    // every attempt: that would pin the App to read-only permanently.
    const original = AbortSignal.timeout;
    // @ts-expect-error -- simulate a browser that predates the static
    AbortSignal.timeout = undefined;
    try {
      const fetchSpy = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(null, {
        status: 200,
        headers: { "X-Ennote-Worker-Instance": "worker-1" },
      }));
      vi.stubGlobal("fetch", fetchSpy);
      const { probeWorkerReachability } = await import("@/lib/worker-api.client");

      await expect(probeWorkerReachability()).resolves.toBe("reachable");
      expect(fetchSpy.mock.calls[0]?.[1]?.signal).toBeUndefined();
    } finally {
      AbortSignal.timeout = original;
    }
  });

  it("reports a recovered Worker after a failed probe", async () => {
    const { probeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Failed to fetch"); }));
    await expect(probeWorkerReachability()).resolves.toBe("unreachable");
    expect(getWorkerReachability()).toBe("unreachable");

    // The same Worker answering again proves the process is up whatever it says:
    // here it has come back with an unavailable projection.
    vi.stubGlobal("fetch", vi.fn(async () => new Response(null, {
      status: 503,
      headers: { "X-Ennote-Worker-Instance": "worker-1" },
    })));
    await expect(probeWorkerReachability()).resolves.toBe("reachable");
    expect(getWorkerReachability()).toBe("reachable");
  });
});

describe("worker reachability monitor", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("probes on subscribe, then on a cadence that follows the answer", async () => {
    vi.useFakeTimers();
    const fetchSpy = vi.fn(async () => new Response(null, {
      status: 200,
      headers: { "X-Ennote-Worker-Instance": "worker-1" },
    }));
    vi.stubGlobal("fetch", fetchSpy);
    const { subscribeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    const unsubscribe = subscribeWorkerReachability(() => {});
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(getWorkerReachability()).toBe("reachable");

    // Healthy: the next probe is on the slow cadence.
    await vi.advanceTimersByTimeAsync(20_000);
    expect(fetchSpy).toHaveBeenCalledTimes(2);

    // The last subscriber leaving stops the loop; nothing keeps probing a state
    // no component is watching.
    unsubscribe();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(fetchSpy).toHaveBeenCalledTimes(2);
  });

  it("speeds up while the Worker is gone, so recovery is noticed quickly", async () => {
    vi.useFakeTimers();
    const fetchSpy = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    vi.stubGlobal("fetch", fetchSpy);
    const { subscribeWorkerReachability, getWorkerReachability } = await import("@/lib/worker-api.client");

    const unsubscribe = subscribeWorkerReachability(() => {});
    await vi.advanceTimersByTimeAsync(0);
    expect(getWorkerReachability()).toBe("unreachable");

    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchSpy).toHaveBeenCalledTimes(2);

    unsubscribe();
    await vi.advanceTimersByTimeAsync(60_000);
  });

  it("survives a subscriber that throws while being notified", async () => {
    // A throwing listener must not end the loop: a dead monitor is the exact
    // failure the probe exists to prevent.
    vi.useFakeTimers();
    const fetchSpy = vi.fn(async () => new Response(null, {
      status: 200,
      headers: { "X-Ennote-Worker-Instance": "worker-1" },
    }));
    vi.stubGlobal("fetch", fetchSpy);
    const { subscribeWorkerReachability } = await import("@/lib/worker-api.client");

    const unsubscribe = subscribeWorkerReachability(() => {
      throw new Error("listener exploded");
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchSpy).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(20_000);
    expect(fetchSpy).toHaveBeenCalledTimes(2);

    unsubscribe();
    await vi.advanceTimersByTimeAsync(60_000);
  });
});
