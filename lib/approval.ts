import type { components } from "./worker-api.gen";

export type ToolApprovalRequest = components["schemas"]["ToolApprovalRequest"];
export type ApprovalItem = components["schemas"]["ApprovalItem"];
export type ActiveRunState = components["schemas"]["ActiveRunState"];
export type AgentRun = components["schemas"]["AgentRun"];
export type ApprovalDecision = "approved" | "rejected";

const riskLabels: Record<ApprovalItem["riskClass"], string> = {
  read_only: "Read only",
  local_write: "Local write",
  shell: "Shell",
  external: "External",
  sensitive: "Sensitive",
};

export function approvalRiskLabel(risk: ApprovalItem["riskClass"]): string {
  return riskLabels[risk] ?? "Sensitive";
}

export function isPendingApproval(value: ToolApprovalRequest | null | undefined): value is ToolApprovalRequest {
  return Boolean(value && value.status === "pending" && value.items.length > 0);
}
