"use client";

import { Fragment, useCallback, useEffect, useState } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import {
  diffText,
  draftToYAML,
  emptyDraft,
  emptyTask,
  flowToDraft,
  suggestedBudget,
  type FlowDraft,
  type TaskDraft,
} from "@/lib/agent-flow";
import type {
  AgentFlowCandidate,
  AgentFlowCheckApproval,
  AgentFlowProfile,
  AgentFlowVersion,
  FlowValidationResult,
  ProjectAgentFlowBinding,
  RoleSummary,
  RunAgentFlow,
  RunAgentFlowNode,
  Session,
} from "@/components/settings/types";

// --- Settings component ---

export function AgentFlowSettings({ projectId, setError }: {
  projectId: string | null;
  setError: (value: string | null) => void;
}) {
  const [profiles, setProfiles] = useState<AgentFlowProfile[]>([]);
  const [candidates, setCandidates] = useState<AgentFlowCandidate[]>([]);
  const [bindings, setBindings] = useState<ProjectAgentFlowBinding[]>([]);
  const [versions, setVersions] = useState<Record<string, AgentFlowVersion[]>>({});
  const [runs, setRuns] = useState<RunAgentFlow[]>([]);
  const [approvals, setApprovals] = useState<AgentFlowCheckApproval[]>([]);
  const [roles, setRoles] = useState<RoleSummary[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [draft, setDraft] = useState<FlowDraft | null>(null);
  const [validation, setValidation] = useState<FlowValidationResult | null>(null);
  const [busy, setBusy] = useState<"save" | "publish" | "create" | "run" | null>(null);
  const [detail, setDetail] = useState<{ run: RunAgentFlow; nodes: RunAgentFlowNode[] } | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [newName, setNewName] = useState("");
  const [newSlug, setNewSlug] = useState("");
  const [timeline, setTimeline] = useState<string[]>([]);
  const [diffView, setDiffView] = useState<{ slug: string; lines: string[] } | null>(null);
  const [showImport, setShowImport] = useState(false);
  const [runPanelFor, setRunPanelFor] = useState<string | null>(null);
  const [runSessionId, setRunSessionId] = useState<string>("");
  const [importYaml, setImportYaml] = useState("");
  const [importReport, setImportReport] = useState<{
    valid: boolean;
    diagnostics: Array<{ code: string; message: string }>;
    dependencies: Array<{ kind: string; name: string; version?: number; present: boolean; reason?: string }>;
  } | null>(null);
  const [importing, setImporting] = useState(false);

  const refresh = useCallback(async () => {
    if (!projectId) return;
    try {
      const [profileList, bindingList, candidateList, runList, approvalList, rolePage, sessionList] = await Promise.all([
        apiFetch<AgentFlowProfile[]>("/v1/agent-flows"),
        apiFetch<ProjectAgentFlowBinding[]>(`/v1/projects/${projectId}/agent-flows/bindings`),
        apiFetch<AgentFlowCandidate[]>(`/v1/projects/${projectId}/agent-flows/candidates`),
        apiFetch<RunAgentFlow[]>(`/v1/projects/${projectId}/agent-flows/runs`),
        apiFetch<AgentFlowCheckApproval[]>(`/v1/projects/${projectId}/agent-flows/check-approvals`),
        apiFetch<{ items: RoleSummary[] }>(`/v1/roles?status=active&limit=100`),
        apiFetch<Session[]>(`/v1/projects/${projectId}/sessions`),
      ]);
      setProfiles(profileList);
      setBindings(bindingList);
      setCandidates(candidateList);
      setRuns(runList);
      setApprovals(approvalList);
      setRoles(rolePage.items ?? []);
      setSessions(sessionList);
      const versionMap: Record<string, AgentFlowVersion[]> = {};
      await Promise.all(profileList.map(async (profile) => {
        try {
          versionMap[profile.id!] = await apiFetch<AgentFlowVersion[]>(`/v1/agent-flows/${profile.id}/versions`);
        } catch {
          versionMap[profile.id!] = [];
        }
      }));
      setVersions(versionMap);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load Agent Flows");
    }
  }, [projectId, setError]);

  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    void Promise.all([
      apiFetch<AgentFlowProfile[]>("/v1/agent-flows"),
      apiFetch<ProjectAgentFlowBinding[]>(`/v1/projects/${projectId}/agent-flows/bindings`),
      apiFetch<AgentFlowCandidate[]>(`/v1/projects/${projectId}/agent-flows/candidates`),
      apiFetch<RunAgentFlow[]>(`/v1/projects/${projectId}/agent-flows/runs`),
      apiFetch<AgentFlowCheckApproval[]>(`/v1/projects/${projectId}/agent-flows/check-approvals`),
      apiFetch<{ items: RoleSummary[] }>(`/v1/roles?status=active&limit=100`),
      apiFetch<Session[]>(`/v1/projects/${projectId}/sessions`),
    ]).then(async ([profileList, bindingList, candidateList, runList, approvalList, rolePage, sessionList]) => {
      if (cancelled) return;
      setProfiles(profileList);
      setBindings(bindingList);
      setCandidates(candidateList);
      setRuns(runList);
      setApprovals(approvalList);
      setRoles(rolePage.items ?? []);
      setSessions(sessionList);
      const versionMap: Record<string, AgentFlowVersion[]> = {};
      await Promise.all(profileList.map(async (profile) => {
        try {
          versionMap[profile.id!] = await apiFetch<AgentFlowVersion[]>(`/v1/agent-flows/${profile.id}/versions`);
        } catch {
          versionMap[profile.id!] = [];
        }
      }));
      if (cancelled) return;
      setVersions(versionMap);
    }).catch((err: unknown) => {
      if (cancelled) return;
      setError(err instanceof Error ? err.message : "Failed to load Agent Flows");
    });
    const timer = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, 4000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [projectId, refresh, setError]);

  const versionFor = useCallback((binding: ProjectAgentFlowBinding): AgentFlowVersion | undefined => {
    for (const list of Object.values(versions)) {
      const version = list.find((v) => v.id === binding.flowVersionId);
      if (version) return version;
    }
    return undefined;
  }, [versions]);

  const openEditor = async (profile: AgentFlowProfile) => {
    setSelected(profile.id!);
    setValidation(null);
    try {
      const detail = await apiFetch<AgentFlowProfile>(`/v1/agent-flows/${profile.id ?? ""}`);
      if (detail.draft) {
        const parsed = typeof detail.draft === "string" ? JSON.parse(detail.draft) : detail.draft;
        setDraft(flowToDraft(parsed, profile.slug ?? ""));
      } else {
        setDraft(emptyDraft(profile.slug ?? ""));
      }
    } catch {
      setDraft(emptyDraft(profile.slug ?? ""));
    }
  };

  const precheckImport = async () => {
    if (!importYaml.trim()) return;
    try {
      const report = await apiFetch<typeof importReport>(`/v1/agent-flows/check-dependencies`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ yaml: importYaml }),
      });
      setImportReport(report);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Dependency pre-check failed");
    }
  };

  const confirmImport = async () => {
    setImporting(true);
    try {
      await apiFetch(`/v1/agent-flows/import`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ yaml: importYaml }),
      });
      setShowImport(false);
      setImportYaml("");
      setImportReport(null);
      setError(null);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Import failed");
    } finally {
      setImporting(false);
    }
  };

  const exportFlow = async (profileId: string, source: "draft" | "version", versionId?: string) => {
    try {
      const params = new URLSearchParams({ source });
      if (versionId) params.set("versionID", versionId);
      const yamlText = await apiFetch<string>(`/v1/agent-flows/${profileId}/export?${params.toString()}`);
      await navigator.clipboard.writeText(yamlText);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Export failed");
    }
  };

  const createProfile = async () => {
    setBusy("create");
    try {
      const profile = await apiFetch<AgentFlowProfile>("/v1/agent-flows", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: newName, slug: newSlug }),
      });
      setShowCreate(false);
      setNewName("");
      setNewSlug("");
      await refresh();
      await openEditor(profile);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create flow");
    } finally {
      setBusy(null);
    }
  };

  const saveDraft = async () => {
    if (!selected || !draft) return;
    setBusy("save");
    try {
      const profile = await apiFetch<AgentFlowProfile>(`/v1/agent-flows/${selected}/draft`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ yaml: draftToYAML(draft), expectedRevision: currentRevision() }),
      });
      setValidation(null);
      await refresh();
      await openEditor(profile);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save draft");
    } finally {
      setBusy(null);
    }
  };

  const currentRevision = () => {
    const profile = profiles.find((p) => p.id === selected);
    return profile?.draftRevision ?? 0;
  };

  const validateDraft = async () => {
    if (!selected) return;
    try {
      const result = await apiFetch<FlowValidationResult>(`/v1/agent-flows/${selected}/validate`, { method: "POST" });
      setValidation(result);
      setError(result.valid ? null : "Flow is invalid — see diagnostics below");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Validation failed");
    }
  };

  const publishDraft = async () => {
    if (!selected) return;
    setBusy("publish");
    try {
      const result = await apiFetch<FlowValidationResult>(`/v1/agent-flows/${selected}/validate`, { method: "POST" });
      setValidation(result);
      if (!result.valid) {
        setError("Flow is invalid — fix the diagnostics below before publishing");
        return;
      }
      await apiFetch<AgentFlowVersion>(`/v1/agent-flows/${selected}/publish`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ expectedRevision: currentRevision() }),
      });
      setError(null);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Publish failed");
    } finally {
      setBusy(null);
    }
  };

  const updateTask = (index: number, change: (task: TaskDraft) => TaskDraft) => {
    if (!draft) return;
    setDraft((current) => {
      if (!current) return current;
      const tasks = current.tasks.map((task, i) => (i === index ? change(task) : task));
      return { ...current, tasks };
    });
    setValidation(null);
  };

  const addTask = () => {
    if (!draft) return;
    setDraft((current) => {
      if (!current) return current;
      const base = `task${current.tasks.length + 1}`;
      let name = base;
      let n = 1;
      while (current.tasks.some((t) => t.name === name)) {
        name = `${base}-${++n}`;
      }
      return { ...current, tasks: [...current.tasks, emptyTask(name)] };
    });
  };

  const bindCandidate = async (candidate: AgentFlowCandidate) => {
    if (!projectId) return;
    try {
      await apiFetch<ProjectAgentFlowBinding>(`/v1/projects/${projectId}/agent-flows/bindings/from-candidate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ slug: candidate.slug }),
      });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to bind candidate");
    }
  };

  const bindVersion = async (version: AgentFlowVersion) => {
    if (!projectId) return;
    try {
      await apiFetch<ProjectAgentFlowBinding>(`/v1/projects/${projectId}/agent-flows/bindings`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flowVersionId: version.id, desiredEnabled: false }),
      });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to bind version");
    }
  };

  const updateBinding = async (binding: ProjectAgentFlowBinding, desiredEnabled: boolean) => {
    if (!projectId) return;
    try {
      await apiFetch<ProjectAgentFlowBinding>(
        `/v1/projects/${projectId}/agent-flows/bindings/${binding.id}`,
        { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ desiredEnabled }) },
      );
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update binding");
    }
  };

  const runFlow = async (binding: ProjectAgentFlowBinding, sessionId: string) => {
    if (!sessionId) return;
    try {
      await apiFetch<RunAgentFlow>(`/v1/projects/${projectId}/agent-flows/bindings/${binding.id}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sessionId, inputs: {}, clientRequestId: `flow-run-${binding.id}` }),
      });
      setError(null);
      setRunPanelFor(null);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to start flow run");
    }
  };

  const cancelRun = async (run: RunAgentFlow) => {
    if (!projectId) return;
    if (!window.confirm("Cancel this flow run? The active child Run is hard-cancelled and future tasks are never scheduled.")) return;
    try {
      await apiFetch(`/v1/projects/${projectId}/agent-flows/runs/${run.runId ?? ""}/cancel`, { method: "POST" });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Cancel failed");
    }
  };

  const resumeRun = async (run: RunAgentFlow) => {
    if (!projectId) return;
    if (!window.confirm("Resume this flow run? Completed tasks keep their checkpoints; unfinished tasks continue.")) return;
    try {
      await apiFetch(`/v1/projects/${projectId}/agent-flows/runs/${run.runId ?? ""}/resume`, { method: "POST" });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Resume failed");
    }
  };

  const decideApproval = async (approval: AgentFlowCheckApproval, approved: boolean) => {
    if (!projectId) return;
    try {
      await apiFetch(`/v1/projects/${projectId}/agent-flows/check-approvals/${approval.runId}/${approval.taskIndex}/decide`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ approved, clientRequestId: `check-${approval.runId}-${approval.taskIndex}` }),
      });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Decision failed");
    }
  };

  const openRunDetail = async (run: RunAgentFlow) => {
    if (!projectId) return;
    try {
      const data = await apiFetch<{ run: RunAgentFlow; nodes: RunAgentFlowNode[] }>(
        `/v1/projects/${projectId}/agent-flows/runs/${run.runId ?? ""}`,
      );
      setDetail(data);
      // Timeline from the durable event stream.
      try {
        const events = await apiFetch<Array<{ eventType: string; payload: Record<string, unknown> }>>(
          `/v1/runs/${run.runId ?? ""}/events`,
        );
        setTimeline((events ?? []).map((event) => `${event.eventType}`));
      } catch {
        setTimeline([]);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load run detail");
    }
  };

  if (!projectId) {
    return <div style={{ fontSize: 12, color: "var(--text-dim)" }}>Open a project to manage Agent Flows.</div>;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <div>
          <div style={{ fontSize: 13, fontWeight: 700, color: "var(--text)" }}>Agent Flows</div>
          <div style={{ fontSize: 11, color: "var(--text-dim)", marginTop: 2 }}>
            Task DAGs of Role agents. Published versions are immutable; project files only produce candidates.
          </div>
        </div>
        <div style={{ display: "flex", gap: 6 }}>
          <button
            type="button"
            onClick={() => {
              setShowImport((value) => !value);
              setImportReport(null);
              setImportYaml("");
            }}
            style={{
              padding: "5px 10px", borderRadius: 6, border: "1px solid var(--border)",
              background: "var(--bg)", color: "var(--text)", fontSize: 12, cursor: "pointer",
            }}
          >
            {showImport ? "Cancel" : "Import YAML"}
          </button>
          <button
            type="button"
            onClick={() => setShowCreate((value) => !value)}
            style={{
              padding: "5px 10px", borderRadius: 6, border: "1px solid var(--border)",
              background: "var(--bg)", color: "var(--text)", fontSize: 12, cursor: "pointer",
            }}
          >
            {showCreate ? "Cancel" : "+ New flow"}
          </button>
        </div>
      </div>

      {showCreate && (
        <div style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 12, display: "flex", flexDirection: "column", gap: 8 }}>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
            <label style={labelStyle}>Name
              <input value={newName} onChange={(e) => setNewName(e.target.value)} style={inputStyle} placeholder="Go Change Review" />
            </label>
            <label style={labelStyle}>Slug (id)
              <input value={newSlug} onChange={(e) => setNewSlug(e.target.value)} style={inputStyle} placeholder="go-change-review" />
            </label>
          </div>
          <button
            type="button"
            disabled={!newName.trim() || !newSlug.trim() || busy === "create"}
            onClick={createProfile}
            style={{
              padding: "6px 12px", borderRadius: 6, border: "none",
              background: "var(--accent)", color: "#fff", fontSize: 12, cursor: "pointer", alignSelf: "flex-start",
            }}
          >
            Create flow
          </button>
        </div>
      )}

      {/* Import dialog */}
      {showImport && (
        <div style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 12, display: "flex", flexDirection: "column", gap: 8 }}>
          <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)" }}>
            Import flow YAML <span style={{ fontWeight: 400, color: "var(--text-dim)" }}>· creates a managed draft; never publishes or binds</span>
          </div>
          <textarea
            value={importYaml}
            onChange={(e) => {
              setImportYaml(e.target.value);
              setImportReport(null);
            }}
            placeholder={"schemaVersion: 1\nid: my-flow\nbudget:\n  max_total_tokens: 100000\ntasks:\n  ..."}
            style={{ ...inputStyle, minHeight: 120, fontFamily: "var(--font-mono, monospace)", fontSize: 11 }}
          />
          <div style={{ display: "flex", gap: 6 }}>
            <button type="button" onClick={precheckImport} style={ghostButtonStyle}>
              Check dependencies
            </button>
            <button
              type="button"
              disabled={importing || !importYaml.trim() || (importReport !== null && !importReport.valid)}
              onClick={confirmImport}
              style={{
                padding: "4px 10px", borderRadius: 6, fontSize: 11, border: "none",
                background: "var(--accent)", color: "#fff", cursor: "pointer",
              }}
            >
              {importing ? "Importing…" : "Import as draft"}
            </button>
          </div>
          {importReport && (
            <div style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 8, display: "flex", flexDirection: "column", gap: 6 }}>
              <div style={{ fontSize: 11, fontWeight: 700, color: importReport.valid ? "#059669" : "#E11D48" }}>
                {importReport.valid ? "Valid — ready to import as draft" : "Validation failed — fix before importing"}
              </div>
              {importReport.diagnostics?.length > 0 && (
                <div style={{ fontSize: 11, color: "#E11D48", fontFamily: "var(--font-mono, monospace)" }}>
                  {importReport.diagnostics.map((d, index) => (
                    <div key={index}>{d.code}: {d.message}</div>
                  ))}
                </div>
              )}
              {importReport.dependencies?.length > 0 && (
                <div>
                  <div style={{ fontSize: 11, color: "var(--text-muted)", marginBottom: 4 }}>Dependencies (reported only; never installed)</div>
                  {importReport.dependencies.map((dep, index) => (
                    <div key={index} style={{ fontSize: 11, color: dep.present ? "var(--text-muted)" : "#E11D48", fontFamily: "var(--font-mono, monospace)" }}>
                      {dep.kind} {dep.name}{dep.version ? `@${dep.version}` : ""} · {dep.present ? "present" : "missing"}
                      {!dep.present && dep.reason ? ` (${dep.reason})` : ""}
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {/* Editor */}
      {selected && draft && (
        <div style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 12, display: "flex", flexDirection: "column", gap: 10 }}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
            <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)" }}>
              Edit {draft.id} <span style={{ color: "var(--text-dim)", fontWeight: 400 }}>· draft rev {currentRevision()}</span>
            </div>
            <div style={{ display: "flex", gap: 6 }}>
              <button type="button" onClick={validateDraft} style={ghostButtonStyle}>Validate</button>
              <button
                type="button"
                disabled={busy === "save"}
                onClick={saveDraft}
                style={{ ...ghostButtonStyle, color: "var(--text)" }}
              >
                {busy === "save" ? "Saving…" : "Save draft"}
              </button>
              <button
                type="button"
                disabled={busy === "publish"}
                onClick={publishDraft}
                style={{
                  padding: "4px 10px", borderRadius: 6, fontSize: 11, border: "none",
                  background: "var(--accent)", color: "#fff", cursor: "pointer",
                }}
              >
                {busy === "publish" ? "Publishing…" : "Publish"}
              </button>
            </div>
          </div>

          <div style={{ display: "grid", gridTemplateColumns: "2fr 1fr", gap: 10 }}>
            <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
              <label style={labelStyle}>Description
                <input value={draft.description} onChange={(e) => setDraft({ ...draft, description: e.target.value })} style={inputStyle} />
              </label>
              <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
                <div>
                  <div style={{ fontSize: 11, color: "var(--text-muted)", fontWeight: 600, marginBottom: 3 }}>Inputs (typed ports)</div>
                  {draft.inputs.map((input, index) => (
                    <div key={index} style={{ display: "flex", gap: 4, marginBottom: 4 }}>
                      <input
                        value={input.name}
                        onChange={(e) => setDraft({
                          ...draft,
                          inputs: draft.inputs.map((item, i) => (i === index ? { ...item, name: e.target.value } : item)),
                        })}
                        style={{ ...inputStyle, width: 120 }}
                      />
                      <select
                        value={input.type}
                        onChange={(e) => setDraft({
                          ...draft,
                          inputs: draft.inputs.map((item, i) => (i === index ? { ...item, type: e.target.value } : item)),
                        })}
                        style={inputStyle}
                      >
                        {["path", "string", "int", "file", "artifact"].map((type) => (
                          <option key={type} value={type}>{type}</option>
                        ))}
                      </select>
                    </div>
                  ))}
                </div>
                <div>
                  <div style={{ fontSize: 11, color: "var(--text-muted)", fontWeight: 600, marginBottom: 3 }}>Outputs</div>
                  {draft.outputs.map((output, index) => (
                    <div key={index} style={{ display: "flex", gap: 4, marginBottom: 4 }}>
                      <input
                        value={output.name}
                        onChange={(e) => setDraft({
                          ...draft,
                          outputs: draft.outputs.map((item, i) => (i === index ? { ...item, name: e.target.value } : item)),
                        })}
                        style={{ ...inputStyle, width: 120 }}
                      />
                      <select
                        value={output.type}
                        onChange={(e) => setDraft({
                          ...draft,
                          outputs: draft.outputs.map((item, i) => (i === index ? { ...item, type: e.target.value } : item)),
                        })}
                        style={inputStyle}
                      >
                        {["string", "path", "int", "file", "artifact"].map((type) => (
                          <option key={type} value={type}>{type}</option>
                        ))}
                      </select>
                    </div>
                  ))}
                </div>
              </div>
              <label style={labelStyle}>Flow budget — max total tokens (required at publish)
                <div style={{ display: "flex", gap: 6 }}>
                  <input
                    type="number"
                    value={draft.maxTotalTokens}
                    onChange={(e) => setDraft({ ...draft, maxTotalTokens: e.target.value })}
                    style={inputStyle}
                    placeholder="e.g. 600000"
                  />
                  <button
                    type="button"
                    title="Suggested = 1.25× sum of task token budgets (min 10,000)"
                    onClick={() => setDraft({ ...draft, maxTotalTokens: String(suggestedBudget(draft)) })}
                    style={ghostButtonStyle}
                  >
                    Auto
                  </button>
                </div>
              </label>
            </div>

            <div style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 8, fontSize: 11 }}>
              <div style={{ fontWeight: 700, color: "var(--text)", marginBottom: 4 }}>Dependency view</div>
              {draft.tasks.map((task) => (
                <div key={task.id} style={{ padding: "2px 0", color: "var(--text-muted)" }}>
                  {task.depends.length > 0
                    ? <span>{task.name} ← {task.depends.join(", ")}</span>
                    : <span>{task.name} <span style={{ color: "var(--accent)" }}>· entry</span></span>}
                </div>
              ))}
            </div>
          </div>

          {/* Tasks */}
          <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
            {draft.tasks.map((task, index) => {
              const previous = draft.tasks.slice(0, index);
              return (
                <div key={task.id} style={{ border: "1px solid var(--border)", borderRadius: 8, padding: 10, display: "flex", flexDirection: "column", gap: 8 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                    <span style={{ fontSize: 11, color: "var(--text-dim)" }}>#{index + 1}</span>
                    <input
                      value={task.name}
                      onChange={(e) => updateTask(index, (t) => ({ ...t, name: e.target.value.replace(/\s+/g, "-") }))}
                      style={{ ...inputStyle, width: 160, fontWeight: 600 }}
                      placeholder="task name"
                    />
                    {index === 0 && (
                      <span style={{ fontSize: 10, color: "var(--accent)", border: "1px solid var(--accent)", borderRadius: 4, padding: "1px 6px" }}>
                        entry
                      </span>
                    )}
                    <select
                      value={task.kind}
                      onChange={(e) => updateTask(index, (t) => ({ ...t, kind: e.target.value as TaskDraft["kind"] }))}
                      style={{ ...inputStyle, width: 110 }}
                    >
                      <option value="role">role task</option>
                      <option value="check">check task</option>
                      <option value="terminal">terminal</option>
                    </select>
                    <div style={{ flex: 1 }} />
                    <button
                      type="button"
                      title="Remove task"
                      onClick={() => setDraft({ ...draft, tasks: draft.tasks.filter((_, i) => i !== index) })}
                      style={{ ...ghostButtonStyle, color: "#E11D48" }}
                    >
                      Remove
                    </button>
                  </div>
                  {task.kind === "role" && (
                    <>
                      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
                        <label style={labelStyle}>Role (published, pinned version)
                          <select
                            value={task.role}
                            onChange={(e) => updateTask(index, (t) => ({ ...t, role: e.target.value }))}
                            style={inputStyle}
                          >
                            <option value="">Select role…</option>
                            {roles.map((role) => (
                              <option key={role.id} value={`${role.handle}@${role.currentVersion ?? 1}`}>
                                {role.handle}@{role.currentVersion ?? 1} — {role.name}
                              </option>
                            ))}
                          </select>
                        </label>
                        <label style={labelStyle}>Skills (catalog)
                          <select
                            value={task.skills.join(",")}
                            onChange={(e) => updateTask(index, (t) => ({
                              ...t,
                              skills: e.target.value ? e.target.value.split(",") : [],
                            }))}
                            style={inputStyle}
                          >
                            <option value="">None</option>
                            <option value="go-dev">go-dev</option>
                          </select>
                        </label>
                      </div>
                      <label style={labelStyle}>Goal (variables: {"{inputs.x}"}, {"{task.<depends>.output(.field)}"}, {"{flow.vars.y}"} — no {"{prev.*}"})
                        <textarea
                          value={task.goal}
                          onChange={(e) => updateTask(index, (t) => ({ ...t, goal: e.target.value }))}
                          style={{ ...inputStyle, minHeight: 52, fontFamily: "var(--font-mono, monospace)", fontSize: 11 }}
                          placeholder={"Implement {inputs.target} and report changed files"}
                        />
                      </label>
                      <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                        {draft.inputs.map((input) => (
                          <button key={input.name} type="button" onClick={() => updateTask(index, (t) => ({ ...t, goal: `${t.goal}{inputs.${input.name}}` }))} style={chipStyle}>
                            {"{inputs."}{input.name}{"}"}
                          </button>
                        ))}
                        {previous.map((prev) => (
                          <button key={prev.id} type="button" onClick={() => updateTask(index, (t) => ({ ...t, goal: `${t.goal}{task.${prev.name}.output}` }))} style={chipStyle}>
                            {"{task."}{prev.name}{".output}"}
                          </button>
                        ))}
                        <button type="button" onClick={() => updateTask(index, (t) => ({ ...t, goal: `${t.goal}{flow.vars.mode}` }))} style={chipStyle}>
                          {"{flow.vars.mode}"}
                        </button>
                      </div>
                      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
                        <label style={labelStyle}>Depends on (previous tasks)
                          <select
                            multiple
                            value={task.depends}
                            onChange={(e) => updateTask(index, (t) => ({
                              ...t,
                              depends: Array.from(e.target.selectedOptions).map((option) => option.value),
                            }))}
                            style={{ ...inputStyle, minHeight: 60 }}
                          >
                            {previous.map((prev) => (
                              <option key={prev.id} value={prev.name}>{prev.name}</option>
                            ))}
                          </select>
                        </label>
                        <label style={labelStyle}>Budget tokens (optional)
                          <input
                            type="number"
                            value={task.budgetTokens}
                            onChange={(e) => updateTask(index, (t) => ({ ...t, budgetTokens: e.target.value }))}
                            style={inputStyle}
                            placeholder="e.g. 200000"
                          />
                        </label>
                      </div>
                    </>
                  )}
                  {task.kind === "check" && (
                    <label style={labelStyle}>Check command (sandbox allowlist, deterministic)
                      <input
                        value={task.command}
                        onChange={(e) => updateTask(index, (t) => ({ ...t, command: e.target.value }))}
                        style={{ ...inputStyle, fontFamily: "var(--font-mono, monospace)" }}
                        placeholder="go test ./..."
                      />
                    </label>
                  )}
                  {task.kind === "terminal" && (
                    <label style={labelStyle}>Output port (from flow outputs)
                      <select
                        value={task.outputPort}
                        onChange={(e) => updateTask(index, (t) => ({ ...t, outputPort: e.target.value }))}
                        style={inputStyle}
                      >
                        {draft.outputs.map((output) => (
                          <option key={output.name} value={output.name}>{output.name}</option>
                        ))}
                      </select>
                    </label>
                  )}
                  {task.depends.length > 0 && task.kind === "role" && (
                    <div style={{ fontSize: 10, color: "var(--text-dim)" }}>
                      Depends: {task.depends.join(", ")}
                    </div>
                  )}
                </div>
              );
            })}
            <button type="button" onClick={addTask} style={{ ...ghostButtonStyle, alignSelf: "flex-start" }}>
              + Add task
            </button>
          </div>

          {validation && !validation.valid && (
            <div style={{ border: "1px solid #E11D48", borderRadius: 8, padding: 8, display: "flex", flexDirection: "column", gap: 4 }}>
              <div style={{ fontSize: 11, fontWeight: 700, color: "#E11D48" }}>Validation failed — fix before publishing</div>
              {(validation.diagnostics ?? []).map((diagnostic, index) => (
                <div key={index} style={{ fontSize: 11, color: "var(--text-muted)", fontFamily: "var(--font-mono, monospace)" }}>
                  {diagnostic.code}: {diagnostic.message}
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Bindings */}
      <div>
        <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginBottom: 6 }}>Project bindings</div>
        <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
          {bindings.length === 0 && <div style={{ fontSize: 11, color: "var(--text-dim)" }}>No flows bound to this project yet.</div>}
          {bindings.map((binding) => {
            const version = versionFor(binding);
            return (
              <Fragment key={binding.id}>
              <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "6px 10px", border: "1px solid var(--border)", borderRadius: 6 }}>
                <div style={{ minWidth: 0, flex: 1 }}>
                  <span style={{ fontSize: 12, fontWeight: 600, color: "var(--text)" }}>
                    {version ? `${(version.definition as { id?: string } | undefined)?.id ?? "flow"} v${version.version}` : "flow"}
                  </span>
                  <span style={{ fontSize: 10, color: "var(--text-dim)", marginLeft: 6 }}>rev {binding.revision ?? 0}</span>
                </div>
                <button
                  type="button"
                  onClick={() => updateBinding(binding, !binding.desiredEnabled)}
                  style={{
                    padding: "4px 10px", borderRadius: 6, fontSize: 11, border: "1px solid var(--border)",
                    background: binding.desiredEnabled ? "var(--accent)" : "var(--bg)",
                    color: binding.desiredEnabled ? "#fff" : "var(--text)",
                    cursor: "pointer",
                  }}
                >
                  {binding.desiredEnabled ? "Enabled" : "Disabled"}
                </button>
                {runPanelFor === binding.id ? (
                  <button type="button" onClick={() => setRunPanelFor(null)} style={ghostButtonStyle}>
                    Cancel
                  </button>
                ) : (
                  <button
                    type="button"
                    disabled={!binding.desiredEnabled}
                    onClick={() => {
                      setRunSessionId(sessions.find((s) => s.status === "active")?.id ?? "");
                      setRunPanelFor(binding.id ?? null);
                    }}
                    style={ghostButtonStyle}
                  >
                    Run
                  </button>
                )}
              </div>
              {runPanelFor === binding.id && (
                <div
                  style={{
                    borderTop: "1px solid var(--border)", marginTop: 6, paddingTop: 8,
                    display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap",
                  }}
                >
                  <span style={{ fontSize: 11, color: "var(--text-dim)" }}>Run in session</span>
                  <select
                    value={runSessionId}
                    onChange={(e) => setRunSessionId(e.target.value)}
                    style={{ ...inputStyle, width: 220, padding: "4px 8px", fontSize: 11 }}
                    aria-label="Target session for this flow run"
                  >
                    {sessions.length === 0 && <option value="">No active sessions — open one in the sidebar first</option>}
                    {sessions.map((session) => (
                      <option key={session.id} value={session.id}>
                        {session.title || "Untitled"} · {session.id.slice(0, 8)}
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    disabled={!runSessionId}
                    onClick={() => runFlow(binding, runSessionId)}
                    style={{
                      padding: "4px 12px", borderRadius: 6, fontSize: 11, border: "none",
                      background: "var(--accent)", color: "#fff", cursor: "pointer",
                    }}
                  >
                    Start run
                  </button>
                </div>
              )}
              </Fragment>
          );
        })}
        </div>
      </div>

      {/* Candidates */}
      {(candidates.length > 0 || profiles.length > 0) && (
        <div>
          <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginBottom: 6 }}>Discovered flows</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {candidates.map((candidate) => (
              <div key={`${candidate.sourceKind}:${candidate.slug}`} style={{ display: "flex", flexDirection: "column", gap: 4 }}>
              <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8, padding: "6px 10px", border: "1px solid var(--border)", borderRadius: 6 }}>
                <div style={{ minWidth: 0 }}>
                  <span style={{ fontSize: 12, fontWeight: 600, color: "var(--text)" }}>{candidate.name ?? candidate.slug}</span>
                  <span style={{ fontSize: 10, color: "var(--text-dim)", marginLeft: 6 }}>
                    {candidate.sourceKind} · {candidate.taskCount ?? 0} tasks
                  </span>
                  {candidate.parseError && (
                    <div style={{ fontSize: 10, color: "#E11D48" }}>parse error: {candidate.parseError}</div>
                  )}
                </div>
                <div style={{ display: "flex", gap: 6 }}>
                  {candidate.alreadyBound ? (
                    <span style={{ fontSize: 10, color: "var(--text-dim)", border: "1px solid var(--border)", borderRadius: 4, padding: "2px 8px" }}>
                      {candidate.updateAvailable ? "Update available" : "Bound"}
                    </span>
                  ) : (
                    <button type="button" onClick={() => bindCandidate(candidate)} style={ghostButtonStyle}>
                      Bind
                    </button>
                  )}
                  {candidate.updateAvailable && (
                    <button
                      type="button"
                      title="Show the read-only diff between the bound version and the project file"
                      onClick={() => {
                        let bound;
                        for (const list of Object.values(versions)) {
                          bound = list.find((version) => version.id === candidate.boundVersionId);
                          if (bound) break;
                        }
                        const oldText = JSON.stringify(bound?.definition ?? {}, null, 2);
                        const newText = JSON.stringify(candidate.definition ?? {}, null, 2);
                        setDiffView({ slug: candidate.slug ?? "", lines: diffText(oldText, newText) });
                      }}
                      style={ghostButtonStyle}
                    >
                      View diff
                    </button>
                  )}
                </div>
              </div>
              {diffView && diffView.slug === candidate.slug && (
                <div style={{ border: "1px solid var(--border)", borderRadius: 6, padding: 8, marginTop: 4 }}>
                  <div style={{ fontSize: 11, fontWeight: 700, color: "var(--text)", marginBottom: 4 }}>
                    Read-only diff: bound version vs project file
                  </div>
                  <div style={{ maxHeight: 200, overflowY: "auto", fontSize: 10, fontFamily: "var(--font-mono, monospace)", color: "var(--text-muted)", whiteSpace: "pre-wrap" }}>
                    {diffView.lines.map((line, index) => (
                      <div key={index} style={{ color: line.startsWith("- ") ? "#E11D48" : line.startsWith("+ ") ? "#059669" : undefined }}>
                        {line}
                      </div>
                    ))}
                  </div>
                </div>
              )}
              </div>
            ))}
            {profiles.filter((profile) => profile.sourceKind === "managed" && !bindings.some((binding) => versionFor(binding)?.profileId === profile.id)).map((profile) => (
              <div key={profile.id} style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8, padding: "6px 10px", border: "1px solid var(--border)", borderRadius: 6 }}>
                <div style={{ minWidth: 0 }}>
                  <span style={{ fontSize: 12, fontWeight: 600, color: "var(--text)" }}>{profile.name}</span>
                  <span style={{ fontSize: 10, color: "var(--text-dim)", marginLeft: 6 }}>{profile.slug} · v{profile.latestVersion ?? 0} · managed</span>
                </div>
                <div style={{ display: "flex", gap: 6 }}>
                  <button type="button" onClick={() => openEditor(profile)} style={ghostButtonStyle}>
                    Edit
                  </button>
                  <button
                    type="button"
                    title="Copy draft YAML"
                    onClick={() => profile.id && exportFlow(profile.id, "draft")}
                    style={ghostButtonStyle}
                  >
                    Export
                  </button>
                  {(versions[profile.id!] ?? []).map((version) => (
                    <button key={version.id} type="button" onClick={() => bindVersion(version)} style={ghostButtonStyle}>
                      Bind v{version.version}
                    </button>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Check approvals */}
      {approvals.length > 0 && (
        <div>
          <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginBottom: 6 }}>Pending check approvals</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {approvals.map((approval) => (
              <div key={`${approval.runId}:${approval.taskIndex}`} style={{ display: "flex", alignItems: "center", gap: 8, padding: "6px 10px", border: "1px solid var(--border)", borderRadius: 6 }}>
                <code style={{ fontSize: 11, color: "var(--text)", flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {approval.command}
                </code>
                <span style={{ fontSize: 10, color: "var(--text-dim)" }}>task {approval.taskIndex}</span>
                <button type="button" onClick={() => decideApproval(approval, true)} style={{ ...ghostButtonStyle, color: "#059669" }}>Approve</button>
                <button type="button" onClick={() => decideApproval(approval, false)} style={{ ...ghostButtonStyle, color: "#E11D48" }}>Reject</button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Runs + timeline */}
      <div>
        <div style={{ fontSize: 12, fontWeight: 700, color: "var(--text)", marginBottom: 6 }}>Runs</div>
        <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
          {runs.length === 0 && <div style={{ fontSize: 11, color: "var(--text-dim)" }}>No flow runs yet.</div>}
          {runs.map((run) => {
            const runId = run.runId ?? "";
            return (
            <div key={runId} style={{ border: "1px solid var(--border)", borderRadius: 6, padding: "6px 10px", display: "flex", flexDirection: "column", gap: 4 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <span style={{ fontSize: 11, fontFamily: "var(--font-mono, monospace)", color: "var(--text-muted)" }}>{runId.slice(0, 8)}</span>
                <span
                  style={{
                    fontSize: 10, padding: "1px 8px", borderRadius: 4, border: "1px solid var(--border)",
                    color: run.state === "completed" ? "#059669" : run.state === "cancelled" || run.state === "failed" || run.state === "budget_exceeded" ? "#E11D48" : "var(--text)",
                  }}
                >
                  {run.state}
                </span>
                <span style={{ fontSize: 10, color: "var(--text-dim)" }}>{run.totalTokensUsed ?? 0} tokens</span>
                <div style={{ flex: 1 }} />
                <button type="button" onClick={() => openRunDetail(run)} style={ghostButtonStyle}>
                  Timeline
                </button>
                {runId && !["completed", "failed", "cancelled", "budget_exceeded", "convergence_exceeded"].includes(run.state ?? "") && (
                  <button type="button" onClick={() => cancelRun(run)} style={{ ...ghostButtonStyle, color: "#E11D48" }}>
                    Cancel
                  </button>
                )}
                {run.state === "cancelled" && runId && (
                  <button type="button" onClick={() => resumeRun(run)} style={{ ...ghostButtonStyle, color: "#059669" }}>
                    Resume
                  </button>
                )}
              </div>
              {detail?.run.runId === runId && (
                <div style={{ borderTop: "1px solid var(--border)", paddingTop: 6 }}>
                  {(detail?.nodes?.length ?? 0) > 0 && (() => {
                    const nodes = detail?.nodes ?? [];
                    const counts = nodes.reduce<Record<string, number>>((acc, node) => {
                      acc[node.terminalState ?? "pending"] = (acc[node.terminalState ?? "pending"] ?? 0) + 1;
                      return acc;
                    }, {});
                    const summary = nodes.length === 0
                      ? ""
                      : `${nodes.length} task${nodes.length > 1 ? "s" : ""}` +
                        (counts.completed ? ` · ${counts.completed} done` : "") +
                        (counts.running ? ` · ${counts.running} running` : "") +
                        (counts.failed || counts.blocked ? ` · ${(counts.failed ?? 0) + (counts.blocked ?? 0)} failed` : "");
                    return (
                      <>
                        <div style={{ fontSize: 11, fontWeight: 700, color: "var(--text)", marginBottom: 4 }}>
                          Task checkpoints
                          {summary && <span style={{ fontWeight: 400, color: "var(--text-dim)", marginLeft: 6 }}>{summary}</span>}
                        </div>
                        <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
                          {nodes.map((node) => {
                            const state = node.terminalState ?? "pending";
                            const stateColor =
                              state === "completed" ? "#059669"
                              : state === "running" ? "var(--accent)"
                              : state === "failed" || state === "blocked" || state === "cancelled" || state === "interrupted" ? "#E11D48"
                              : "var(--text-dim)";
                            const output = node.outputRef ? JSON.stringify(node.outputRef) : null;
                            return (
                              <div key={node.taskIndex} style={{ border: "1px solid var(--border)", borderRadius: 6, padding: "6px 8px", display: "flex", flexDirection: "column", gap: 3 }}>
                                <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
                                  <span style={{ fontSize: 10, color: "var(--text-dim)", fontFamily: "var(--font-mono, monospace)" }}>
                                    t{node.taskIndex}
                                  </span>
                                  <span style={{ fontSize: 12, fontWeight: 600, color: "var(--text)" }}>{node.handle}</span>
                                  <span style={{ fontSize: 10, padding: "0 6px", borderRadius: 8, border: `1px solid ${stateColor}`, color: stateColor }}>
                                    {state}
                                  </span>
                                  {node.childRunId && <span style={{ fontSize: 10, color: "var(--text-dim)", fontFamily: "var(--font-mono, monospace)" }}>{node.childRunId.slice(0, 8)}</span>}
                                </div>
                                {node.goalText && (
                                  <div style={{ fontSize: 10, color: "var(--text-muted)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 520 }}>
                                    {node.goalText}
                                  </div>
                                )}
                                {node.errorCode && <div style={{ fontSize: 10, color: "#E11D48", fontFamily: "var(--font-mono, monospace)" }}>{node.errorCode}</div>}
                                {output && (
                                  <pre style={{ fontSize: 10, margin: 0, color: "var(--text-muted)", fontFamily: "var(--font-mono, monospace)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: 560 }}>
                                    {output}
                                  </pre>
                                )}
                              </div>
                            );
                          })}
                        </div>
                      </>
                    );
                  })()}
                  {timeline.length > 0 && (
                    <div style={{ fontSize: 10, color: "var(--text-dim)", marginTop: 4, display: "flex", flexWrap: "wrap", gap: "2px 8px" }}>
                      {timeline.map((eventType, index) => (
                        <span
                          key={index}
                          style={{
                            color: eventType.includes("check_result") ? "var(--accent)"
                              : eventType.includes("cancelled") || eventType.includes("convergence_exceeded") || eventType.includes("budget_exceeded") ? "#E11D48"
                              : eventType.includes("completed") ? "#059669"
                              : undefined,
                          }}
                        >
                          {eventType.replace(/^flow_/, "").replace(/_/g, " ")}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
              )}
            </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

const labelStyle: React.CSSProperties = {
  display: "flex", flexDirection: "column", gap: 3,
  fontSize: 11, color: "var(--text-muted)", fontWeight: 600,
};

const inputStyle: React.CSSProperties = {
  padding: "6px 8px", borderRadius: 6, border: "1px solid var(--border)",
  background: "var(--bg)", color: "var(--text)", fontSize: 12, width: "100%",
};

const ghostButtonStyle: React.CSSProperties = {
  padding: "4px 8px", borderRadius: 6, fontSize: 11,
  border: "1px solid var(--border)", background: "var(--bg)",
  color: "var(--text-muted)", cursor: "pointer",
};

const chipStyle: React.CSSProperties = {
  padding: "2px 8px", borderRadius: 10, fontSize: 10,
  border: "1px dashed var(--border)", background: "var(--bg)",
  color: "var(--text-muted)", cursor: "pointer",
};
