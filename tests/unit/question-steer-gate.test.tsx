import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

const { apiFetch } = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock("@/lib/worker-api.client", () => ({
  apiFetch,
  apiResponse: vi.fn(),
  WorkerGenerationChangedError: class WorkerGenerationChangedError extends Error {},
}));

import type { AgentRun, ToolApprovalRequest } from "@/lib/approval";
import { useAgentSession } from "@/hooks/useAgentSession";

const run: AgentRun = {
  id: "run-1", turnId: "turn-1", sessionId: "s1", runKind: "agent", attempt: 1,
  status: "waiting_for_approval", requestedConfig: {}, effectiveConfig: {},
  commitFormatVersion: 2, executionDepth: 0, publishMode: "public_final",
  speakerSnapshot: { kind: "host", displayName: "Host" }, contextSnapshot: {},
  createdAt: "2026-10-26T00:00:00Z",
};

const question: ToolApprovalRequest = {
  id: "approval-1", runId: run.id, sessionId: "s1", iteration: 1, batchDigest: "digest",
  kind: "question", status: "pending", items: [], requestedAt: "2026-10-26T00:00:01Z",
  questions: [{ id: "scope", prompt: "Which samples?", options: [{ id: "all", label: "All" }] }],
};

function capture(pendingApproval: ToolApprovalRequest | null) {
  let api: ReturnType<typeof useAgentSession> | undefined;
  function Harness() {
    api = useAgentSession({
      sessionId: "s1",
      appendMessage: vi.fn(),
      upsertMessage: vi.fn(),
      refreshLatest: async () => {},
      refreshSession: async () => null,
      activeRun: run,
      pendingApproval,
    });
    return null;
  }
  renderToStaticMarkup(<Harness />);
  return api as ReturnType<typeof useAgentSession>;
}

describe("steer question gate", () => {
  it("refuses to steer and sends no request while a question is pending", async () => {
    apiFetch.mockReset();
    const api = capture(question);

    await expect(api.steer("use only treated samples")).resolves.toBe(false);
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("still steers when no question is pending", async () => {
    apiFetch.mockReset();
    apiFetch.mockResolvedValue({});
    const api = capture({ ...question, kind: "tool_approval", status: "pending" });

    await expect(api.steer("narrow the range")).resolves.toBe(true);
    expect(apiFetch).toHaveBeenCalledWith("/v1/runs/run-1/inputs", expect.objectContaining({ method: "POST" }));
  });
});
