import { describe, expect, it, vi } from "vitest";

const { apiResponse } = vi.hoisted(() => ({ apiResponse: vi.fn() }));

vi.mock("@/lib/worker-api.client", () => ({
  apiFetch: vi.fn(),
  apiResponse,
  WorkerGenerationChangedError: class WorkerGenerationChangedError extends Error {},
}));

import { newAgentStreamState, streamAgentEvents, type AgentStreamState } from "@/hooks/useAgentSession";
import type { TurnMessage } from "@/lib/chat-messages";

const approvals = () => ({
  requested: vi.fn(), resolved: vi.fn(), delegated: vi.fn(),
  followUpConsumed: vi.fn(), inputCancelled: vi.fn(),
});

/** A live frame, the way the run stream carries transient deltas. */
function live(id: number, type: string, payload: Record<string, unknown>): string {
  return `id: ${id}\nevent: live\ndata: ${JSON.stringify({ type, payload })}\n\n`;
}

/** A durable frame: no `event:` line, which is what makes it non-live. */
function durable(id: number, type: string, payload: Record<string, unknown>): string {
  return `id: ${id}\ndata: ${JSON.stringify({ type, payload })}\n\n`;
}

function stream(frames: string[]): Response {
  return new Response(frames.join(""), { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

async function read(frames: string[], state: AgentStreamState = newAgentStreamState()) {
  const upsertMessage = vi.fn<(message: TurnMessage) => void>();
  apiResponse.mockResolvedValueOnce(stream(frames));
  await streamAgentEvents(
    "run-1", upsertMessage, vi.fn(), vi.fn(), vi.fn(),
    new AbortController().signal, approvals(), state, {},
  );
  return { state, upsertMessage };
}

function toolMessages(upsertMessage: ReturnType<typeof vi.fn<(message: TurnMessage) => void>>) {
  return (upsertMessage.mock.calls as [TurnMessage][]).map(([message]) => message).filter((message) => message.kind === "tool");
}

describe("live tool output", () => {
  it("renders a delta on the card its tool call id names", async () => {
    // The payload carries the identity the durable tool_call_* events use. Without
    // it every delta in the run lands on the same fallback card.
    const { upsertMessage } = await read([
      live(1, "tool_output_delta", { toolCallId: "call-1", toolName: "bash", stream: "stdout", text: "hello" }),
      live(2, "tool_output_delta", { toolCallId: "call-1", toolName: "bash", stream: "stdout", text: " world" }),
    ]);

    const [first, second] = toolMessages(upsertMessage);
    expect(first).toMatchObject({ id: "run-1-tool-call-1", toolCallId: "call-1", toolName: "bash", toolState: "running" });
    expect(first.text).toBe("hello");
    expect(second.text).toBe("hello world");
  });

  it("keeps stderr apart from stdout", async () => {
    const { upsertMessage } = await read([
      live(1, "tool_output_delta", { toolCallId: "call-1", stream: "stdout", text: "out" }),
      live(2, "tool_output_delta", { toolCallId: "call-1", stream: "stderr", text: "bad" }),
    ]);

    expect(toolMessages(upsertMessage).at(-1)?.text).toBe("out[stderr] bad");
  });

  it("ignores output for a call whose durable result already arrived", async () => {
    // The run stream writes the durable frames it read before the live deltas it
    // buffered in the same pass, so a delta for a finished call can arrive after
    // its result. Rendering it would flip the card back to running and overwrite
    // the result the user just saw.
    const { upsertMessage, state } = await read([
      live(1, "tool_call_started", { toolCallId: "call-1", toolName: "bash" }),
      durable(2, "tool_call_completed", { toolCallId: "call-1", toolName: "bash", content: "done", isError: false }),
      live(3, "tool_output_delta", { toolCallId: "call-1", toolName: "bash", stream: "stdout", text: "late" }),
    ]);

    expect(state.finishedTools.has("call-1")).toBe(true);
    const rendered = toolMessages(upsertMessage);
    expect(rendered.at(-1)).toMatchObject({ toolState: "completed", text: "done" });
    expect(rendered.some((message) => message.toolState === "running" && message.text === "late")).toBe(false);
  });

  it("treats a failed call as finished too", async () => {
    const { state } = await read([
      durable(1, "tool_call_failed", { toolCallId: "call-1", toolName: "bash", reason: "boom" }),
      live(2, "tool_output_delta", { toolCallId: "call-1", stream: "stdout", text: "late" }),
    ]);

    expect(state.finishedTools.has("call-1")).toBe(true);
    expect(state.toolOutputs.size).toBe(0);
  });

  it("re-opens a call when it starts again", async () => {
    // A tool call id can be reissued after a failure; the second attempt has to be
    // able to stream its own output.
    const { upsertMessage, state } = await read([
      durable(1, "tool_call_failed", { toolCallId: "call-1", toolName: "bash", reason: "boom" }),
      live(2, "tool_call_started", { toolCallId: "call-1", toolName: "bash" }),
      live(3, "tool_output_delta", { toolCallId: "call-1", toolName: "bash", stream: "stdout", text: "retry" }),
    ]);

    expect(state.finishedTools.has("call-1")).toBe(false);
    expect(toolMessages(upsertMessage).at(-1)).toMatchObject({ toolState: "running", text: "retry" });
  });

  it("drops a delta that does not match the declared payload shape", async () => {
    // The frame's payload is open in the API, so this narrowing is the only place
    // the contract is enforced. A producer that renames a field or drops the
    // identity must not draw output anywhere.
    const { state } = await read([
      live(1, "tool_output_delta", { toolName: "bash", stream: "stdout", text: "no id" }),
      live(2, "tool_output_delta", { toolCallId: "call-1", stream: "stdout" }),
      live(3, "tool_output_delta", { toolCallId: "call-1", stream: "sideways", text: "bad stream" }),
      live(4, "tool_output_delta", { toolCallId: "call-1", stream: "stdout", text: 42 }),
    ]);

    expect(state.toolOutputs.size).toBe(0);
  });

  it("ignores streaming arguments for a call that already finished", async () => {
    const { upsertMessage } = await read([
      durable(1, "tool_call_completed", { toolCallId: "call-1", toolName: "bash", content: "done", isError: false }),
      live(2, "tool_call_delta", { id: "call-1", name: "bash", index: 0, argumentsFragment: "late" }),
    ]);

    const rendered = toolMessages(upsertMessage);
    expect(rendered.at(-1)).toMatchObject({ toolState: "completed", text: "done" });
    expect(rendered.some((message) => message.toolState === "pending")).toBe(false);
  });

  it("does not overwrite the tool name when a delta carries none", async () => {
    // Older producers send only the text. upsertMessage merges, so a placeholder
    // name here would replace the real one tool_call_started already set.
    const { upsertMessage } = await read([
      live(1, "tool_call_started", { toolCallId: "call-1", toolName: "bash" }),
      live(2, "tool_output_delta", { toolCallId: "call-1", stream: "stdout", text: "out" }),
    ]);

    const preview = toolMessages(upsertMessage).at(-1);
    expect(preview).toMatchObject({ toolState: "running", text: "out" });
    expect(preview).not.toHaveProperty("toolName");
  });
});
