"use client";

import { Bot, ImagePlus, ListPlus, Minimize2, Paperclip, Plus } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type DragEvent, type FormEvent, type KeyboardEvent } from "react";
import { RoleTargetPicker } from "@/components/RoleTargetPicker";
import type { ModelProfile, RoleSummary } from "@/components/settings/types";
import { artifactPreviewURL } from "@/lib/artifacts";
import type { PermissionMode, ThinkingEffort } from "@/lib/permission-mode";
import { PromptCommandMenu } from "./PromptCommandMenu";

export interface TextAttachment {
  id: string;
  name: string;
  size: number;
  text: string;
}

export interface PendingImage {
  id: string;
  name: string;
  mimeType: string;
  width?: number;
  height?: number;
}

const ATTACHMENT_DOC_ICONS: Record<string, { emoji: string; color: string }> = {
  pdf: { emoji: "📄", color: "#ef4444" },
  docx: { emoji: "📝", color: "#3b82f6" },
  doc: { emoji: "📝", color: "#3b82f6" },
  xlsx: { emoji: "📊", color: "#22c55e" },
  xls: { emoji: "📊", color: "#22c55e" },
  pptx: { emoji: "📽", color: "#f97316" },
  ppt: { emoji: "📽", color: "#f97316" },
};

function docAttachmentIcon(name: string): { emoji: string; color: string } {
  const ext = name.toLowerCase().split(".").pop() ?? "";
  return ATTACHMENT_DOC_ICONS[ext] ?? { emoji: "📎", color: "var(--text-muted)" };
}

function formatAttachmentSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

interface ComposerProps {
  selectedSession: string | null;
  activeLeafMessageId?: string;
  input: string;
  setInput: (value: string) => void;
  activeRun: boolean;
  compacting: boolean;
  hasPendingImage: boolean;
  reconnecting: boolean;
  permissionMode: PermissionMode;
  permissionReady: boolean;
  setPermissionMode: (mode: PermissionMode) => void;
  models: ModelProfile[];
  selectedModelId: string | null;
  setSelectedModelId: (modelId: string) => void;
  thinkingEffort: ThinkingEffort;
  setThinkingEffort: (effort: ThinkingEffort) => void;
  roles: RoleSummary[];
  selectedRoleId: string | null;
  setSelectedRoleId: (roleId: string | null) => void;
  textAttachments: TextAttachment[];
  removeTextAttachment: (id: string) => void;
  pendingImage: PendingImage | null;
  clearPendingImage: () => void;
  attachFiles: (files: File[]) => void;
  uploadImage: (file: File) => void;
  submit: () => void;
  steer: () => void;
  followUp: () => void;
  cancel: () => void;
  pendingFollowUps: { id: string; text: string }[];
  compactSession: () => void;
  // Prompt templates + @addressing panel.
  promptTemplates: { name: string; description: string; argumentHint: string; source: string; editable: boolean }[];
  panelRoles: { id: string; handle: string; name: string; description?: string }[];
  panelFlows: { name: string; version?: number; description?: string }[];
  showPromptPanel: boolean;
  onPromptSelect: (name: string) => void;
  onRoleSelect: (roleId: string, handle: string) => void;
  onFlowSelect: (name: string, version?: number) => void;
  onPromptPanelClose: () => void;
  expanding: boolean;
  expandDiag: string | null;
}

