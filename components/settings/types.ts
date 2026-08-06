import type { components } from "@/lib/worker-api.gen";

export type ProviderProfile = components["schemas"]["ProviderProfile"];
export type ProviderDiagnostic = components["schemas"]["ProviderDiagnostic"];
export type ModelProfile = components["schemas"]["ModelProfile"];
export type PolicyProfile = components["schemas"]["PolicyProfile"];
export type Session = components["schemas"]["Session"];
export type RoleDefinition = components["schemas"]["RoleDefinition"];
export type RoleIdentity = components["schemas"]["RoleIdentity"];
export type RoleSummary = components["schemas"]["RoleSummary"];
export type RoleVersion = components["schemas"]["RoleVersion"];
export type RoleValidationResult = components["schemas"]["RoleValidationResult"];
export type MCPServerProfile = components["schemas"]["MCPServerProfile"];
export type MCPServerProfileVersion = components["schemas"]["MCPServerProfileVersion"];
export type AgentFlowProfile = components["schemas"]["AgentFlowProfile"];
export type AgentFlowVersion = components["schemas"]["AgentFlowVersion"];
export type ProjectAgentFlowBinding = components["schemas"]["ProjectAgentFlowBinding"];
export type RunAgentFlow = components["schemas"]["RunAgentFlow"];
export type RunAgentFlowNode = components["schemas"]["RunAgentFlowNode"];
export type AgentFlowCandidate = components["schemas"]["AgentFlowCandidate"];
export type FlowValidationResult = components["schemas"]["FlowValidationResult"];
export type AgentFlowCheckApproval = components["schemas"]["AgentFlowCheckApproval"];
export type MCPProjectBinding = components["schemas"]["MCPProjectBinding"];
export type MCPCatalogEntry = components["schemas"]["MCPCatalogEntry"];
export type MCPCandidate = components["schemas"]["MCPCandidate"];

export type SettingsTab = "providers" | "models" | "roles" | "policies" | "context" | "templates" | "mcp" | "flows";
