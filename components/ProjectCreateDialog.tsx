"use client";

import { FolderPlus, X } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";

// ProjectCreateDialog replaces the previous native prompt() pair with a
// proper dialog. The host path is resolved by the Worker into a jailed
// workspace; the browser never reads or writes it directly.
export function ProjectCreateDialog({ busy, error, onCreate, onClose }: {
  busy: boolean;
  error: string | null;
  onCreate: (name: string, hostPath: string) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [hostPath, setHostPath] = useState("");
  const nameInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    nameInputRef.current?.focus();
  }, []);

  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [onClose]);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || !name.trim() || !hostPath.trim()) return;
    onCreate(name.trim(), hostPath.trim());
  }

  return <div
    className="project-create-overlay"
    onPointerDown={(event) => { if (event.target === event.currentTarget) onClose(); }}
  >
    <div className="project-create-dialog" role="dialog" aria-modal="true" aria-label="New project">
      <div className="project-create-header">
        <span><FolderPlus size={15} aria-hidden="true" /> New project</span>
        <button type="button" className="follow-up-close" aria-label="Close" title="Close" onClick={onClose}>
          <X size={14} aria-hidden="true" />
        </button>
      </div>
      <form onSubmit={submit} className="project-create-form">
        <label>
          Project name
          <input
            ref={nameInputRef}
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="RNA screen"
            required
          />
        </label>
        <label>
          Host path
          <input
            value={hostPath}
            onChange={(event) => setHostPath(event.target.value)}
            placeholder="/data/projects/rna-screen"
            required
          />
          <small>Directory on this machine; the Worker maps it into a jailed workspace.</small>
        </label>
        {error && <div className="project-create-error" role="alert">{error}</div>}
        <div className="project-create-actions">
          <button type="button" className="secondary-btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="project-create-submit" disabled={busy || !name.trim() || !hostPath.trim()}>
            {busy ? "Creating…" : "Create project"}
          </button>
        </div>
      </form>
    </div>
  </div>;
}
