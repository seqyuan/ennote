"use client";

import {
  Check, ChevronRight, FilePenLine, Folder, FolderOpen, Plus,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useT } from "@/components/LocaleProvider";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type DirectoryEntry = components["schemas"]["HostDirectoryEntry"];
type DirectoryListing = components["schemas"]["HostDirectoryListing"];

function separatorFor(listing: DirectoryListing): "/" | "\\" {
  return listing.home.includes("\\") ? "\\" : "/";
}

function basename(path: string): string {
  const parts = path.replace(/[\\/]+$/, "").split(/[\\/]/);
  return parts.at(-1) || path;
}

function visibleCrumbs(listing: DirectoryListing, homeLabel: string): DirectoryEntry[] {
  const homeIndex = listing.crumbs.findIndex((crumb) => crumb.path === listing.home);
  if (homeIndex < 0) return listing.crumbs;
  return [
    { name: homeLabel, path: listing.home, hidden: false },
    ...listing.crumbs.slice(homeIndex + 1),
  ];
}

function DirectoryColumn({
  listing, selectedPath, showHidden, disabled, onSelect,
}: {
  listing: DirectoryListing;
  selectedPath: string | null;
  showHidden: boolean;
  disabled: boolean;
  onSelect: (entry: DirectoryEntry) => void;
}) {
  const entries = showHidden ? listing.entries : listing.entries.filter((entry) => !entry.hidden || entry.path === selectedPath);
  return (
    <div className="project-picker-column" role="list">
      {entries.map((entry) => {
        const selected = entry.path === selectedPath;
        return (
          <span role="listitem" className="project-picker-row-seat" key={entry.path}>
            <button
              type="button"
              className={`project-picker-row${selected ? " selected" : ""}`}
              aria-current={selected || undefined}
              disabled={disabled}
              onClick={() => onSelect(entry)}
            >
              {selected
                ? <FolderOpen size={16} className="project-picker-folder selected" aria-hidden="true" />
                : <Folder size={16} className="project-picker-folder" aria-hidden="true" />}
              <span>{entry.name}</span>
              <ChevronRight size={12} className="project-picker-row-chevron" aria-hidden="true" />
            </button>
          </span>
        );
      })}
    </div>
  );
}

