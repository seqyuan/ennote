import type { components } from "@/lib/worker-api.gen";

export type ProviderProfile = components["schemas"]["ProviderProfile"];
export type ProviderDiagnostic = components["schemas"]["ProviderDiagnostic"];
export type DiscoveredModel = components["schemas"]["DiscoveredModel"];
export type ModelProfile = components["schemas"]["ModelProfile"];
export type PolicyProfile = components["schemas"]["PolicyProfile"];
export type Session = components["schemas"]["Session"];
export type RoleSummary = components["schemas"]["RoleSummary"];
export type MCPServerProfile = components["schemas"]["MCPServerProfile"];
export type MCPServerProfileVersion = components["schemas"]["MCPServerProfileVersion"];

export type AgentFlowVersion = components["schemas"]["AgentFlowVersion"];

export type RunAgentFlow = components["schemas"]["RunAgentFlow"];
export type RunAgentFlowNode = components["schemas"]["RunAgentFlowNode"];

export type FlowValidationResult = components["schemas"]["FlowValidationResult"];

export type MCPProjectBinding = components["schemas"]["MCPProjectBinding"];
export type MCPCatalogEntry = components["schemas"]["MCPCatalogEntry"];
export type MCPCandidate = components["schemas"]["MCPCandidate"];
export type StandingApproval = components["schemas"]["StandingApproval"];
export type SkillListResult = components["schemas"]["SkillListResult"];
export type AnnotatedSkill = components["schemas"]["AnnotatedSkill"];
export type SkillInstallInfo = components["schemas"]["SkillInstallInfo"];
export type SkillSearchResult = components["schemas"]["SkillSearchResult"];
export type SkillUpdateResult = components["schemas"]["SkillUpdateResult"];
export type SkillRoot = components["schemas"]["SkillRoot"];

export type SettingsTab = "general" | "models" | "policies" | "context" | "templates" | "mcp" | "skills";
