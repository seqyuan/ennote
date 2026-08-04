"use client";

import { Bot, Plus, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { RoleEditor } from "@/components/settings/RoleEditor";
import type { ModelProfile, RoleDefinition, RoleIdentity, RoleSummary } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

function defaultRoleDefinition(model: ModelProfile): RoleDefinition {
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
      admission: "approval_required", allowedCallerKinds: ["host"], allowedStrategies: ["single"],
      maxInvocationsPerParentRun: 1, maxConcurrentInstances: 1,
      budgetCeiling: { maxModelCalls: 4, maxToolCalls: 8, maxTotalTokens: 20000,
        maxOutputTokens: 4000, maxCostUsdMicros: 100000, maxWallTimeMs: 120000 },
    },
    outputContract: "text-v1", maxLoopIterations: 8,
  };
}

function toSummary(role: RoleIdentity): RoleSummary {
  return {
    id: role.id, handle: role.handle, name: role.name, description: role.description,
    positioning: role.positioning, icon: role.icon, color: role.color, scope: role.scope,
    projectId: role.projectId, status: role.status, currentVersionId: role.currentVersionId,
    currentVersion: role.currentVersion, updatedAt: role.updatedAt,
  };
}

export function RolesSettings({ projectId, models, setError }: {
  projectId: string | null;
  models: ModelProfile[];
  setError: (message: string | null) => void;
}) {
  const [roles, setRoles] = useState<RoleSummary[]>([]);
  const [selectedRole, setSelectedRole] = useState<RoleIdentity | null>(null);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<"active" | "archived">("active");
  const [loadingKey, setLoadingKey] = useState("");
  const [creating, setCreating] = useState(false);

  const catalogKey = `${projectId ?? "global"}:${status}`;
  const loadCatalog = useCallback(async () => {
    const params = new URLSearchParams({ status, limit: "100" });
    if (projectId) params.set("projectId", projectId);
    const page = await apiFetch<{ items: RoleSummary[] }>(`/v1/roles?${params}`);
    setRoles(page.items);
    setLoadingKey(catalogKey);
  }, [catalogKey, projectId, status]);

  useEffect(() => {
    let cancelled = false;
    const params = new URLSearchParams({ status, limit: "100" });
    if (projectId) params.set("projectId", projectId);
    void apiFetch<{ items: RoleSummary[] }>(`/v1/roles?${params}`)
      .then((page) => {
        if (cancelled) return;
        setRoles(page.items);
        setLoadingKey(catalogKey);
      })
      .catch((reason: unknown) => { if (!cancelled) setError((reason as Error).message); });
    return () => { cancelled = true; };
  }, [catalogKey, projectId, setError, status]);

  const visibleRoles = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return roles;
    return roles.filter((role) => [role.name, role.handle, role.positioning].some((value) => value.toLowerCase().includes(needle)));
  }, [query, roles]);

  async function selectRole(roleID: string) {
    try {
      setSelectedRole(await apiFetch<RoleIdentity>(`/v1/roles/${encodeURIComponent(roleID)}`));
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  async function createRole(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const model = models[0];
    if (!model) {
      setError("Create an active model profile before creating a Role.");
      return;
    }
    const data = new FormData(event.currentTarget);
    const scope = data.get("scope") === "global" ? "global" : "project";
    try {
      const role = await apiFetch<RoleIdentity>("/v1/roles", {
        method: "POST",
        body: JSON.stringify({
          name: data.get("name"), handle: data.get("handle"), scope,
          projectId: scope === "project" ? projectId : undefined,
          description: "", positioning: "", icon: "bot", color: "#2563eb",
          definition: defaultRoleDefinition(model),
        }),
      });
      setCreating(false);
      setStatus("active");
      setSelectedRole(role);
      await loadCatalog();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  function roleSaved(role: RoleIdentity) {
    setSelectedRole(role);
    setRoles((current) => current.map((item) => item.id === role.id ? toSummary(role) : item));
  }

  function roleArchived(role: RoleIdentity) {
    setSelectedRole(null);
    setRoles((current) => current.filter((item) => item.id !== role.id));
  }

  const loading = loadingKey !== catalogKey;

  return <section className="roles-settings" aria-labelledby="settings-roles-heading">
    <div className="roles-master">
      <header>
        <div><h2 id="settings-roles-heading">Roles</h2><span>{roles.length} {status}</span></div>
        <button type="button" className="settings-btn primary" onClick={() => setCreating((value) => !value)}>
          <Plus size={14} /> New
        </button>
      </header>
      <div className="roles-filterbar">
        <label><Search size={14} aria-hidden="true" /><input value={query} placeholder="Search Roles"
          onChange={(event) => setQuery(event.target.value)} /></label>
        <div className="roles-status-toggle" role="group" aria-label="Role status">
          <button type="button" aria-pressed={status === "active"} onClick={() => setStatus("active")}>Active</button>
          <button type="button" aria-pressed={status === "archived"} onClick={() => setStatus("archived")}>Archived</button>
        </div>
      </div>
      {creating && <form className="role-create-form" onSubmit={createRole}>
        <input name="name" placeholder="Role name" required />
        <input name="handle" placeholder="role-handle" pattern="[a-z][a-z0-9_-]{1,31}" required />
        <select name="scope" defaultValue={projectId ? "project" : "global"}>
          {projectId && <option value="project">Project</option>}<option value="global">Global</option>
        </select>
        <button type="submit" className="settings-btn primary">Create</button>
      </form>}
      <div className="roles-list" aria-busy={loading}>
        {loading && <span className="roles-empty">Loading</span>}
        {!loading && visibleRoles.length === 0 && <span className="roles-empty">No Roles</span>}
        {visibleRoles.map((role) => <button type="button" key={role.id}
          className={selectedRole?.id === role.id ? "role-list-item selected" : "role-list-item"}
          onClick={() => void selectRole(role.id)}>
          <span className="role-list-icon" style={{ color: role.color }}><Bot size={16} /></span>
          <span><strong>{role.name}</strong><small>@{role.handle}</small><small>{role.positioning || role.description}</small></span>
          <em>{role.currentVersion ? `v${role.currentVersion}` : "draft"}</em>
        </button>)}
      </div>
    </div>
    <div className="roles-detail">
      {selectedRole
        ? <RoleEditor key={`${selectedRole.id}:${selectedRole.draftRevision}:${selectedRole.currentVersionId ?? "draft"}`}
            role={selectedRole} models={models} onSaved={roleSaved} onArchived={roleArchived} setError={setError} />
        : <div className="roles-detail-empty"><Bot size={24} /><strong>Select a Role</strong></div>}
    </div>
  </section>;
}
