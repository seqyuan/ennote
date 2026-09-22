export type PermissionMode = "discuss" | "ask" | "auto";

export type ThinkingEffort = "default" | "low" | "medium" | "high";

export interface PermissionPolicyProfile {
  id: string;
  kind: string;
  status: string;
  config: Record<string, unknown>;
}

/** Narrow an arbitrary value (a policy config, a frozen run field) to a mode. */
export function isPermissionMode(value: unknown): value is PermissionMode {
  return value === "discuss" || value === "ask" || value === "auto";
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null ? value as Record<string, unknown> : undefined;
}

export function permissionPolicyID(profiles: PermissionPolicyProfile[], mode: PermissionMode): string | undefined {
  const active = profiles.filter(profile => profile.kind === "tool" && profile.status === "active" && profile.config.mode === mode);
  // Builtin tool-policy versions are not uniform (Discuss ships v3 while Ask
  // and Auto ship v1), so match any versioned builtin id for the mode instead
  // of pinning one version number.
  const builtinID = new RegExp(`^builtin-tool-${mode}-v\\d+$`);
  return active.find(profile => builtinID.test(profile.id))?.id ?? active[0]?.id;
}

export function permissionModeForPolicyID(profiles: PermissionPolicyProfile[], policyID: unknown): PermissionMode | undefined {
  if (typeof policyID !== "string" || !policyID) return undefined;
  const profile = profiles.find(item => item.id === policyID && item.kind === "tool");
  const configured = profile?.config.mode;
  if (isPermissionMode(configured)) return configured;
  const builtin = /^builtin-tool-(discuss|ask|auto)-v\d+$/.exec(policyID)?.[1];
  return builtin === "discuss" || builtin === "ask" || builtin === "auto" ? builtin : undefined;
}

/**
 * Resolve the permission mode actually frozen onto a Run. A Role Run pins it
 * through the Role's permissionCeiling (and the builtin tool policy mapped from
 * it), so requestedConfig is not authoritative there. `requestedConfig` is only
 * a fallback for a Run whose effective snapshot has not been recorded yet.
 */
export function frozenPermissionMode(
  run: { requestedConfig?: unknown; effectiveConfig?: unknown } | null | undefined,
  profiles: PermissionPolicyProfile[],
): PermissionMode | undefined {
  const effective = asRecord(run?.effectiveConfig);
  const ceiling = asRecord(effective?.role)?.permissionCeiling;
  if (isPermissionMode(ceiling)) return ceiling;
  const frozenToolPolicyID = asRecord(effective?.toolPolicy)?.id;
  const requestedToolPolicyID = asRecord(run?.requestedConfig)?.toolPolicyProfileId;
  return permissionModeForPolicyID(profiles, frozenToolPolicyID ?? requestedToolPolicyID);
}

export function withRunConfig<T extends Record<string, unknown>>(
  payload: T,
  policyID: string,
  modelProfileID?: string | null,
  thinkingEffort?: ThinkingEffort | null,
): T & { config: { toolPolicyProfileId: string; modelProfileId?: string; thinkingEffort?: ThinkingEffort } } {
  return {
    ...payload,
    config: {
      toolPolicyProfileId: policyID,
      ...(modelProfileID ? { modelProfileId: modelProfileID } : {}),
      ...(thinkingEffort ? { thinkingEffort } : {}),
    },
  };
}

export const withPermissionConfig = withRunConfig;
