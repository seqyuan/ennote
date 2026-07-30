import { describe, expect, it } from "vitest";

import { approvalRiskLabel, isPendingApproval, type ToolApprovalRequest } from "../../lib/approval";

const approval: ToolApprovalRequest = {
  id: "approval", runId: "run", sessionId: "session", iteration: 1, batchDigest: "digest",
  status: "pending", requestedAt: "2026-07-28T00:00:00Z",
  items: [{ callIndex: 0, toolCallId: "call", toolName: "write", riskClass: "local_write", argumentsPreview: "{}" }],
};

describe("Interactive Approval helpers", () => {
  it("recognizes only actionable pending approvals", () => {
    expect(isPendingApproval(approval)).toBe(true);
    expect(isPendingApproval({ ...approval, status: "approved" })).toBe(false);
    expect(isPendingApproval({ ...approval, items: [] })).toBe(false);
  });

  it("uses stable human-readable risk labels", () => {
    expect(approvalRiskLabel("local_write")).toBe("Local write");
    expect(approvalRiskLabel("shell")).toBe("Shell");
  });
});