export function ProjectCreateDialog({ busy, onCreate, onClose }: {
  busy: boolean;
  onCreate: (name: string, hostPath: string) => Promise<void>;
  onClose: () => void;
}) {
  const t = useT();
  const [parent, setParent] = useState<DirectoryListing | null>(null);
  const [selected, setSelected] = useState<DirectoryEntry | null>(null);
  const [child, setChild] = useState<DirectoryListing | null>(null);
  const [loading, setLoading] = useState(true);
  const [localError, setLocalError] = useState<string | null>(null);
  const [pathDraft, setPathDraft] = useState<string | null>(null);
  const [showHidden, setShowHidden] = useState(false);
  const [folderDraft, setFolderDraft] = useState<string | null>(null);
  const [creatingFolder, setCreatingFolder] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const requestSequence = useRef(0);
  const pathInputRef = useRef<HTMLInputElement>(null);

  const listDirectory = useCallback(async (path?: string): Promise<DirectoryListing> => {
    const query = path ? `?${new URLSearchParams({ path })}` : "";
    return apiFetch<DirectoryListing>(`/v1/host/directories${query}`);
  }, []);

  const land = useCallback(async (path?: string) => {
    const sequence = ++requestSequence.current;
    setLoading(true);
    setLocalError(null);
    try {
      const listing = await listDirectory(path);
      if (sequence !== requestSequence.current) return;
      setParent(listing);
      setSelected(null);
      setChild(null);
      setPathDraft(null);
    } catch (reason) {
      if (sequence === requestSequence.current) setLocalError((reason as Error).message);
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }, [listDirectory]);

  useEffect(() => {
    const sequence = ++requestSequence.current;
    void listDirectory().then((listing) => {
      if (sequence !== requestSequence.current) return;
      setParent(listing);
      setLoading(false);
    }, (reason: unknown) => {
      if (sequence !== requestSequence.current) return;
      setLocalError((reason as Error).message);
      setLoading(false);
    });
    return () => { requestSequence.current += 1; };
  }, [listDirectory]);

  useEffect(() => {
    if (pathDraft !== null) pathInputRef.current?.focus();
  }, [pathDraft]);

  const selectFromParent = useCallback(async (entry: DirectoryEntry) => {
    const sequence = ++requestSequence.current;
    setSelected(entry);
    setChild(null);
    setLoading(true);
    setLocalError(null);
    try {
      const listing = await listDirectory(entry.path);
      if (sequence === requestSequence.current) setChild(listing);
    } catch (reason) {
      if (sequence === requestSequence.current) {
        setSelected(null);
        setLocalError((reason as Error).message);
      }
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }, [listDirectory]);

  const advance = useCallback((entry: DirectoryEntry) => {
    if (!child) return;
    setParent(child);
    void selectFromParent(entry);
  }, [child, selectFromParent]);

  const targetPath = selected?.path ?? parent?.path ?? null;
  const targetName = targetPath ? basename(targetPath) : "";
  const crumbs = useMemo(() => parent ? visibleCrumbs(parent, t("directory.home")) : [], [parent, t]);
  const interactionLocked = busy || creatingFolder;

  const openPathEditor = () => {
    if (!parent || interactionLocked) return;
    const base = selected?.path ?? parent.path;
    const separator = separatorFor(parent);
    setPathDraft(base.endsWith(separator) ? base : `${base}${separator}`);
  };

  const createFolder = async () => {
    if (!targetPath || folderDraft === null || !folderDraft.trim() || creatingFolder) return;
    setCreatingFolder(true);
    setCreateError(null);
    try {
      const created = await apiFetch<{ path: string }>("/v1/host/directories", {
        method: "POST",
        body: JSON.stringify({ path: targetPath, name: folderDraft }),
      });
      setFolderDraft(null);
      if (selected && child) {
        const refreshed = await listDirectory(selected.path);
        setParent(refreshed);
        const entry = refreshed.entries.find((item) => item.path === created.path);
        if (entry) await selectFromParent(entry);
      } else {
        const refreshed = await listDirectory(parent?.path);
        setParent(refreshed);
        const entry = refreshed.entries.find((item) => item.path === created.path);
        if (entry) void selectFromParent(entry);
      }
    } catch (reason) {
      setCreateError((reason as Error).message);
    } finally {
      setCreatingFolder(false);
    }
  };

  const confirmOpen = async () => {
    if (!targetPath || busy || loading || pathDraft !== null) return;
    setLocalError(null);
    try {
      await onCreate(targetName, targetPath);
    } catch (reason) {
      setLocalError((reason as Error).message);
    }
  };

  return (
    <div
      className="project-picker-overlay"
      onPointerDown={(event) => { if (event.target === event.currentTarget && !interactionLocked && folderDraft === null) onClose(); }}
    >
      <div className="project-picker-dialog" role="dialog" aria-modal="true" aria-labelledby="project-picker-title">
        <header className="project-picker-header">
          <h2 id="project-picker-title">{t("directory.title")}</h2>
          <div className={`project-picker-crumb-bar${pathDraft !== null ? " editing" : ""}`}>
            {pathDraft === null ? (
              <>
                <nav className="project-picker-crumbs" aria-label={t("directory.path")}>
                  {crumbs.map((crumb, index) => (
                    <span key={crumb.path}>
                      {index > 0 && <ChevronRight size={12} aria-hidden="true" />}
                      <button type="button" disabled={interactionLocked} onClick={() => void land(crumb.path)}>{crumb.name}</button>
                    </span>
                  ))}
                </nav>
                <button type="button" className="project-picker-edit-path" aria-label={t("directory.editPath")} title={t("directory.editPath")} disabled={interactionLocked} onClick={openPathEditor}>
                  <FilePenLine size={14} aria-hidden="true" />
                </button>
              </>
            ) : (
              <input
                ref={pathInputRef}
                value={pathDraft}
                aria-label={t("directory.editPath")}
                disabled={interactionLocked}
                onChange={(event) => setPathDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && pathDraft.trim()) void land(pathDraft);
                  if (event.key === "Escape") { event.stopPropagation(); setPathDraft(null); }
                }}
              />
            )}
          </div>
        </header>

        <main className="project-picker-content">
          <div className="project-picker-miller-row">
            {parent && <DirectoryColumn listing={parent} selectedPath={selected?.path ?? null} showHidden={showHidden} disabled={interactionLocked} onSelect={(entry) => void selectFromParent(entry)} />}
            {selected && <span className="project-picker-divider" />}
            {selected && child && <DirectoryColumn listing={child} selectedPath={null} showHidden={showHidden} disabled={interactionLocked} onSelect={advance} />}
          </div>
          {loading && <div className="project-picker-loading" role="status">{t("directory.loading")}</div>}
          {(parent?.truncated || child?.truncated) && <div className="project-picker-status">{t("directory.truncated")}</div>}
          {localError && <div className="project-picker-error" role="alert">{localError}</div>}
        </main>

        <footer className="project-picker-footer">
          <button type="button" className="project-picker-button outline new-folder" disabled={!parent || loading || interactionLocked || pathDraft !== null} onClick={() => { setFolderDraft(""); setCreateError(null); }}>
            <Plus size={14} aria-hidden="true" /> {t("directory.newFolder")}
          </button>
          <button type="button" className={`project-picker-show-hidden${showHidden ? " active" : ""}`} aria-pressed={showHidden} disabled={interactionLocked} onClick={() => setShowHidden((value) => !value)}>
            {t("directory.showHidden")}{showHidden && <Check size={14} aria-hidden="true" />}
          </button>
          <span className="project-picker-footer-gap" />
          <button type="button" className="project-picker-button outline" disabled={interactionLocked} onClick={onClose}>{t("directory.cancel")}</button>
          <button type="button" className="project-picker-button primary" disabled={!targetPath || loading || interactionLocked || pathDraft !== null} onClick={() => void confirmOpen()}>
            {busy ? t("directory.opening") : t("directory.open")}
          </button>
        </footer>
      </div>

      {folderDraft !== null && (
        <div className="project-picker-nested-overlay" onPointerDown={(event) => { if (event.target === event.currentTarget && !creatingFolder) setFolderDraft(null); }}>
          <div className="project-picker-create-dialog" role="dialog" aria-modal="true" aria-labelledby="project-folder-title">
            <h3 id="project-folder-title">{t("directory.newFolder")}</h3>
            <p>{t("directory.createIn").replace("{name}", targetName)}</p>
            <input
              value={folderDraft}
              aria-label={t("directory.folderName")}
              placeholder={t("directory.untitledFolder")}
              autoFocus
              disabled={creatingFolder}
              onChange={(event) => setFolderDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void createFolder();
                if (event.key === "Escape" && !creatingFolder) { event.stopPropagation(); setFolderDraft(null); }
              }}
            />
            {createError && <div className="project-picker-error" role="alert">{createError}</div>}
            <div className="project-picker-create-actions">
              <button type="button" className="project-picker-button outline" disabled={creatingFolder} onClick={() => setFolderDraft(null)}>{t("directory.cancel")}</button>
              <button type="button" className="project-picker-button primary" disabled={creatingFolder || !folderDraft.trim()} onClick={() => void createFolder()}>{t("directory.create")}</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