export function Composer({
  selectedSession, activeLeafMessageId, input, setInput, activeRun, compacting, hasPendingImage, reconnecting,
  permissionMode, permissionReady, setPermissionMode, models, selectedModelId, setSelectedModelId, thinkingEffort, setThinkingEffort,
  roles, selectedRoleId, setSelectedRoleId, textAttachments, removeTextAttachment, pendingImage, clearPendingImage, attachFiles, uploadImage, submit, steer, followUp, cancel, compactSession,
  pendingFollowUps,
  promptTemplates, showPromptPanel, onPromptSelect, onPromptPanelClose, expanding, expandDiag,
  panelRoles, panelFlows, onRoleSelect, onFlowSelect,
}: ComposerProps) {
  const textarea = useRef<HTMLTextAreaElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const imageInput = useRef<HTMLInputElement>(null);
  const [isDragOver, setIsDragOver] = useState(false);
  const dragCounter = useRef(0);
  const [configOpen, setConfigOpen] = useState(false);
  const configRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!configOpen) return;
    const close = (event: PointerEvent) => {
      if (!configRef.current?.contains(event.target as Node)) setConfigOpen(false);
    };
    const onKey = (event: globalThis.KeyboardEvent) => { if (event.key === "Escape") setConfigOpen(false); };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", onKey);
    };
  }, [configOpen]);

  const configDot = Boolean(selectedRoleId) || permissionMode !== "discuss";

  useEffect(() => {
    const element = textarea.current;
    if (!element) return;
    element.style.height = "0px";
    element.style.height = `${Math.min(element.scrollHeight, 176)}px`;
  }, [input]);

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (activeRun) {
      if (!compacting && !reconnecting) steer();
    } else {
      submit();
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    form.current?.requestSubmit();
  }

  const canAttach = Boolean(selectedSession) && !activeRun && !compacting;
  const handleDragEnter = useCallback((event: DragEvent) => {
    if (!canAttach || !event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    dragCounter.current += 1;
    if (dragCounter.current === 1) setIsDragOver(true);
  }, [canAttach]);
  const handleDragLeave = useCallback((event: DragEvent) => {
    if (!isDragOver) return;
    event.preventDefault();
    dragCounter.current -= 1;
    if (dragCounter.current <= 0) {
      dragCounter.current = 0;
      setIsDragOver(false);
    }
  }, [isDragOver]);
  const handleDragOver = useCallback((event: DragEvent) => {
    if (!canAttach) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
  }, [canAttach]);
  const handleDrop = useCallback((event: DragEvent) => {
    if (!canAttach) return;
    event.preventDefault();
    dragCounter.current = 0;
    setIsDragOver(false);
    const files = Array.from(event.dataTransfer.files);
    if (files.length) attachFiles(files);
  }, [attachFiles, canAttach]);

  return (
    <div className="composer-shell" onDragEnter={handleDragEnter} onDragLeave={handleDragLeave} onDragOver={handleDragOver} onDrop={handleDrop}>
      {isDragOver && <div className="composer-drop-zone"><Paperclip size={18} />Drop images or text files</div>}

      {(pendingImage || textAttachments.length > 0) && (
        <div className="composer-attachments" aria-label="Attached files">
          {pendingImage && selectedSession && (
            <div className="composer-image-preview">
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img src={artifactPreviewURL(selectedSession, pendingImage.id)} alt={pendingImage.name} />
              <button type="button" onClick={clearPendingImage} aria-label="Remove image">
                <svg width="8" height="8" viewBox="0 0 8 8" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden="true">
                  <line x1="1" y1="1" x2="7" y2="7" /><line x1="7" y1="1" x2="1" y2="7" />
                </svg>
              </button>
            </div>
          )}
          {textAttachments.map((attachment) => {
            const icon = docAttachmentIcon(attachment.name);
            const ext = attachment.name.toLowerCase().split(".").pop() ?? "";
            return (
              <span className="composer-attachment" key={attachment.id} title={attachment.name}>
                <span className="composer-attachment-icon" style={{ color: icon.color }}>{icon.emoji}</span>
                <span className="composer-attachment-body">
                  <span className="composer-attachment-name">{attachment.name}</span>
                  <span className="composer-attachment-meta">{ext.toUpperCase()} · {formatAttachmentSize(attachment.size)} · text</span>
                </span>
                <button type="button" onClick={() => removeTextAttachment(attachment.id)} aria-label={`Remove ${attachment.name}`}>
                  <svg width="7" height="7" viewBox="0 0 7 7" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden="true">
                    <line x1="1" y1="1" x2="6" y2="6" /><line x1="6" y1="1" x2="1" y2="6" />
                  </svg>
                </button>
              </span>
            );
          })}
        </div>
      )}

      <form className="composer" onSubmit={onSubmit} ref={form}>
        <div className="composer-editor" ref={configRef}>
          {configOpen && (
            <div className="composer-config-panel" role="dialog" aria-label="Run configuration">
              <div className="composer-config-row">
                <span className="composer-config-label">Target</span>
                <RoleTargetPicker roles={roles} selectedRoleId={selectedRoleId} onSelect={setSelectedRoleId}
                  disabled={!selectedSession || activeRun || compacting} />
              </div>
              <div className="composer-config-row">
                <span className="composer-config-label">Permission</span>
                <div className="permission-segment" role="group" aria-label="Permission mode">
                  {(["discuss", "ask", "auto"] as PermissionMode[]).map((mode) => (
                    <button
                      key={mode}
                      type="button"
                      aria-pressed={permissionMode === mode}
                      disabled={!selectedSession || activeRun || !permissionReady || Boolean(selectedRoleId)}
                      onClick={() => setPermissionMode(mode)}
                    >
                      {mode === "discuss" ? "Discuss" : mode === "ask" ? "Ask" : "Auto"}
                    </button>
                  ))}
                </div>
              </div>
              <div className="composer-config-row">
                <span className="composer-config-label">Attach</span>
                <div className="composer-config-tools">
                  <button type="button" className="icon-command" disabled={!canAttach} onClick={() => fileInput.current?.click()} aria-label="Attach files" title="Attach image or text files">
                    <Paperclip size={15} aria-hidden="true" />
                  </button>
                  <button type="button" className="icon-command" disabled={!canAttach} onClick={() => imageInput.current?.click()} aria-label="Attach image" title="Attach image">
                    <ImagePlus size={15} aria-hidden="true" />
                  </button>
                  <button type="button" className="icon-command" onClick={compactSession} disabled={!selectedSession || !activeLeafMessageId || activeRun} aria-label="Compact context" title="Compact context">
                    <Minimize2 size={15} aria-hidden="true" />
                  </button>
                </div>
              </div>
            </div>
          )}
          {showPromptPanel && (
            <PromptCommandMenu
              templates={promptTemplates}
              roles={panelRoles}
              flows={panelFlows}
              input={input}
              onSelectTemplate={onPromptSelect}
              onSelectRole={onRoleSelect}
              onSelectFlow={onFlowSelect}
              onClose={onPromptPanelClose}
            />
          )}
          <textarea
            ref={textarea}
            value={input}
            onChange={(event) => setInput(event.target.value)}
            onKeyDown={onKeyDown}
            rows={1}
            placeholder={compacting ? "Context compaction in progress" : activeRun ? "Steer the agent… (Enter)" : !selectedSession ? "Select a project and session" : "Type a message…"}
            disabled={!selectedSession || compacting}
            aria-label={activeRun ? "Steer the agent" : "Message the agent"}
          />
          <input
            ref={imageInput}
            className="sr-only"
            type="file"
            accept="image/png,image/jpeg,image/gif,image/webp"
            disabled={!canAttach}
            onChange={(event) => {
              const file = event.target.files?.[0];
              if (file) uploadImage(file);
              event.currentTarget.value = "";
            }}
          />
          <input
            ref={fileInput}
            className="sr-only"
            type="file"
            multiple
            accept="image/png,image/jpeg,image/gif,image/webp,text/*,.md,.mdx,.json,.yaml,.yml,.toml,.csv,.tsv,.js,.jsx,.ts,.tsx,.py,.r,.go,.rs,.java,.c,.cpp,.h,.hpp,.css,.html,.xml,.sh,.sql"
            disabled={!canAttach}
            onChange={(event) => {
              const files = Array.from(event.currentTarget.files ?? []);
              if (files.length) attachFiles(files);
              event.currentTarget.value = "";
            }}
          />
          <div className="composer-toolbar-row">
            <div className="composer-tools">
              <button
                type="button"
                className="composer-plus"
                aria-expanded={configOpen}
                aria-label="Configure run"
                title="Configure run"
                disabled={!selectedSession || compacting}
                onClick={() => setConfigOpen((value) => !value)}
              >
                <Plus size={15} aria-hidden="true" />
                {configDot && <span className="composer-plus-dot" aria-hidden="true" />}
              </button>
              {selectedSession && (
                <>
                  <span
                    className="composer-tag"
                    title="Permission mode"
                    aria-label={`Permission mode: ${permissionMode === "discuss" ? "Discuss" : permissionMode === "ask" ? "Ask" : "Auto"}`}
                  >
                    {permissionMode === "discuss" ? "Discuss" : permissionMode === "ask" ? "Ask" : "Auto"}
                  </span>
                  {selectedRoleId && (
                    <span className="composer-tag composer-tag-role" title="Role target">
                      <Bot size={11} aria-hidden="true" />
                      <span>{roles.find((role) => role.id === selectedRoleId)?.name ?? "Role"}</span>
                    </span>
                  )}
                </>
              )}
            </div>
            <div className="composer-trailing">
              <ThinkingPicker
                models={models}
                selectedModelId={selectedModelId}
                thinkingEffort={thinkingEffort}
                setThinkingEffort={setThinkingEffort}
                disabled={!selectedSession || activeRun || Boolean(selectedRoleId)}
              />
              <label className="composer-chip">
                <span className="sr-only">Model</span>
                <select
                  className="composer-chip-select"
                  value={selectedModelId ?? ""}
                  disabled={!selectedSession || activeRun || models.length === 0 || Boolean(selectedRoleId)}
                  onChange={(event) => setSelectedModelId(event.target.value)}
                  title="Model for the next run"
                >
                  {models.length === 0 && <option value="">No model configured</option>}
                  {models.map((model) => <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}
                </select>
              </label>
              {activeRun && (
                <button type="button" className="composer-followup" aria-label="Queue follow-up"
                  disabled={!input.trim() || compacting || reconnecting} onClick={followUp}>
                  <svg width="12" height="12" viewBox="0 0 10 10" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <line x1="5" y1="1" x2="5" y2="6" /><polyline points="2.5 3.5 5 1 7.5 3.5" />
                    <line x1="2" y1="9" x2="8" y2="9" />
                  </svg>
                  <span>Follow-up</span>
                </button>
              )}
              {activeRun ? (
                <button type="button" className="composer-primary" aria-label="Stop run" title="Stop run" onClick={cancel}>
                  <svg width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true">
                    <rect x="1.5" y="1.5" width="7" height="7" rx="1.5" fill="currentColor" />
                  </svg>
                </button>
              ) : (
                <button type="submit" className="composer-primary" aria-label="Send"
                  disabled={!selectedSession || (!permissionReady && !selectedRoleId) || (!input.trim() && !hasPendingImage && textAttachments.length === 0)}>
                  <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <path d="M8.3125 0.980183C8.66767 1.0531 8.97902 1.20418 9.2627 1.43233C9.48724 1.61297 9.73029 1.85793 9.97949 2.10714L14.707 6.83468L13.293 8.24874L9 3.95577V15.0417H7V3.95577L2.70703 8.24874L1.29297 6.83468L6.02051 2.10714C6.26971 1.85793 6.51277 1.61297 6.7373 1.43233C6.97662 1.23986 7.28445 1.04402 7.6875 0.980183C7.8973 0.947006 8.1031 0.95516 8.3125 0.980183Z" fill="currentColor" />
                  </svg>
                </button>
              )}
            </div>
          </div>
        </div>
      </form>
      <div className="composer-status-line">
        {activeRun && <span className="composer-config-hint">{permissionMode} is frozen for this run</span>}
        {!permissionReady && selectedSession && !selectedRoleId && <span className="composer-config-hint is-danger">Policy unavailable</span>}
        {expanding && <span className="composer-status" aria-live="polite">Expanding prompt…</span>}
        {expandDiag && <span className="composer-status diag-warning" aria-live="polite">{expandDiag}</span>}
        {pendingFollowUps.length > 0 && (
          <div className="composer-followup-queue" aria-live="polite" role="status">
            <ListPlus size={13} aria-hidden="true" />
            <span>Queued: {pendingFollowUps.map(item => item.text).join(" · ")}</span>
          </div>
        )}
      </div>
    </div>
  );
}

const THINKING_LEVEL_DESC: Record<ThinkingEffort, string> = {
  default: "默认",
  low: "低强度推理",
  medium: "中等推理",
  high: "高强度推理",
};

function ThinkingPicker({ models, selectedModelId, thinkingEffort, setThinkingEffort, disabled }: {
  models: ModelProfile[];
  selectedModelId: string | null;
  thinkingEffort: ThinkingEffort;
  setThinkingEffort: (effort: ThinkingEffort) => void;
  disabled: boolean;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const selected = models.find((model) => model.id === selectedModelId) ?? null;
  const supports = Boolean(selected?.supportsThinking);
  const efforts = selected?.supportedThinkingEfforts?.length
    ? selected.supportedThinkingEfforts
    : (["default"] as ThinkingEffort[]);
  const active = efforts.includes(thinkingEffort) ? thinkingEffort : "default";
  const canUse = !disabled && supports && Boolean(selected);

  // 切换模型后若新模型不支持当前档位，回退到 default。
  useEffect(() => {
    if (selected && !efforts.includes(thinkingEffort)) setThinkingEffort("default");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedModelId]);

  useEffect(() => {
    if (!open) return;
    const onPointer = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) setOpen(false);
    };
    window.addEventListener("mousedown", onPointer);
    return () => window.removeEventListener("mousedown", onPointer);
  }, [open]);

  return (
    <div className="composer-chip" ref={ref}>
      <button type="button" className="composer-chip-button" aria-label="Thinking effort" aria-expanded={open}
        title={supports ? "切换推理强度" : "当前模型不支持思考模式"}
        disabled={!canUse}
        onClick={() => setOpen((value) => !value)}>
        <span className="composer-thinking-label">{active}</span>
        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M3 4.5L6 7.5L9 4.5" />
        </svg>
      </button>
      {open && (
        <div className="composer-thinking-menu" role="listbox" aria-label="Thinking effort">
          {efforts.map((lvl) => {
            const isActive = lvl === active;
            return (
              <button key={lvl} type="button" role="option" aria-selected={isActive}
                onClick={() => { setThinkingEffort(lvl); setOpen(false); }}>
                {isActive
                  ? <svg width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="var(--accent)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><polyline points="1.5 5 4 7.5 8.5 2.5" /></svg>
                  : <span className="composer-thinking-check" aria-hidden="true" />}
                <span className="composer-thinking-item">{lvl}</span>
                <span className="composer-thinking-desc">{THINKING_LEVEL_DESC[lvl]}</span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
