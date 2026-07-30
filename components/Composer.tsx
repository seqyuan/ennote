"use client";

import { Bot, CornerDownRight, ImagePlus, Minimize2, Paperclip, Send, Square, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type DragEvent, type FormEvent, type KeyboardEvent } from "react";
import type { ModelProfile } from "@/components/settings/types";
import type { PermissionMode } from "@/lib/permission-mode";

export interface TextAttachment {
  id: string;
  name: string;
  size: number;
  text: string;
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
  textAttachments: TextAttachment[];
  removeTextAttachment: (id: string) => void;
  attachFiles: (files: File[]) => void;
  uploadImage: (file: File) => void;
  submit: () => void;
  steer: () => void;
  cancel: () => void;
  compactSession: () => void;
}

export function Composer({
  selectedSession, activeLeafMessageId, input, setInput, activeRun, compacting, hasPendingImage, reconnecting,
  permissionMode, permissionReady, setPermissionMode, models, selectedModelId, setSelectedModelId,
  textAttachments, removeTextAttachment, attachFiles, uploadImage, submit, steer, cancel, compactSession,
}: ComposerProps) {
  const textarea = useRef<HTMLTextAreaElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const [isDragOver, setIsDragOver] = useState(false);
  const dragCounter = useRef(0);

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

      {textAttachments.length > 0 && (
        <div className="composer-attachments" aria-label="Attached text files">
          {textAttachments.map((attachment) => (
            <span className="composer-attachment" key={attachment.id} title={attachment.name}>
              <Paperclip size={12} aria-hidden="true" />
              <span>{attachment.name}</span>
              <small>{formatFileSize(attachment.size)}</small>
              <button type="button" onClick={() => removeTextAttachment(attachment.id)} aria-label={`Remove ${attachment.name}`}><X size={11} /></button>
            </span>
          ))}
        </div>
      )}

      <div className="composer-toolbar">
        <div className="permission-control">
          <span className="toolbar-label">Permission</span>
          <div className="permission-segment" role="group" aria-label="Permission mode">
            {(["discuss", "ask", "auto"] as PermissionMode[]).map((mode) => (
              <button
                key={mode}
                type="button"
                aria-pressed={permissionMode === mode}
                disabled={!selectedSession || activeRun || !permissionReady}
                onClick={() => setPermissionMode(mode)}
              >
                {mode === "discuss" ? "Discuss" : mode === "ask" ? "Ask" : "Auto"}
              </button>
            ))}
          </div>
        </div>

        <label className="model-control">
          <Bot size={13} aria-hidden="true" />
          <span className="sr-only">Model</span>
          <select
            value={selectedModelId ?? ""}
            disabled={!selectedSession || activeRun || models.length === 0}
            onChange={(event) => setSelectedModelId(event.target.value)}
            title="Model for the next run"
          >
            {models.length === 0 && <option value="">No model configured</option>}
            {models.map((model) => <option key={model.id} value={model.id}>{model.displayName || model.modelName}</option>)}
          </select>
        </label>

        {activeRun && <span className="permission-frozen">{permissionMode} is frozen for this run</span>}
        {!permissionReady && selectedSession && <span className="permission-unavailable">Policy unavailable</span>}
      </div>

      <form className="composer" onSubmit={onSubmit} ref={form}>
        <div className="composer-editor">
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
          <div className="composer-actions-secondary">
            {!activeRun && (
              <>
                <button type="button" className="icon-command" disabled={!canAttach} onClick={() => fileInput.current?.click()} aria-label="Attach files" title="Attach image or text files">
                  <Paperclip size={16} aria-hidden="true" />
                </button>
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
                <label className={`icon-command file-command ${!canAttach ? "is-disabled" : ""}`} title="Attach image">
                  <ImagePlus size={16} aria-hidden="true" />
                  <span className="sr-only">Attach image</span>
                  <input type="file" accept="image/png,image/jpeg,image/gif,image/webp" disabled={!canAttach} onChange={(event) => {
                    const file = event.target.files?.[0];
                    if (file) uploadImage(file);
                    event.currentTarget.value = "";
                  }} />
                </label>
                <button type="button" className="icon-command" onClick={compactSession} disabled={!selectedSession || !activeLeafMessageId} aria-label="Compact context" title="Compact context">
                  <Minimize2 size={16} aria-hidden="true" />
                </button>
              </>
            )}
          </div>
        </div>
        <div className="composer-primary-actions">
          {activeRun ? (
            <>
              <button type="submit" className="command-button steer-command" aria-label="Steer" disabled={!input.trim() || compacting || reconnecting}>
                <CornerDownRight size={16} aria-hidden="true" /><span>Steer</span>
              </button>
              <button type="button" className="icon-command stop-command" onClick={cancel} aria-label="Stop run" title="Stop run">
                <Square size={15} fill="currentColor" aria-hidden="true" />
              </button>
            </>
          ) : (
            <button type="submit" className="command-button send-command" aria-label="Send" disabled={!selectedSession || !permissionReady || (!input.trim() && !hasPendingImage && textAttachments.length === 0)}>
              <Send size={16} aria-hidden="true" /><span>Send</span>
            </button>
          )}
        </div>
      </form>
    </div>
  );
}

function formatFileSize(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KB`;
}
