"use client";

import { AlertTriangle, Info, Plus, Save, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type TemplateSummary = components["schemas"]["PromptTemplateSummary"];
type TemplateDiagnostic = components["schemas"]["PromptTemplateDiagnostic"];
type GlobalTemplateEntry = components["schemas"]["GlobalPromptTemplateEntry"];
type ManagementCatalog = components["schemas"]["PromptTemplateManagementCatalog"];
type ProjectCatalog = { templates: TemplateSummary[]; diagnostics: TemplateDiagnostic[] };

interface TemplatesSettingsProps {
  projectId: string | null;
  setError: (msg: string | null) => void;
}

function fetchTemplateCatalogs(projectId: string | null) {
  return Promise.all([
    apiFetch<ManagementCatalog>("/v1/prompt-templates").catch(() => null),
    projectId
      ? apiFetch<ProjectCatalog>(`/v1/projects/${encodeURIComponent(projectId)}/prompt-templates`).catch(() => null)
      : Promise.resolve(null),
  ]);
}

export function TemplatesSettings({ projectId, setError }: TemplatesSettingsProps) {
  const [mgmt, setMgmt] = useState<ManagementCatalog | null>(null);
  const [project, setProject] = useState<ProjectCatalog | null>(null);
  const [loadedProjectId, setLoadedProjectId] = useState<string | null | undefined>(undefined);

  // Create form state.
  const [showCreate, setShowCreate] = useState(false);
  const [formName, setFormName] = useState("");
  const [formDesc, setFormDesc] = useState("");
  const [formHint, setFormHint] = useState("");
  const [formBody, setFormBody] = useState("");
  const [saving, setSaving] = useState(false);

  // Edit/repair state per template name.
  const [editing, setEditing] = useState<string | null>(null);
  const [editDesc, setEditDesc] = useState("");
  const [editHint, setEditHint] = useState("");
  const [editBody, setEditBody] = useState("");

  // Expand/collapse body preview.
  const [expandedBody, setExpandedBody] = useState<string | null>(null);
  // Inline delete confirmation (per name).
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [mgmtData, projData] = await fetchTemplateCatalogs(projectId);
      setMgmt(mgmtData);
      setProject(projData);
      setLoadedProjectId(projectId);
      setError(null);
    } catch {
      setLoadedProjectId(projectId);
      setError("Failed to load prompt templates");
    }
  }, [projectId, setError]);

  useEffect(() => {
    let cancelled = false;
    void fetchTemplateCatalogs(projectId)
      .then(([mgmtData, projData]) => {
        if (cancelled) return;
        setMgmt(mgmtData);
        setProject(projData);
        setLoadedProjectId(projectId);
        setError(null);
      })
      .catch(() => {
        if (cancelled) return;
        setLoadedProjectId(projectId);
        setError("Failed to load prompt templates");
      });
    return () => { cancelled = true; };
  }, [projectId, setError]);

  const loading = loadedProjectId !== projectId;
  const displayCatalog = project ?? mgmt;

  // ——— CRUD ———

  async function doCreate(e: FormEvent) {
    e.preventDefault();
    if (!formName.trim() || !formBody.trim()) return;
    setSaving(true);
    try {
      await apiFetch("/v1/prompt-templates", {
        method: "POST",
        body: JSON.stringify({
          name: formName.trim(),
          description: formDesc.trim(),
          argumentHint: formHint.trim(),
          body: formBody,
        }),
      });
      setFormName(""); setFormDesc(""); setFormHint(""); setFormBody("");
      setShowCreate(false);
      await load();
    } catch (err) { setError((err as Error).message); }
    finally { setSaving(false); }
  }

  async function doDelete(name: string) {
    try {
      await apiFetch(`/v1/prompt-templates/${encodeURIComponent(name)}`, { method: "DELETE" });
      setConfirmDelete(null);
      await load();
    } catch (err) { setError((err as Error).message); }
  }

  function startEdit(entry: GlobalTemplateEntry) {
    setEditing(entry.name);
    if (entry.valid) {
      // Valid entry: load full body via named GET.
      apiFetch<{ name: string; description: string; argumentHint: string; body: string }>(
        `/v1/prompt-templates/${encodeURIComponent(entry.name)}`,
      ).then((t) => {
        setEditDesc(t.description);
        setEditHint(t.argumentHint);
        setEditBody(t.body);
      }).catch(() => {});
    } else {
      // Invalid entry: open repair form with empty fields.
      setEditDesc("");
      setEditHint("");
      setEditBody("");
    }
  }

  async function doSaveEdit(name: string) {
    if (!editBody.trim()) return;
    setSaving(true);
    try {
      await apiFetch(`/v1/prompt-templates/${encodeURIComponent(name)}`, {
        method: "PUT",
        body: JSON.stringify({
          description: editDesc.trim(),
          argumentHint: editHint.trim(),
          body: editBody,
        }),
      });
      setEditing(null);
      await load();
    } catch (err) { setError((err as Error).message); }
    finally { setSaving(false); }
  }

  // ——— render ———

  if (loading && !mgmt) return <div className="settings-tab-section"><p>Loading…</p></div>;

  const diagnostics = [...(mgmt?.diagnostics ?? []), ...(project?.diagnostics ?? [])];
  const recoveryMode = mgmt?.globalRecoveryMode ?? false;

  return (
    <section className="settings-tab-section" aria-labelledby="settings-templates-heading">
      <header><h2 id="settings-templates-heading">Prompt templates</h2>
        <p>Manage slash-command prompt templates across builtin, settings, global, and project sources.</p></header>

      {/* Diagnostics banner */}
      {diagnostics.length > 0 && (
        <div className="templates-diag-banner" role="status">
          {diagnostics.map((d, i) => (
            <div key={i} className={`templates-diag ${d.level === "warning" ? "diag-warn" : "diag-info"}`}>
              <span className="templates-diag-icon">
                {d.level === "warning" ? <AlertTriangle size={14} /> : <Info size={14} />}
              </span>
              <span className="templates-diag-msg">
                <code>{d.code}</code> {d.message}
              </span>
            </div>
          ))}
        </div>
      )}

      {/* Effective templates */}
      {displayCatalog && (
        <>
          <header className="templates-section-header">
            <h3>Effective templates {projectId ? "(project)" : "(global)"}</h3>
          </header>
          {displayCatalog.templates.length === 0 ? (
            <p className="templates-empty">No templates available. Create a global template below or add markdown files to your project&apos;s <code>.ennote/prompts/</code> directory.</p>
          ) : (
            <ul className="templates-list">
              {displayCatalog.templates.map((t) => (
                <li key={t.name} className="template-row">
                  <span className="template-name">/{t.name}</span>
                  {t.argumentHint && <span className="template-hint">{t.argumentHint}</span>}
                  <span className="template-source source-{t.source}">{t.source}</span>
                  <span className="template-desc">{t.description}</span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}

      {/* Global templates management */}
      <header className="templates-section-header">
        <h3>My global templates</h3>
        {!recoveryMode && (
          <button type="button" className="settings-btn" onClick={() => setShowCreate(!showCreate)}>
            <Plus size={14} /> New
          </button>
        )}
      </header>

      {recoveryMode && (
        <div className="templates-diag diag-warn" style={{ marginBottom: 12 }}>
          <AlertTriangle size={14} />
          <span>Global store is in recovery mode. Delete some entries to restore normal operation. Create is disabled.</span>
        </div>
      )}

      {/* Create form */}
      {showCreate && (
        <form className="template-edit-form" onSubmit={doCreate}>
          <label>Name <input value={formName} onChange={(e) => setFormName(e.target.value)} placeholder="my-command" required disabled={saving} /></label>
          <label>Description <input value={formDesc} onChange={(e) => setFormDesc(e.target.value)} placeholder="Optional" disabled={saving} /></label>
          <label>Argument hint <input value={formHint} onChange={(e) => setFormHint(e.target.value)} placeholder="&lt;path&gt; [focus]" disabled={saving} /></label>
          <label>Body <textarea value={formBody} onChange={(e) => setFormBody(e.target.value)} rows={4} placeholder="Template body with $1, $@, etc." required disabled={saving} /></label>
          <div className="template-form-actions">
            <button type="submit" className="settings-btn primary" disabled={saving || !formName.trim() || !formBody.trim()}>
              <Save size={14} /> {saving ? "Saving…" : "Create"}
            </button>
            <button type="button" className="settings-btn" onClick={() => setShowCreate(false)}>Cancel</button>
          </div>
        </form>
      )}

      {/* Global entries list */}
      {mgmt?.globalTemplates && mgmt.globalTemplates.length > 0 && (
        <ul className="templates-list">
          {mgmt.globalTemplates.map((entry) => (
            <li key={entry.name} className={`template-row ${!entry.valid ? "invalid" : ""}`}>
              <span className="template-name">/{entry.name}</span>
              {entry.argumentHint && <span className="template-hint">{entry.argumentHint}</span>}
              {entry.valid ? (
                <span className="template-desc">{entry.description}</span>
              ) : (
                <span className="template-desc diag-text">
                  {entry.diagnostic?.code}: {entry.diagnostic?.message}
                </span>
              )}

              <div className="template-actions">
                {entry.editable && editing === entry.name ? (
                  // Edit/repair inline form.
                  <div className="template-edit-form" style={{ width: "100%", marginTop: 6 }}>
                    {entry.valid && <div className="template-edit-readonly">Name: {entry.name}</div>}
                    <label>Description <input value={editDesc} onChange={(e) => setEditDesc(e.target.value)} disabled={saving} /></label>
                    <label>Argument hint <input value={editHint} onChange={(e) => setEditHint(e.target.value)} disabled={saving} /></label>
                    <label>Body <textarea value={editBody} onChange={(e) => setEditBody(e.target.value)} rows={4} disabled={saving} /></label>
                    <div className="template-form-actions">
                      <button type="button" className="settings-btn primary" onClick={() => doSaveEdit(entry.name)} disabled={saving || !editBody.trim()}>
                        <Save size={14} /> {saving ? "Saving…" : entry.valid ? "Update" : "Repair"}
                      </button>
                      <button type="button" className="settings-btn" onClick={() => setEditing(null)}>Cancel</button>
                    </div>
                  </div>
                ) : entry.editable ? (
                  confirmDelete === entry.name ? (
                    <span className="template-confirm">
                      <span>Delete /{entry.name}?</span>
                      <button type="button" className="settings-btn danger" onClick={() => doDelete(entry.name)} disabled={saving}>Delete</button>
                      <button type="button" className="settings-btn" onClick={() => setConfirmDelete(null)}>Cancel</button>
                    </span>
                  ) : (
                    <>
                      <button type="button" className="settings-btn" onClick={() => startEdit(entry)}>
                        {entry.valid ? "Edit" : "Repair"}
                      </button>
                      <button type="button" className="settings-btn danger" onClick={() => setConfirmDelete(entry.name)}>
                        <Trash2 size={14} />
                      </button>
                    </>
                  )
                ) : (
                  <span className="template-manual-cleanup">Manual cleanup required in $ENNOTE_HOME/prompts/</span>
                )}
              </div>

              {/* Body preview for expanded entries */}
              {entry.valid && expandedBody === entry.name && (
                <LoadBodyPreview name={entry.name} onClose={() => setExpandedBody(null)} />
              )}
            </li>
          ))}
        </ul>
      )}

      {(!mgmt?.globalTemplates || mgmt.globalTemplates.length === 0) && !recoveryMode && (
        <p className="templates-empty">No global templates yet. Create one above.</p>
      )}
    </section>
  );
}

function LoadBodyPreview({ name, onClose }: { name: string; onClose: () => void }) {
  const [body, setBody] = useState<string | null>(null);
  useEffect(() => {
    apiFetch<{ body: string }>(`/v1/prompt-templates/${encodeURIComponent(name)}`)
      .then((t) => setBody(t.body))
      .catch(() => setBody("Failed to load"));
  }, [name]);
  if (body === null) return <div className="template-body-preview">Loading…</div>;
  return (
    <div className="template-body-preview">
      <pre>{body}</pre>
      <button type="button" className="settings-btn" onClick={onClose}>Close</button>
    </div>
  );
}
