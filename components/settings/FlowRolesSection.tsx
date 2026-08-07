"use client";

import { useCallback, useEffect, useState } from "react";
import { Plus } from "lucide-react";
import { RoleEditor } from "@/components/settings/RoleEditor";
import { useSettingsProfiles } from "@/hooks/useSettingsProfiles";
import { apiFetch } from "@/lib/worker-api.client";
import type { ModelProfile, RoleIdentity, RoleSummary } from "@/components/settings/types";

const labelStyle: React.CSSProperties = {
  display: "flex", flexDirection: "column", gap: 4,
  fontSize: 11, fontWeight: 600, color: "var(--text-dim)",
};
const inputStyle: React.CSSProperties = {
  padding: "6px 8px", borderRadius: 6, border: "1px solid var(--border)",
  background: "var(--bg)", color: "var(--text)", fontSize: 12,
};

function defaultRoleDefinition(model: ModelProfile): RoleIdentity["draft"] {
  return {
    schemaVersion: 1,
    rolePrompt: "Perform the requested work independently. Distinguish evidence from assumptions and report a concise result.",
    modelBinding: {
      mode: "fixed", modelProfileId: model.id, thinkingEffort: "default",
      fallbackModelProfileIds: [], overridableFields: [],
    },
    skills: { entries: [] },
    authority: "read_only", permissionCeiling: "discuss",
    allowedTools: ["read", "ls", "grep", "find"],
    contextPolicy: { defaultMode: "room", allowedModes: ["room", "fresh"], ownExecutionContinuity: "none" },
    delegationPolicy: {
      admission: "auto_within_budget", allowedCallerKinds: ["host"], allowedStrategies: ["single", "parallel"],
      maxInvocationsPerParentRun: 16, maxConcurrentInstances: 16,
      budgetCeiling: { maxModelCalls: 4, maxToolCalls: 8, maxTotalTokens: 20000,
        maxOutputTokens: 4000, maxCostUsdMicros: 100000, maxWallTimeMs: 120000 },
    },
    outputContract: "text-v1", maxLoopIterations: 8,
  };
}

/**
 * FlowRolesSection manages graph-local Roles (scope='flow') for the currently
 * selected Agent Flow. These Roles resolve only inside the owning graph's task
 * references (bare handle) and never from delegate_tasks. The section lists,
 * creates, and edits them inline via the shared RoleEditor.
 */
export function FlowRolesSection({ flowId, projectId, setError, onRolesChanged }: {
  flowId: string;
  projectId: string | null;
  setError: (message: string | null) => void;
  onRolesChanged?: () => void;
}) {
  const settings = useSettingsProfiles();
  const models = settings.models.filter((model) => model.status === "active");
  const [roles, setRoles] = useState<RoleSummary[]>([]);
  const [selectedRole, setSelectedRole] = useState<RoleIdentity | null>(null);
  const [creating, setCreating] = useState(false);
  const [newHandle, setNewHandle] = useState("");
  const [newName, setNewName] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    if (!flowId) return;
    setLoading(true);
    try {
      const page = await apiFetch<{ items: RoleSummary[] }>(
        `/v1/roles?scope=flow&flowId=${encodeURIComponent(flowId)}&status=active&limit=100`,
      );
      setRoles(page.items ?? []);
      setSelectedRole(null);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setLoading(false);
    }
  }, [flowId, setError]);

  useEffect(() => { void load(); }, [load]);

  const openRole = useCallback(async (roleId: string) => {
    try {
      const role = await apiFetch<RoleIdentity>(`/v1/roles/${encodeURIComponent(roleId)}`);
      setSelectedRole(role);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [setError]);

  const createRole = useCallback(async () => {
    const model = models[0];
    if (!model) {
      setError("Create an active model profile before creating a graph Role.");
      return;
    }
    if (!newHandle.trim() || !newName.trim()) {
      setError("Name and handle are required.");
      return;
    }
    try {
      const role = await apiFetch<RoleIdentity>("/v1/roles", {
        method: "POST",
        body: JSON.stringify({
          name: newName.trim(), handle: newHandle.trim(), scope: "flow", flowId,
          description: "", positioning: "", icon: "bot", color: "#2563eb",
          definition: defaultRoleDefinition(model),
        }),
      });
      setCreating(false);
      setNewHandle("");
      setNewName("");
      setSelectedRole(role);
      await load();
      onRolesChanged?.();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }, [flowId, load, models, newHandle, newName, onRolesChanged, setError]);

  return (
    <div style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 10, display: "flex", flexDirection: "column", gap: 8 }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <div>
          <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)" }}>Graph-local Roles</div>
          <div style={{ fontSize: 10, color: "var(--text-dim)" }}>
            Resolve only inside this graph (bare task role refs); never from delegate_tasks.
          </div>
        </div>
        <button
          type="button"
          onClick={() => setCreating((value) => !value)}
          style={{
            display: "flex", alignItems: "center", gap: 5,
            padding: "4px 10px", borderRadius: 6, border: "1px solid var(--border)",
            background: "var(--bg)", color: "var(--text)", fontSize: 11, cursor: "pointer",
          }}
        >
          <Plus size={12} />
          {creating ? "Cancel" : "New role"}
        </button>
      </div>

      {creating && (
        <div style={{ display: "flex", gap: 8, alignItems: "end", flexWrap: "wrap" }}>
          <label style={labelStyle}>Handle
            <input value={newHandle} onChange={(e) => setNewHandle(e.target.value)} style={{ ...inputStyle, width: 140 }} placeholder="graph-writer" />
          </label>
          <label style={labelStyle}>Name
            <input value={newName} onChange={(e) => setNewName(e.target.value)} style={{ ...inputStyle, width: 160 }} placeholder="Graph Writer" />
          </label>
          <button
            type="button"
            onClick={createRole}
            style={{
              padding: "6px 12px", borderRadius: 6, border: "none",
              background: "var(--accent)", color: "#fff", fontSize: 12, cursor: "pointer",
            }}
          >
            Create
          </button>
        </div>
      )}

      {loading ? (
        <div style={{ fontSize: 11, color: "var(--text-dim)" }}>Loading…</div>
      ) : roles.length === 0 && !creating ? (
        <div style={{ fontSize: 11, color: "var(--text-dim)" }}>
          No graph-local roles yet. Create one to bind a task with a bare <code>role:</code> reference.
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
          {roles.map((role) => (
            <button
              key={role.id}
              type="button"
              onClick={() => void openRole(role.id)}
              style={{
                display: "flex", alignItems: "center", gap: 8,
                padding: "5px 8px", borderRadius: 6,
                background: selectedRole?.id === role.id ? "var(--bg-selected)" : "transparent",
                border: "1px solid transparent", color: "var(--text)", fontSize: 12, cursor: "pointer",
                textAlign: "left",
              }}
            >
              <span style={{ fontFamily: "var(--font-mono, monospace)", fontSize: 11, color: "var(--accent)" }}>{role.handle}</span>
              <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{role.name}</span>
              <span style={{ fontSize: 10, color: "var(--text-dim)" }}>
                {role.currentVersion ? `v${role.currentVersion}` : "draft"}
              </span>
            </button>
          ))}
        </div>
      )}

      {selectedRole && (
        <div style={{ borderTop: "1px solid var(--border)", paddingTop: 8 }}>
          <RoleEditor
            role={selectedRole}
            models={models}
            onSaved={async (role) => { setSelectedRole(role); await load(); onRolesChanged?.(); }}
            onArchived={() => { setSelectedRole(null); void load(); onRolesChanged?.(); }}
            setError={setError}
          />
        </div>
      )}
    </div>
  );
}
