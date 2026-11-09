"use client";

import {
  ArrowUp, File, FileSpreadsheet, FileText, ListPlus, Paperclip, Plus, Presentation, Square, X,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState, type DragEvent, type FormEvent, type KeyboardEvent, type PointerEvent as ReactPointerEvent } from "react";
import { useT } from "@/components/LocaleProvider";
import { ComposerPicker, type ComposerPickerHandle } from "@/components/ComposerPicker";
import { ComposerTargetPills } from "@/components/ComposerTargetPills";
import { PermissionChip } from "@/components/PermissionChip";
import { ModelSelect } from "@/components/ModelSelect";
import { ContextMeter } from "@/components/ContextMeter";
import type { ModelProfile, ProviderProfile } from "@/components/settings/types";
import type { SessionContextUsage, SessionStats } from "@/hooks/chat-controller-types";
import { artifactPreviewURL } from "@/lib/artifacts";
import type { PermissionMode, ThinkingEffort } from "@/lib/permission-mode";
import { useComposerPicker, type ComposerPickerData } from "@/hooks/useComposerPicker";
import { QueueDock, type QueuedMessage } from "./QueueDock";

const COMPOSER_MIN_HEIGHT = 120;
const COMPOSER_MAX_HEIGHT = 336;
const COMPOSER_HEIGHT_KEY = "ennote-composer-height";

function clampComposerHeight(value: number): number {
  return Math.min(COMPOSER_MAX_HEIGHT, Math.max(COMPOSER_MIN_HEIGHT, value));
}

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

type AttachmentIcon = { Icon: typeof FileText; color: string };

const ATTACHMENT_DOC_ICONS: Record<string, AttachmentIcon> = {
  pdf: { Icon: FileText, color: "var(--danger)" },
  docx: { Icon: FileText, color: "var(--info)" },
  doc: { Icon: FileText, color: "var(--info)" },
  xlsx: { Icon: FileSpreadsheet, color: "var(--success)" },
  xls: { Icon: FileSpreadsheet, color: "var(--success)" },
  pptx: { Icon: Presentation, color: "var(--warning)" },
  ppt: { Icon: Presentation, color: "var(--warning)" },
};

function docAttachmentIcon(name: string): AttachmentIcon {
  const ext = name.toLowerCase().split(".").pop() ?? "";
  return ATTACHMENT_DOC_ICONS[ext] ?? { Icon: File, color: "var(--content-3)" };
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
  questionPending?: boolean;
  compacting: boolean;
  hasPendingImage: boolean;
  reconnecting: boolean;
  permissionMode: PermissionMode;
  permissionReady: boolean;
  setPermissionMode: (mode: PermissionMode) => void;
  models: ModelProfile[];
  providers: ProviderProfile[];
  selectedModelId: string | null;
  setSelectedModelId: (modelId: string) => void;
  thinkingEffort: ThinkingEffort;
  setThinkingEffort: (effort: ThinkingEffort) => void;
  textAttachments: TextAttachment[];
  removeTextAttachment: (id: string) => void;
  pendingImage: PendingImage | null;
  clearPendingImage: () => void;
  attachFiles: (files: File[]) => void;
  uploadImage: (file: File) => void;
  submit: () => void;
  steer: () => void;
  steerText: (text: string) => Promise<boolean>;
  followUp: () => void;
  cancel: () => void;
  cancelFollowUp: (id: string) => Promise<boolean>;
  pendingFollowUps: { id: string; text: string }[];
  compactSession: () => void;
  /** No workspace: the dashed card opens the project picker (dsh inert). */
  inert?: boolean;
  /** Centered empty-conversation layout (dsh welcome stage / blank session). */
  welcome?: boolean;
  /** No session selected: the inert composer requests a project (welcome picker). */
  onRequestProject?: () => void;
  /** The picker's catalogue and the turn's addressed targets. */
  picker: ComposerPickerData;
  expanding: boolean;
  expandDiag: string | null;
  contextUsage: SessionContextUsage | null;
  stats: SessionStats | null;
  /** Increment to focus the textarea and place the caret at the end. */
  focusNonce?: number;
}

