import { describe, expect, it, vi } from "vitest";

const { apiResponse } = vi.hoisted(() => ({ apiResponse: vi.fn() }));

vi.mock("@/lib/worker-api.client", () => ({
  apiFetch: vi.fn(),
  apiResponse,
  WorkerGenerationChangedError: class WorkerGenerationChangedError extends Error {},
}));

import { newAgentStreamState, streamAgentEvents } from "@/hooks/useAgentSession";

function sse(frames: string[]): Response {
  return new Response(frames.join(""), { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

function textFrame(id: number, text: string): string {
  return `id: ${id}\nevent: live\ndata: ${JSON.stringify({ type: "text_delta", payload: { iteration: 1, text } })}\n\n`;
}

const approvals = () => ({
  requested: vi.fn(), resolved: vi.fn(), delegated: vi.fn(),
  followUpConsumed: vi.fn(), inputCancelled: vi.fn(),
});

async function read(runId: string, response: Response, cursor: { value?: string }, state = newAgentStreamState(),
  upsertMessage = vi.fn(), signal = new AbortController().signal) {
  apiResponse.mockResolvedValueOnce(response);
  const terminal = await streamAgentEvents(
    runId, upsertMessage, vi.fn(), vi.fn(), vi.fn(), signal, approvals(), state, cursor,
  );
  return { terminal, state, upsertMessage };
}

describe("run event stream resume", () => {
  it("records the server event id and sends Last-Event-ID on reconnect without losing text", async () => {
    apiResponse.mockReset();
    const cursor: { value?: string } = {};
    const state = newAgentStreamState();

    const first = await read("run-1", sse([textFrame(7, "hello")]), cursor, state);
    expect(first.terminal).toBe(false);
    expect(cursor.value).toBe("7");
    expect(state.assistants.get(1)?.text).toBe("hello");

    const second = await read("run-1", sse([
      textFrame(8, " world"),
      `id: 9\nevent: durable\ndata: ${JSON.stringify({ type: "run_succeeded", payload: {} })}\n\n`,
    ]), cursor, state, first.upsertMessage);
    expect(second.terminal).toBe(true);
    expect(cursor.value).toBe("9");
    expect(state.assistants.get(1)?.text).toBe("hello world");

    const headers = new Headers(apiResponse.mock.calls[1]?.[1]?.headers as HeadersInit);
    expect(headers.get("Last-Event-ID")).toBe("7");
    expect(first.upsertMessage).toHaveBeenLastCalledWith(
      expect.objectContaining({ role: "assistant", text: "hello world" }),
    );
  });
});