export function Composer({
  selectedSession, activeLeafMessageId, input, setInput, activeRun, questionPending = false, compacting, hasPendingImage, reconnecting,
  permissionMode, permissionReady, setPermissionMode, models, providers, selectedModelId, setSelectedModelId, thinkingEffort, setThinkingEffort,
  textAttachments, removeTextAttachment, pendingImage, clearPendingImage, attachFiles, uploadImage, submit, steer, steerText, followUp, cancel, cancelFollowUp, compactSession,
  pendingFollowUps,
  picker, expanding, expandDiag, contextUsage, stats, focusNonce,
  onRequestProject, inert = false, welcome = false,
}: ComposerProps) {
  const textarea = useRef<HTMLTextAreaElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const imageInput = useRef<HTMLInputElement>(null);
  const [isDragOver, setIsDragOver] = useState(false);
  const dragCounter = useRef(0);
  const [manualHeight, setManualHeight] = useState(() => {
    if (typeof window === "undefined") return 0;
    try {
      const parsed = Number.parseInt(localStorage.getItem(COMPOSER_HEIGHT_KEY) ?? "", 10);
      return Number.isFinite(parsed) ? clampComposerHeight(parsed) : 0;
    } catch { return 0; }
  });
  const [resizing, setResizing] = useState(false);
  const manualHeightRef = useRef(manualHeight);
  const resizeStart = useRef<{ y: number; height: number } | null>(null);
  const t = useT();

  const applyManualHeight = useCallback((value: number) => {
    manualHeightRef.current = value;
    setManualHeight(value);
  }, []);

  const persistManualHeight = useCallback((value: number) => {
    try {
      if (value > 0) localStorage.setItem(COMPOSER_HEIGHT_KEY, String(value));
      else localStorage.removeItem(COMPOSER_HEIGHT_KEY);
    } catch { /* storage unavailable */ }
  }, []);

  const resetComposerHeight = useCallback(() => {
    applyManualHeight(0);
    persistManualHeight(0);
  }, [applyManualHeight, persistManualHeight]);

  const beginResize = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || !event.isPrimary) return;
    event.preventDefault();
    const startHeight = manualHeightRef.current || textarea.current?.offsetHeight || COMPOSER_MIN_HEIGHT;
    resizeStart.current = { y: event.clientY, height: startHeight };
    setResizing(true);
    event.currentTarget.setPointerCapture(event.pointerId);
    document.body.style.cursor = "row-resize";
    document.body.style.userSelect = "none";
  }, []);

  const moveResize = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    const start = resizeStart.current;
    if (!start) return;
    event.preventDefault();
    applyManualHeight(clampComposerHeight(start.height + start.y - event.clientY));
  }, [applyManualHeight]);

  const endResize = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (!resizeStart.current) return;
    resizeStart.current = null;
    setResizing(false);
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
    persistManualHeight(manualHeightRef.current);
  }, [persistManualHeight]);

  const resizeByKeyboard = useCallback((delta: number) => {
    const current = manualHeightRef.current || textarea.current?.offsetHeight || COMPOSER_MIN_HEIGHT;
    const next = clampComposerHeight(current + delta);
    applyManualHeight(next);
    persistManualHeight(next);
  }, [applyManualHeight, persistManualHeight]);

  useEffect(() => {
    if (!focusNonce) return;
    const element = textarea.current;
    if (!element) return;
    element.focus();
    const end = element.value.length;
    element.setSelectionRange(end, end);
  }, [focusNonce]);

  useEffect(() => {
    const element = textarea.current;
    if (!element) return;
    element.style.height = "0px";
    element.style.height = `${Math.min(Math.max(element.scrollHeight, manualHeight), COMPOSER_MAX_HEIGHT)}px`;
  }, [input, manualHeight]);

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (activeRun) {
      if (questionPending) return;
      if (!compacting && !reconnecting) steer();
    } else {
      submit();
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (!selectedSession) return;
    // The picker owns Arrow/Enter/Escape while it is open: otherwise Enter would
    // send `@sec` as a chat message instead of picking the hero it names.
    if (!event.nativeEvent.isComposing && composerPicker.handleKeyDown(event)) return;
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    if (activeRun && questionPending) return;
    form.current?.requestSubmit();
  }

  const editQueued = useCallback(async (item: QueuedMessage) => {
    if (!(await cancelFollowUp(item.id))) return;
    setInput(item.text);
    window.requestAnimationFrame(() => textarea.current?.focus());
  }, [cancelFollowUp, setInput]);

  const removeQueued = useCallback((item: QueuedMessage) => {
    void cancelFollowUp(item.id);
  }, [cancelFollowUp]);

  const steerQueued = useCallback(async (item: QueuedMessage) => {
    if (await cancelFollowUp(item.id)) await steerText(item.text);
  }, [cancelFollowUp, steerText]);

  const canAttach = Boolean(selectedSession) && !activeRun && !compacting;
  const pickerRef = useRef<ComposerPickerHandle>(null);
  const composerPicker = useComposerPicker({
    input, setInput, data: picker, canAct: canAttach, panel: pickerRef,
    onAttachFile: () => fileInput.current?.click(),
    onAttachImage: () => imageInput.current?.click(),
    onCompact: compactSession,
  });
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
      {isDragOver && <div className="composer-drop-zone"><Paperclip size={18} />{t("composer.dropFiles")}</div>}
      <QueueDock items={pendingFollowUps} onEdit={editQueued} onRemove={removeQueued} onSteer={steerQueued} />

      {(pendingImage || textAttachments.length > 0) && (
        <div className="composer-attachments" aria-label={t("composer.attachedFiles")}>
          {pendingImage && selectedSession && (
            <div className="composer-image-preview">
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img src={artifactPreviewURL(selectedSession, pendingImage.id)} alt={pendingImage.name} />
              <button type="button" onClick={clearPendingImage} aria-label={t("composer.removeImage")}>
                <X size={10} aria-hidden="true" />
              </button>
            </div>
          )}
          {textAttachments.map((attachment) => {
            const icon = docAttachmentIcon(attachment.name);
            const Icon = icon.Icon;
            const ext = attachment.name.toLowerCase().split(".").pop() ?? "";
            return (
              <span className="composer-attachment" key={attachment.id} title={attachment.name}>
                <span className="composer-attachment-icon" style={{ color: icon.color }}>
                  <Icon size={14} aria-hidden="true" />
                </span>
                <span className="composer-attachment-body">
                  <span className="composer-attachment-name">{attachment.name}</span>
                  <span className="composer-attachment-meta">{ext.toUpperCase()} · {formatAttachmentSize(attachment.size)} · text</span>
                </span>
                <button type="button" onClick={() => removeTextAttachment(attachment.id)} aria-label={`${t("composer.remove")} ${attachment.name}`}>
                  <X size={9} aria-hidden="true" />
                </button>
              </span>
            );
          })}
        </div>
      )}

      <ComposerTargetPills
        hero={picker.heroes.find((hero) => hero.id === picker.selectedHeroId) ?? null}
        graph={picker.pendingGraph}
        skills={picker.pendingSkills.map((id) => {
          const skill = picker.skills.find((entry) => entry.id === id);
          return { id, label: skill?.label ?? id };
        })}
        onClearHero={() => picker.onSelectHero("", "")}
        onClearGraph={picker.onClearGraph}
        onRemoveSkill={picker.onToggleSkill}
      />

      <form className="composer" onSubmit={onSubmit} ref={form}>
        <div
          className="composer-resizer"
          data-resizing={resizing || undefined}
          role="separator"
          aria-orientation="horizontal"
          aria-label={t("composer.hint.resizeAria")}
          tabIndex={0}
          onPointerDown={beginResize}
          onPointerMove={moveResize}
          onPointerUp={endResize}
          onPointerCancel={endResize}
          onDoubleClick={resetComposerHeight}
          onKeyDown={(event) => {
            if (event.key === "ArrowUp") { event.preventDefault(); resizeByKeyboard(24); return; }
            if (event.key === "ArrowDown") { event.preventDefault(); resizeByKeyboard(-24); return; }
            if (event.key === "Enter" || event.key === " ") { event.preventDefault(); resetComposerHeight(); }
          }}
        />
        <div
          className={`composer-editor${inert ? " composer-no-session" : ""}`}
          onClick={() => { if (inert) onRequestProject?.(); }}
        >
          <ComposerPicker
            ref={pickerRef}
            open={composerPicker.open}
            sections={composerPicker.sections}
            filter={composerPicker.filter}
            showFilter={composerPicker.fromPlus}
            ariaLabel={t("composer.picker")}
            onFilterChange={composerPicker.setPlusFilter}
            onPick={composerPicker.pick}
            onClose={() => { composerPicker.close(); textarea.current?.focus(); }}
          />
          <textarea
            ref={textarea}
            value={input}
            onChange={composerPicker.onInputChange}
            onKeyDown={onKeyDown}
            rows={1}
            readOnly={inert}
            disabled={compacting}
            placeholder={compacting ? t("composer.placeholderCompacting") : questionPending ? t("composer.answerQuestionFirst") : activeRun ? t("composer.placeholderSteer") : inert ? t("composer.placeholderNoSession") : welcome ? t("composer.placeholderWelcome") : t("composer.placeholderDefault")}
            aria-label={activeRun ? t("composer.steerAria") : t("composer.messageAria")}
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
                aria-expanded={composerPicker.open}
                aria-haspopup="dialog"
                onPointerDown={composerPicker.notePointerClose}
                aria-label={t("composer.insert")}
                title={t("composer.insert")}
                disabled={!selectedSession || compacting}
                onClick={composerPicker.toggle}
              >
                <Plus size={14} aria-hidden="true" />
              </button>
            </div>
            <div className="composer-trailing">
              {activeRun && (
                <button type="button" className="composer-followup" aria-label={t("composer.queueFollowUp")}
                  disabled={!input.trim() || compacting || reconnecting || questionPending} onClick={followUp}>
                  <ListPlus size={12} aria-hidden="true" />
                  <span>{t("composer.followUp")}</span>
                </button>
              )}
              {activeRun ? (
                <button type="button" className="composer-primary" aria-label={t("composer.stopRun")} title={t("composer.stopRun")} onClick={cancel}>
                  <Square size={12} fill="currentColor" aria-hidden="true" />
                </button>
              ) : (
                <button type="submit" className="composer-primary" aria-label={t("composer.send")}
                  disabled={inert || !selectedSession || (!permissionReady && !picker.selectedHeroId)
                    || (!input.trim() && !hasPendingImage && textAttachments.length === 0 && !picker.pendingGraph)}>
                  <ArrowUp size={16} aria-hidden="true" />
                </button>
              )}
            </div>
          </div>
        </div>
        {/* Outside the editor box on purpose: the run configuration belongs to the
            composer, not inside its border. Keeping it after the editor also keeps
            the tab order following the text area instead of preceding it. */}
        <div className="composer-context-bar" aria-label={t("composer.runContext")}>
          <PermissionChip
            permissionMode={permissionMode}
            permissionReady={permissionReady}
            setPermissionMode={setPermissionMode}
            disabled={!selectedSession || activeRun || compacting || Boolean(picker.selectedHeroId)}
            frozen={activeRun}
          />
          <span className="composer-context-spacer" aria-hidden="true" />
          <ModelSelect
            models={models}
            providers={providers}
            selectedModelId={selectedModelId}
            setSelectedModelId={setSelectedModelId}
            thinkingEffort={thinkingEffort}
            setThinkingEffort={setThinkingEffort}
            disabled={!selectedSession || activeRun || models.length === 0 || Boolean(picker.selectedHeroId)}
          />
          <ContextMeter contextUsage={contextUsage} stats={stats} />
        </div>
      </form>
      <div className="composer-status-line">
        {!permissionReady && selectedSession && !picker.selectedHeroId && <span className="composer-config-hint is-danger">{t("composer.policyUnavailable")}</span>}
        {expanding && <span className="composer-status" aria-live="polite">{t("composer.expanding")}</span>}
        {expandDiag && <span className="composer-status diag-warning" aria-live="polite">{expandDiag}</span>}
      </div>
    </div>
  );
}
